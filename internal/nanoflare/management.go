package nanoflare

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrManagementConflict = errors.New("script management revision changed")

func (s *Service) CompatibilityDateCeiling() string { return s.maxCompatibilityDate }

func (s *Service) ManagementRequestIdentity(payload []byte) (string, error) {
	canonical, err := canonicalPayload(json.RawMessage(payload))
	if err != nil {
		return "", err
	}
	if s.secrets == nil {
		return hashBytes(canonical), nil
	}
	mac := hmac.New(sha256.New, s.secrets.key)
	_, _ = mac.Write([]byte("nanoflare-management-http-v1\x00"))
	_, _ = mac.Write(canonical)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// Replay before resolving inheritance against a new latest snapshot. Callers
// provide a server-computed identity of the normalized original wire request.
func (s *Service) ReplayManagedRequestForOrg(org, worker, key, identity string) (ManagementUpload, bool, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	release, err := s.store.LockDeploymentUpdates()
	if err != nil {
		return ManagementUpload{}, false, err
	}
	defer release()
	if _, err := s.appForOrg(org, worker); err != nil {
		return ManagementUpload{}, false, err
	}
	p, found, err := s.store.GetPublication(worker, strings.TrimSpace(key))
	if err != nil || !found {
		return ManagementUpload{}, found, err
	}
	if identity == "" || p.RequestIdentity != identity {
		return ManagementUpload{}, true, ErrIdempotencyConflict
	}
	d, err := s.replayPublication(p)
	if err != nil {
		return ManagementUpload{}, true, err
	}
	snapshot, err := s.ManagementUploadRevisionForOrg(org, worker, d.ID)
	return snapshot, true, err
}

// ScriptManagementMetadata retains wire binding types and tags independently
// from traffic selection. Secret bindings contain names/types only.
type ScriptManagementMetadata struct {
	Bindings []json.RawMessage `json:"bindings"`
	Tags     []string          `json:"tags"`
}
type ScriptManagementState struct {
	WorkerID     string                   `json:"worker_id"`
	OrgID        string                   `json:"org_id"`
	Revision     int64                    `json:"revision"`
	DeploymentID string                   `json:"deployment_id"`
	Metadata     ScriptManagementMetadata `json:"metadata"`
	UpdatedAt    time.Time                `json:"updated_at"`
}
type ManagedDeployInput struct {
	AssetUploadToken  string `json:"-"`
	ConfigurationOnly bool   `json:"-"`
	RequestIdentity   string `json:"-"`
	Deployment        DeployInput
	ExpectedRevision  int64
	BaseDeploymentID  string
	Metadata          ScriptManagementMetadata
	Secrets           map[string]string `json:"-"`
	// Destination name -> source name from the base artifact.
	KeepSecrets map[string]string
}
type managedPublication struct {
	ConfigurationOnly bool
	RequestIdentity   string
	ExpectedRevision  int64
	Metadata          ScriptManagementMetadata
	Secrets           []ArtifactSecret
}

func cloneManagementState(state ScriptManagementState) ScriptManagementState {
	data, _ := json.Marshal(state)
	var copy ScriptManagementState
	_ = json.Unmarshal(data, &copy)
	return copy
}

func (s *Store) GetScriptManagementState(worker string) (ScriptManagementState, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.apps[worker]; !ok {
		return ScriptManagementState{}, false, ErrAppNotFound
	}
	state, ok := s.management[worker]
	return cloneManagementState(state), ok, nil
}
func (s *Store) CommitScriptManagementState(state ScriptManagementState, expected int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	app, ok := s.apps[state.WorkerID]
	if !ok || app.OrgID != state.OrgID {
		return ErrAppNotFound
	}
	artifact, ok := s.artifacts[state.DeploymentID]
	if !ok || artifact.Deployment.AppID != state.WorkerID {
		return ErrArtifactNotFound
	}
	current := s.management[state.WorkerID]
	if expected < 0 || current.Revision != expected || state.Revision != expected+1 {
		return ErrManagementConflict
	}
	if s.management == nil {
		s.management = map[string]ScriptManagementState{}
	}
	s.management[state.WorkerID] = cloneManagementState(state)
	return nil
}

func (s *Service) ScriptManagementForOrg(org, worker string) (ScriptManagementState, bool, error) {
	if _, err := s.appForOrg(org, worker); err != nil {
		return ScriptManagementState{}, false, err
	}
	return s.store.GetScriptManagementState(worker)
}

func validateManagementMetadata(metadata ScriptManagementMetadata) error {
	if len(metadata.Bindings) > 128 || len(metadata.Tags) > 128 {
		return errors.New("management metadata limit exceeded")
	}
	encoded, err := json.Marshal(metadata)
	if err != nil || len(encoded) > 1<<20 {
		return errors.New("invalid management metadata")
	}
	seen := map[string]bool{}
	for _, raw := range metadata.Bindings {
		var binding map[string]json.RawMessage
		if err := json.Unmarshal(raw, &binding); err != nil || binding == nil {
			return errors.New("invalid management binding")
		}
		var name, kind string
		_ = json.Unmarshal(binding["name"], &name)
		_ = json.Unmarshal(binding["type"], &kind)
		if name == "" || len(name) > 128 || seen[name] {
			return errors.New("invalid management binding name")
		}
		seen[name] = true
		allowed := map[string]bool{"name": true, "type": true}
		switch kind {
		case "secret_text", "assets":
		case "plain_text":
			allowed["text"] = true
		case "json":
			allowed["json"] = true
		case "kv_namespace":
			allowed["namespace_id"] = true
		case "d1":
			allowed["id"] = true
		case "r2_bucket":
			allowed["bucket_name"] = true
		case "service":
			allowed["service"] = true
			allowed["environment"] = true
		default:
			return errors.New("unsupported management binding type")
		}
		for field := range binding {
			if !allowed[field] {
				return errors.New("unsupported management binding field")
			}
		}
	}
	for _, tag := range metadata.Tags {
		if tag == "" || len(tag) > 256 {
			return errors.New("invalid management tag")
		}
	}
	return nil
}

// ManagedDeployForOrg captures code, metadata and the selected complete secret
// set in one immutable artifact. It never edits the live secret store or publishes
// intermediate revisions. Activation uses the native recoverable publication.
func (s *Service) ManagedDeployForOrg(org, worker string, in ManagedDeployInput) (Deployment, error) {
	in.Deployment.IdempotencyKey = strings.TrimSpace(in.Deployment.IdempotencyKey)
	if err := s.validateDeploymentForOrg(org, worker, in.Deployment); err != nil {
		return Deployment{}, err
	}
	if err := validateManagementMetadata(in.Metadata); err != nil {
		return Deployment{}, err
	}
	if err := validateManagedBindingIntent(in); err != nil {
		return Deployment{}, err
	}
	if in.ExpectedRevision < 0 {
		return Deployment{}, ErrManagementConflict
	}
	if len(in.Secrets)+len(in.KeepSecrets) > 128 {
		return Deployment{}, errors.New("secret binding limit exceeded")
	}
	if len(in.Secrets)+len(in.KeepSecrets) > 0 && s.secrets == nil {
		return Deployment{}, errors.New("NANOFLARE_SECRET_KEY is required for secret operations")
	}
	secretBytes := 0
	for name, value := range in.Secrets {
		if strings.TrimSpace(name) != name || name == "" || len(name) > 128 || !utf8.ValidString(name) {
			return Deployment{}, errors.New("invalid secret name")
		}
		if !utf8.ValidString(value) || len(value) > 1<<20 {
			return Deployment{}, errors.New("secret values must be UTF-8 and at most 1 MiB")
		}
		secretBytes += len(value)
		if secretBytes > 10<<20 {
			return Deployment{}, errors.New("secret snapshot exceeds 10 MiB")
		}
	}
	for name, source := range in.KeepSecrets {
		if strings.TrimSpace(name) != name || name == "" || source == "" || len(name) > 128 || !utf8.ValidString(name) || !utf8.ValidString(source) {
			return Deployment{}, errors.New("invalid inherited secret name")
		}
		if _, ok := in.Secrets[name]; ok {
			return Deployment{}, errors.New("duplicate secret intent")
		}
	}
	// A keyed fingerprint keeps low-entropy secret values out of public and
	// persisted request metadata, while preserving stable idempotency on retry.
	payload, err := canonicalPayload(struct {
		AssetUploadToken  string
		ConfigurationOnly bool
		Deployment        DeployInput
		ExpectedRevision  int64
		Base              string
		Metadata          ScriptManagementMetadata
		Secrets           map[string]string
		Keep              map[string]string
	}{in.AssetUploadToken, in.ConfigurationOnly, in.Deployment, in.ExpectedRevision, in.BaseDeploymentID, in.Metadata, in.Secrets, in.KeepSecrets})
	if err != nil {
		return Deployment{}, err
	}
	fingerprint := hashBytes(payload)
	if s.secrets != nil {
		mac := hmac.New(sha256.New, s.secrets.key)
		_, _ = mac.Write([]byte("nanoflare-managed-publication-v1\x00"))
		_, _ = mac.Write(payload)
		fingerprint = hex.EncodeToString(mac.Sum(nil))
	}
	return s.deployManagedPublication(org, worker, in, fingerprint)
}

func (s *Service) deployManagedPublication(org, worker string, in ManagedDeployInput, fingerprint string) (Deployment, error) {
	in.Deployment.IdempotencyKey = strings.TrimSpace(in.Deployment.IdempotencyKey)
	if in.Deployment.IdempotencyKey == "" || len(in.Deployment.IdempotencyKey) > 256 {
		return Deployment{}, errors.New("managed publications require an idempotency_key of at most 256 bytes")
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	release, err := s.store.LockDeploymentUpdates()
	if err != nil {
		return Deployment{}, err
	}
	defer release()
	if _, err := s.appForOrg(org, worker); err != nil {
		return Deployment{}, err
	}
	prior, found, err := s.store.GetPublication(worker, in.Deployment.IdempotencyKey)
	if err != nil {
		return Deployment{}, err
	}
	if found {
		if in.RequestIdentity != "" {
			if prior.RequestIdentity != in.RequestIdentity {
				return Deployment{}, ErrIdempotencyConflict
			}
			return s.replayPublication(prior)
		}
		if prior.Fingerprint != fingerprint {
			return Deployment{}, ErrIdempotencyConflict
		}
		return s.replayPublication(prior)
	}
	state, found, err := s.store.GetScriptManagementState(worker)
	if err != nil {
		return Deployment{}, err
	}
	if state.Revision != in.ExpectedRevision || (found && state.DeploymentID != in.BaseDeploymentID) {
		return Deployment{}, ErrManagementConflict
	}
	if in.AssetUploadToken != "" {
		if err := s.bindCompletedAssetUpload(org, worker, in.AssetUploadToken, in.Deployment.Assets); err != nil {
			return Deployment{}, err
		}
	}
	selected := []ArtifactSecret{}
	byName := map[string]ArtifactSecret{}
	if in.BaseDeploymentID != "" {
		base, err := s.store.GetDeploymentArtifact(worker, in.BaseDeploymentID)
		if err != nil {
			return Deployment{}, err
		}
		for _, secret := range base.Secrets {
			if secret.CreatedAt.IsZero() {
				secret.CreatedAt = base.Deployment.CreatedAt
			}
			byName[secret.Name] = secret
		}
	}
	if len(in.KeepSecrets) > 0 {
		for name, source := range in.KeepSecrets {
			secret, ok := byName[source]
			if !ok {
				return Deployment{}, ErrSecretNotFound
			}
			secret.Name = name
			selected = append(selected, secret)
		}
	}
	for name, value := range in.Secrets {
		nonce, ciphertext, err := s.secrets.Encrypt(value)
		if err != nil {
			return Deployment{}, err
		}
		version := hashBytes(append(append([]byte{}, nonce...), ciphertext...))
		now := time.Now().UTC()
		created := byName[name].CreatedAt
		if created.IsZero() {
			created = now
		}
		selected = append(selected, ArtifactSecret{Name: name, Nonce: nonce, Ciphertext: ciphertext, Version: version, CreatedAt: created, UpdatedAt: now})
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })
	return s.buildAndPublishDeployment(worker, in.Deployment, fingerprint, &managedPublication{ExpectedRevision: in.ExpectedRevision, Metadata: in.Metadata, Secrets: selected, RequestIdentity: in.RequestIdentity, ConfigurationOnly: in.ConfigurationOnly})
}

func (s *Service) commitManagedArtifact(p Publication, a DeploymentArtifact) error {
	if a.Management == nil {
		return nil
	}
	app, err := s.store.GetApp(p.AppID)
	if err != nil {
		return err
	}
	state, found, err := s.store.GetScriptManagementState(p.AppID)
	if err != nil {
		return err
	}
	if found && state.DeploymentID == p.DeploymentID && state.Revision == a.Management.ExpectedRevision+1 {
		return nil
	}
	next := ScriptManagementState{WorkerID: p.AppID, OrgID: app.OrgID, Revision: a.Management.ExpectedRevision + 1, DeploymentID: p.DeploymentID, Metadata: a.Management.Metadata, UpdatedAt: time.Now().UTC()}
	return s.store.CommitScriptManagementState(next, a.Management.ExpectedRevision)
}

type ArtifactManagement struct {
	ExpectedRevision int64                    `json:"expected_revision"`
	Metadata         ScriptManagementMetadata `json:"metadata"`
}

// Metadata is a projection of the actual native snapshot, not an independent
// source of authority. Refuse misleading binding names/types or variable values.
func validateManagedBindingIntent(in ManagedDeployInput) error {
	actual := map[string]string{}
	for name := range in.Deployment.Vars {
		actual[name] = "var"
	}
	for _, binding := range in.Deployment.KVNamespaces {
		actual[binding.Binding] = "kv_namespace"
	}
	for _, binding := range in.Deployment.Databases {
		actual[binding.Binding] = "d1"
	}
	for _, binding := range in.Deployment.ObjectStorageBuckets {
		actual[binding.Binding] = "r2_bucket"
	}
	for _, binding := range in.Deployment.Services {
		actual[binding.Binding] = "service"
	}
	if len(in.Deployment.Assets) > 0 && in.Deployment.AssetConfig.Binding != "" {
		actual[in.Deployment.AssetConfig.Binding] = "assets"
	}
	for name := range in.Secrets {
		actual[name] = "secret_text"
	}
	for name := range in.KeepSecrets {
		actual[name] = "secret_text"
	}
	if len(in.Deployment.DispatchNamespaces) > 0 || len(actual) != len(in.Metadata.Bindings) {
		return errors.New("management metadata does not match native bindings")
	}
	for _, raw := range in.Metadata.Bindings {
		var binding struct {
			Name        string          `json:"name"`
			Type        string          `json:"type"`
			Text        *string         `json:"text"`
			JSON        json.RawMessage `json:"json"`
			NamespaceID string          `json:"namespace_id"`
			ID          string          `json:"id"`
		}
		if err := json.Unmarshal(raw, &binding); err != nil {
			return errors.New("invalid management binding")
		}
		kind := binding.Type
		if kind == "plain_text" || kind == "json" {
			kind = "var"
		}
		if actual[binding.Name] != kind {
			return errors.New("management metadata does not match native bindings")
		}
		if kind == "var" {
			value := binding.JSON
			if binding.Type == "plain_text" {
				if binding.Text == nil {
					return errors.New("plain_text requires text")
				}
				value, _ = json.Marshal(*binding.Text)
			}
			left, err := canonicalPayload(value)
			if err != nil {
				return errors.New("invalid management variable")
			}
			right, err := canonicalPayload(in.Deployment.Vars[binding.Name])
			if err != nil || !bytes.Equal(left, right) {
				return errors.New("management variable does not match native value")
			}
		}
		if kind == "kv_namespace" {
			for _, native := range in.Deployment.KVNamespaces {
				if native.Binding == binding.Name && native.ID != binding.NamespaceID {
					return errors.New("management KV identifier does not match native binding")
				}
			}
		}
		if kind == "d1" {
			for _, native := range in.Deployment.Databases {
				if native.Binding == binding.Name && native.DatabaseID != binding.ID {
					return errors.New("management database identifier does not match native binding")
				}
			}
		}
	}
	return nil
}
