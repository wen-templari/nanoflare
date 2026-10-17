package nanoflare

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
)

// ExecutionSnapshot freezes effective environment, code and resource declarations.
// Callers must resolve variables and decrypt secrets before freezing. Compile
// options are opaque to this package, but participate in execution identity.
// Accessors return independent copies, including otherwise JSON-hidden secrets.
type ExecutionSnapshot struct {
	id     string
	active ActiveDeployment
}

func FreezeExecution(active ActiveDeployment, compileOptions ...string) (*ExecutionSnapshot, error) {
	if active.App.ID == "" || active.Deployment.ID == "" || (active.Deployment.AppID != "" && active.Deployment.AppID != active.App.ID) {
		return nil, errors.New("execution requires matching application and deployment identity")
	}
	frozen := cloneExecutionActive(active)
	// Routing metadata and the legacy app-wide credential are not execution state.
	identity := ActiveDeployment{App: App{ID: frozen.App.ID, OrgID: frozen.App.OrgID}, Deployment: frozen.Deployment}
	identity.Deployment.Port = 0
	identity.Deployment.CommitHash = ""
	identity.Deployment.CommitMessage = ""
	identity.Deployment.CreatedBy = ""
	identity.Deployment.CreatedAt = time.Time{}
	encoded, err := json.Marshal(struct {
		Active         ActiveDeployment
		Secrets        map[string]string
		ObjectKey      string
		CompileOptions []string
	}{identity, frozen.App.SecretValues, frozen.Deployment.ObjectKey, compileOptions})
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(encoded)
	return &ExecutionSnapshot{id: hex.EncodeToString(hash[:]), active: frozen}, nil
}

func (s *ExecutionSnapshot) ID() string { return s.id }
func (s *ExecutionSnapshot) ActiveDeployment() ActiveDeployment {
	return cloneExecutionActive(s.active)
}

func cloneExecutionActive(a ActiveDeployment) ActiveDeployment {
	a.App.SecretValues = maps.Clone(a.App.SecretValues)
	a.App.Auth.ProtectedRoutes = slices.Clone(a.App.Auth.ProtectedRoutes)
	d := &a.Deployment
	d.Files = slices.Clone(d.Files)
	d.Assets = slices.Clone(d.Assets)
	for i := range d.Assets {
		d.Assets[i].Data = slices.Clone(d.Assets[i].Data)
	}
	d.CompatibilityFlags = slices.Clone(d.CompatibilityFlags)
	d.Triggers.Crons = slices.Clone(d.Triggers.Crons)
	d.Vars = cloneVars(d.Vars)
	d.KVNamespaces = slices.Clone(d.KVNamespaces)
	d.Databases = slices.Clone(d.Databases)
	d.ObjectStorageBuckets = slices.Clone(d.ObjectStorageBuckets)
	d.Services = slices.Clone(d.Services)
	d.AssetConfig.RunWorkerFirst = slices.Clone(d.AssetConfig.RunWorkerFirst)
	return a
}

type ExecutionResourceKind string

const (
	ExecutionKV       ExecutionResourceKind = "kv"
	ExecutionDatabase ExecutionResourceKind = "database"
	ExecutionObject   ExecutionResourceKind = "object"
	ExecutionAssets   ExecutionResourceKind = "assets"
)

var ErrExecutionRetired = errors.New("execution is not accepting admissions")

type executionRevision struct {
	snapshot  *ExecutionSnapshot
	accepting bool
	leases    int
}
type executionAdmission struct {
	revision *executionRevision
	pins     int
}

// ExecutionRegistry is process-local authority. A publication allows fresh
// admissions; retirement only revokes fresh admission, never existing work.
// A restart discards all capability tokens and requires fresh publication.
type ExecutionRegistry struct {
	mu        sync.Mutex
	revisions map[string]*executionRevision
	tokens    map[string]*executionAdmission
}

func NewExecutionRegistry() *ExecutionRegistry {
	return &ExecutionRegistry{revisions: make(map[string]*executionRevision), tokens: make(map[string]*executionAdmission)}
}

func (r *ExecutionRegistry) Publish(snapshot *ExecutionSnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing := r.revisions[snapshot.id]; existing != nil {
		existing.accepting = true
		return
	}
	r.revisions[snapshot.id] = &executionRevision{snapshot: snapshot, accepting: true}
}

func (r *ExecutionRegistry) Retire(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if revision := r.revisions[id]; revision != nil {
		revision.accepting = false
		if revision.leases == 0 {
			delete(r.revisions, id)
		}
	}
}

// ReferencesDeployment prevents asset/bundle collection while a published
// execution or any admitted lifetime still needs the deployment's objects.
func (r *ExecutionRegistry) ReferencesDeployment(appID, deploymentID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, revision := range r.revisions {
		a := revision.snapshot.active
		if a.App.ID == appID && a.Deployment.ID == deploymentID {
			return true
		}
	}
	return false
}

// ReferencesObject also covers object keys shared by variable-only revisions.
func (r *ExecutionRegistry) ReferencesObject(appID, key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, revision := range r.revisions {
		a := revision.snapshot.active
		if a.App.ID != appID {
			continue
		}
		if a.Deployment.ObjectKey == key {
			return true
		}
		for _, asset := range a.Deployment.Assets {
			if asset.ObjectKey == key {
				return true
			}
		}
	}
	return false
}

// ExecutionLease must outlive the response body, WebSocket and waitUntil work.
// Each independent lifetime must Pin before its parent releases, and Release
// exactly once. A canceled response alone does not finish background work.
type ExecutionLease struct {
	registry *ExecutionRegistry
	token    string
	once     sync.Once
}

func (r *ExecutionRegistry) Admit(id string) (*ExecutionLease, error) {
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return nil, err
	}
	token := "nfexec_" + hex.EncodeToString(entropy[:])
	r.mu.Lock()
	defer r.mu.Unlock()
	revision := r.revisions[id]
	if revision == nil || !revision.accepting {
		return nil, ErrExecutionRetired
	}
	revision.leases++
	r.tokens[token] = &executionAdmission{revision: revision, pins: 1}
	return &ExecutionLease{registry: r, token: token}, nil
}

func (l *ExecutionLease) Token() string { return l.token }
func (l *ExecutionLease) Pin() (*ExecutionLease, error) {
	return l.registry.Pin(l.token)
}

// Pin extends an already admitted execution; it never admits a retired revision.
func (r *ExecutionRegistry) Pin(token string) (*ExecutionLease, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.tokens[token]
	if a == nil {
		return nil, ErrInvalidCapability
	}
	a.pins++
	return &ExecutionLease{registry: r, token: token}, nil
}
func (l *ExecutionLease) Release() {
	l.once.Do(func() {
		r := l.registry
		r.mu.Lock()
		defer r.mu.Unlock()
		a := r.tokens[l.token]
		if a == nil {
			return
		}
		a.pins--
		if a.pins != 0 {
			return
		}
		delete(r.tokens, l.token)
		a.revision.leases--
		if !a.revision.accepting && a.revision.leases == 0 {
			delete(r.revisions, a.revision.snapshot.id)
		}
	})
}

// Authorize only grants resources declared by this admission's frozen revision.
// Asset authority is scoped to the exact deployment, including retired assets.
func (r *ExecutionRegistry) Authorize(token string, kind ExecutionResourceKind, resourceID string) (ActiveDeployment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.tokens[token]
	if a == nil {
		return ActiveDeployment{}, ErrInvalidCapability
	}
	d := a.revision.snapshot.active.Deployment
	allowed := false
	switch kind {
	case ExecutionKV:
		for _, b := range d.KVNamespaces {
			allowed = allowed || b.ID == resourceID
		}
	case ExecutionDatabase:
		for _, b := range d.Databases {
			allowed = allowed || b.DatabaseID == resourceID
		}
	case ExecutionObject:
		for _, b := range d.ObjectStorageBuckets {
			allowed = allowed || b.BucketID == resourceID
		}
	case ExecutionAssets:
		allowed = (len(d.Assets) > 0 || d.AssetConfig.Binding != "") && d.ID == resourceID
	}
	if resourceID == "" || !allowed {
		return ActiveDeployment{}, ErrInvalidCapability
	}
	return cloneExecutionActive(a.revision.snapshot.active), nil
}

func (s *Service) Executions() *ExecutionRegistry { return s.executions }

// RegisterExecution accepts a snapshot from the trusted runner control plane.
// It still validates tenant ownership before minting any resource authority.
func (s *Service) RegisterExecution(active ActiveDeployment, compileOptions string) (string, error) {
	app, err := s.store.GetApp(active.App.ID)
	if err != nil {
		return "", err
	}
	if app.OrgID != active.App.OrgID {
		return "", ErrInvalidCapability
	}
	for _, binding := range active.Deployment.KVNamespaces {
		resource, err := s.store.GetKVNamespace(binding.ID)
		if err != nil {
			return "", err
		}
		if resource.OrgID != app.OrgID {
			return "", ErrInvalidCapability
		}
	}
	for _, binding := range active.Deployment.Databases {
		resource, err := s.store.GetDatabase(binding.DatabaseID)
		if err != nil {
			return "", err
		}
		if resource.OrgID != app.OrgID {
			return "", ErrInvalidCapability
		}
	}
	for _, binding := range active.Deployment.ObjectStorageBuckets {
		resource, err := s.store.GetObjectStorageBucket(binding.BucketID)
		if err != nil {
			return "", err
		}
		if resource.OrgID != app.OrgID {
			return "", ErrInvalidCapability
		}
	}
	snapshot, err := FreezeExecution(active, compileOptions)
	if err != nil {
		return "", err
	}
	s.executions.Publish(snapshot)
	return snapshot.ID(), nil
}

// Reserved execution tokens never fall back to app-wide authorization.
func (s *Service) executionAuthorization(token string, kind ExecutionResourceKind, resourceID string) (ActiveDeployment, bool, error) {
	if !strings.HasPrefix(token, "nfexec_") {
		return ActiveDeployment{}, false, nil
	}
	if s.executions == nil {
		return ActiveDeployment{}, true, ErrInvalidCapability
	}
	a, err := s.executions.Authorize(token, kind, resourceID)
	return a, true, err
}
