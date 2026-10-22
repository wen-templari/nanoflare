package nanoflare

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var dispatchBindingNamePattern = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

const RuntimeDispatch = "dispatch"
const RuntimeServiceDispatch = "service-dispatch"
const invocationCapabilityPrefix = "nfi1."

var ErrDispatchDepth = errors.New("dispatch call depth limit exceeded")

var (
	ErrDispatchUnavailable = errors.New("private dynamic dispatch is disabled")
	ErrInvocationExpired   = errors.New("invocation expired or revoked")
	ErrDispatchOverloaded  = errors.New("dispatch admission limit reached")
	ErrSubrequestLimit     = errors.New("invocation subrequest limit exceeded")
)

type DispatchBinding struct {
	Binding     string `json:"binding"`
	NamespaceID string `json:"namespace_id"`
}

type dispatchRuntime struct {
	enabled atomic.Bool
	mu      sync.Mutex
	leases  map[string]*dispatchLease
	roots   map[string]*dispatchRoot
	observe func(DispatchObservation)
	counts  map[string]DispatchCounters
}
type dispatchLease struct {
	AdmittedAt     time.Time
	Observed       bool
	LogLines       int
	Context        context.Context
	Cancel         context.CancelFunc
	TransportToken string
	Parent         string
	Depth          int
	Root           string
	CallerToken    string
	Caller         RuntimeGrant
	Target         ActiveDeployment
	Deadline       time.Time
	Expires        time.Time
	Finished       bool
	Remaining      int
}

// DispatchArtifact crosses only the authenticated runner management transport.
// It deliberately has no management signing roots or resource bearer credentials.
type DispatchArtifact struct {
	Deployment Deployment     `json:"deployment"`
	Values     map[string]any `json:"values"`
	Digest     string         `json:"digest"`
	Lease      string         `json:"lease"`
	Deadline   time.Time      `json:"deadline"`
}

func (s *Service) SetPlatformDispatchEnabled(enabled bool) {
	s.dispatch.enabled.Store(enabled)
	if enabled {
		return
	}
	s.dispatch.mu.Lock()
	var callbacks []func()
	for _, root := range s.dispatch.roots {
		if root.Cancel != nil {
			callbacks = append(callbacks, root.Cancel)
		}
		if root.Release != nil {
			callbacks = append(callbacks, root.Release)
			root.Release = nil
		}
	}
	for _, lease := range s.dispatch.leases {
		if lease.Cancel != nil {
			callbacks = append(callbacks, lease.Cancel)
		}
	}
	s.dispatch.leases = nil
	s.dispatch.roots = nil
	s.dispatch.mu.Unlock()
	for _, callback := range callbacks {
		callback()
	}
}

func (a App) IsDynamic() bool { return a.IsPrivate() && a.Platform.Execution == "dynamic" }
func isRevisionCapability(token string) bool {
	return strings.HasPrefix(token, runtimeCapabilityPrefix) || strings.HasPrefix(token, invocationCapabilityPrefix)
}

func (s *Service) validateDispatchBindings(app App, in DeployInput) error {
	if !app.IsDynamic() && len(in.DispatchNamespaces) == 0 {
		return nil
	}
	if !s.dispatch.enabled.Load() {
		return ErrDispatchUnavailable
	}
	if len(in.DispatchNamespaces) > 0 {
		for _, file := range in.Files {
			if (file.Path == in.Entrypoint || (in.Entrypoint == "" && len(in.Files) == 1)) && regexp.MustCompile(`(?m)export\s+default\s+class\b`).MatchString(file.Content) {
				return errors.New("platform namespace bindings currently require a module fetch-handler object")
			}
		}
	}
	if in.Format != "modules" {
		return errors.New("dynamic dispatch requires modules format")
	}
	if app.IsDynamic() && (len(in.DispatchNamespaces) > 0 || len(in.Triggers.Crons) > 0 || in.DNS.Profile != "") {
		return errors.New("dynamic scripts do not yet support namespace-wide nested dispatch, schedules or DNS profiles")
	}
	if app.IsDynamic() {
		for _, binding := range in.Services {
			target, err := s.store.GetApp(binding.WorkerID)
			if err != nil {
				return err
			}
			if !target.IsDynamic() || !ServiceTargetAllowed(app, target) {
				return ErrNamespaceGrantDenied
			}
		}
	}
	names := map[string]bool{}
	for name := range in.Vars {
		names[strings.TrimSpace(name)] = true
	}
	records, err := s.store.ListSecrets(app.ID)
	if err != nil {
		return err
	}
	for _, r := range records {
		names[r.Name] = true
	}
	for _, b := range in.KVNamespaces {
		names[strings.TrimSpace(b.Binding)] = true
	}
	for _, b := range in.Databases {
		names[strings.TrimSpace(b.Binding)] = true
	}
	for _, b := range in.ObjectStorageBuckets {
		names[strings.TrimSpace(b.Binding)] = true
	}
	for _, b := range in.Services {
		names[strings.TrimSpace(b.Binding)] = true
	}
	names[assetBindingNameForDispatch(in.AssetConfig)] = true
	for _, b := range in.DispatchNamespaces {
		if !dispatchBindingNamePattern.MatchString(b.Binding) || strings.HasPrefix(b.Binding, "__NANOFLARE_") || names[b.Binding] {
			return errors.New("invalid or duplicate dispatch binding name")
		}
		names[strings.TrimSpace(b.Binding)] = true
		ns, err := s.DispatchNamespaceForOrg(app.OrgID, b.NamespaceID)
		if err != nil {
			return err
		}
		if ns.Trust != "trusted" {
			return ErrUnsupportedExecutionProfile
		}
	}
	return nil
}
func assetBindingNameForDispatch(c AssetConfig) string {
	if strings.TrimSpace(c.Binding) != "" {
		return strings.TrimSpace(c.Binding)
	}
	return "ASSETS"
}

func (s *Service) AcquireDispatch(ctx context.Context, callerToken, namespace, script string) (DispatchArtifact, error) {
	return s.AcquireDispatchWithRoot(ctx, callerToken, namespace, script, "")
}
func (s *Service) AcquireDispatchWithRoot(ctx context.Context, callerToken, namespace, script, rootID string) (DispatchArtifact, error) {
	if !s.dispatch.enabled.Load() {
		return DispatchArtifact{}, ErrDispatchUnavailable
	}
	caller, err := s.AuthorizeRuntime(ctx, callerToken, RuntimeDispatch, namespace)
	if err != nil {
		return DispatchArtifact{}, err
	}
	app, err := s.NamespaceScript(caller.App.OrgID, namespace, script)
	if err != nil {
		return DispatchArtifact{}, err
	}
	return s.acquireResolvedDispatch(ctx, callerToken, callerToken, caller, app, rootID, "", 0)
}
func (s *Service) acquireResolvedDispatch(ctx context.Context, callerToken, transportToken string, caller RuntimePrincipal, app App, rootID, parentID string, depth int) (DispatchArtifact, error) {
	namespace := app.Platform.NamespaceID
	if !app.IsDynamic() {
		return DispatchArtifact{}, errors.New("dispatch target must use dynamic execution")
	}
	active, err := s.store.ActiveDeploymentsForApp(app.ID)
	if err != nil {
		return DispatchArtifact{}, err
	}
	selected := selectWeightedDeploymentWithPreference(active, "")
	if selected == nil {
		return DispatchArtifact{}, ErrArtifactNotFound
	}
	if err = s.hydrateArtifact(selected); err != nil {
		return DispatchArtifact{}, err
	}
	if err = s.checkNamespaceBindings(app, DeployInput{KVNamespaces: selected.Deployment.KVNamespaces, Databases: selected.Deployment.Databases, ObjectStorageBuckets: selected.Deployment.ObjectStorageBuckets}); err != nil {
		return DispatchArtifact{}, err
	}
	artifact, err := s.store.GetDeploymentArtifact(app.ID, selected.Deployment.ID)
	if err != nil {
		return DispatchArtifact{}, err
	}
	values := map[string]any{}
	for name, raw := range selected.Deployment.Vars {
		var value any
		if err = json.Unmarshal(raw, &value); err != nil {
			return DispatchArtifact{}, err
		}
		values[name] = value
	}
	for name, value := range selected.App.SecretValues {
		values[name] = value
	}
	token, err := randomToken()
	if err != nil {
		return DispatchArtifact{}, err
	}
	token = invocationCapabilityPrefix + token
	now := time.Now().UTC()
	deadline := now.Add(30 * time.Second)
	s.dispatch.mu.Lock()
	defer s.dispatch.mu.Unlock()
	if parentID != "" {
		parent := s.dispatch.leases[parentID]
		if parent == nil || !now.Before(parent.Expires) || parent.TransportToken != transportToken || parent.Root != rootID || depth > 8 {
			return DispatchArtifact{}, ErrInvocationExpired
		}
		deadline = minTime(deadline, parent.Expires)
	}
	if rootID != "" {
		root := s.dispatch.roots[rootID]
		if root == nil || root.Cancelled || !now.Before(root.Expires) || (depth == 0 && (root.AppID != caller.App.ID || root.DeploymentID != caller.Deployment.ID)) {
			return DispatchArtifact{}, ErrInvocationExpired
		}
		if depth == 0 && root.InstanceID != "" && root.InstanceID != caller.Grant.InstanceID {
			return DispatchArtifact{}, ErrInvocationExpired
		}
		if depth == 0 {
			root.InstanceID = caller.Grant.InstanceID
		}
		if root.Remaining < 1 {
			return DispatchArtifact{}, ErrSubrequestLimit
		}
		root.Remaining--
		deadline = minTime(deadline, root.Expires)
	}
	if s.dispatch.leases == nil {
		s.dispatch.leases = map[string]*dispatchLease{}
	}
	nsCount, customerCount := 0, 0
	for key, lease := range s.dispatch.leases {
		if !now.Before(lease.Expires) {
			if lease.Cancel != nil {
				lease.Cancel()
			}
			delete(s.dispatch.leases, key)
			continue
		}
		if lease.Target.App.Platform.NamespaceID == namespace {
			nsCount++
			if lease.Target.App.Platform.CustomerID == app.Platform.CustomerID {
				customerCount++
			}
		}
	}
	if len(s.dispatch.leases) >= 1024 || nsCount >= 128 || customerCount >= 32 {
		return DispatchArtifact{}, ErrDispatchOverloaded
	}
	parentContext := context.Background()
	if root := s.dispatch.roots[rootID]; root != nil && root.Context != nil {
		parentContext = root.Context
	}
	if parent := s.dispatch.leases[parentID]; parent != nil && parent.Context != nil {
		parentContext = parent.Context
	}
	leaseContext, leaseCancel := context.WithDeadline(parentContext, deadline)
	s.dispatch.leases[token] = &dispatchLease{AdmittedAt: now, Context: leaseContext, Cancel: leaseCancel, CallerToken: callerToken, Caller: caller.Grant, Target: *selected, Deadline: deadline, Expires: deadline, Remaining: 100, Root: rootID, TransportToken: transportToken, Parent: parentID, Depth: depth}
	s.admitDispatchObservationLocked(app.ID)
	return DispatchArtifact{Deployment: artifact.Deployment, Values: values, Digest: artifact.Digest, Lease: token, Deadline: deadline}, nil
}
func (s *Service) authorizeInvocation(ctx context.Context, token string) (RuntimePrincipal, error) {
	if !s.dispatch.enabled.Load() {
		return RuntimePrincipal{}, ErrDispatchUnavailable
	}
	s.dispatch.mu.Lock()
	lease, ok := s.dispatch.leases[token]
	if !ok || !time.Now().Before(lease.Expires) {
		if ok {
			if lease.Cancel != nil {
				lease.Cancel()
			}
			delete(s.dispatch.leases, token)
		}
		s.dispatch.mu.Unlock()
		return RuntimePrincipal{}, ErrInvocationExpired
	}
	if lease.Root != "" {
		root := s.dispatch.roots[lease.Root]
		if root == nil || root.Cancelled || !time.Now().Before(root.Expires) {
			s.dispatch.mu.Unlock()
			return RuntimePrincipal{}, ErrInvocationExpired
		}
	}
	target, callerToken, callerGrant := lease.Target, lease.CallerToken, lease.Caller
	s.dispatch.mu.Unlock()
	if _, err := s.AuthorizeRuntime(ctx, callerToken, callerGrant.Kind, callerGrant.ResourceID); err != nil {
		return RuntimePrincipal{}, err
	}
	app, err := s.store.GetApp(target.App.ID)
	if err != nil {
		return RuntimePrincipal{}, err
	}
	return RuntimePrincipal{App: app, Deployment: target.Deployment, Grant: RuntimeGrant{OrgID: app.OrgID, AppID: app.ID, DeploymentID: target.Deployment.ID}}, nil
}
func (s *Service) DispatchEffect(ctx context.Context, callerToken, leaseToken, kind, resource string, cost int) (RuntimePrincipal, error) {
	p, err := s.AuthorizeRuntime(ctx, leaseToken, kind, resource)
	if err != nil {
		return p, err
	}
	s.dispatch.mu.Lock()
	defer s.dispatch.mu.Unlock()
	lease := s.dispatch.leases[leaseToken]
	if lease == nil || lease.TransportToken != callerToken || !time.Now().Before(lease.Expires) {
		return RuntimePrincipal{}, ErrInvocationExpired
	}
	if lease.Root != "" {
		root := s.dispatch.roots[lease.Root]
		if root == nil || root.Cancelled || !time.Now().Before(root.Expires) {
			return RuntimePrincipal{}, ErrInvocationExpired
		}
		if cost < 1 || cost > root.Remaining {
			return RuntimePrincipal{}, ErrSubrequestLimit
		}
		root.Remaining -= cost
	}
	if cost < 1 || cost > lease.Remaining {
		return RuntimePrincipal{}, ErrSubrequestLimit
	}
	lease.Remaining -= cost
	return p, nil
}
func (s *Service) FinishDispatch(ctx context.Context, callerToken, leaseToken string, cancel bool) error {
	if _, err := s.authorizeInvocation(ctx, leaseToken); err != nil {
		return err
	}
	s.dispatch.mu.Lock()
	var observations []DispatchObservation
	defer func() {
		sink := s.dispatch.observe
		s.dispatch.mu.Unlock()
		if sink != nil {
			for _, event := range observations {
				sink(event)
			}
		}
	}()
	lease := s.dispatch.leases[leaseToken]
	if lease == nil || lease.TransportToken != callerToken {
		return ErrInvocationExpired
	}
	if cancel {
		pending := []string{leaseToken}
		for len(pending) > 0 {
			id := pending[0]
			pending = pending[1:]
			if cancelled := s.dispatch.leases[id]; cancelled != nil && cancelled.Cancel != nil {
				if event, ok := s.observeDispatchLocked(id, cancelled, "canceled"); ok {
					observations = append(observations, event)
				}
				cancelled.Cancel()
			}
			delete(s.dispatch.leases, id)
			for childID, child := range s.dispatch.leases {
				if child.Parent == id {
					pending = append(pending, childID)
				}
			}
		}
		return nil
	}
	if !lease.Finished {
		if event, ok := s.observeDispatchLocked(leaseToken, lease, "completed"); ok {
			observations = append(observations, event)
		}
		lease.Finished = true
		lease.Expires = minTime(lease.Expires, time.Now().Add(2*time.Second))
		if lease.Cancel != nil {
			time.AfterFunc(max(time.Until(lease.Expires), 0), lease.Cancel)
		}
		pending := []string{leaseToken}
		for len(pending) > 0 {
			id := pending[0]
			pending = pending[1:]
			for childID, child := range s.dispatch.leases {
				if child.Parent == id {
					child.Expires = minTime(child.Expires, lease.Expires)
					pending = append(pending, childID)
				}
			}
		}
	}
	return nil
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (s *Service) DispatchResource(ctx context.Context, token, kind, binding string) (string, error) {
	p, err := s.authorizeInvocation(ctx, token)
	if err != nil {
		return "", err
	}
	switch kind {
	case RuntimeKV:
		for _, b := range p.Deployment.KVNamespaces {
			if b.Binding == binding {
				return b.ID, nil
			}
		}
	case RuntimeDatabase:
		for _, b := range p.Deployment.Databases {
			if b.Binding == binding {
				return b.DatabaseID, nil
			}
		}
	case RuntimeObjects:
		for _, b := range p.Deployment.ObjectStorageBuckets {
			if b.Binding == binding {
				return b.BucketID, nil
			}
		}
	case RuntimeServiceDispatch:
		for _, b := range p.Deployment.Services {
			if b.Binding == binding && b.Execution == "dynamic" {
				return b.WorkerID, nil
			}
		}
	case RuntimeAssets:
		if assetBindingNameForDispatch(p.Deployment.AssetConfig) == binding {
			return p.Deployment.ID, nil
		}
	}
	return "", ErrRuntimeGrantDenied
}

func validateDispatchNameCollisions(bindings []DispatchBinding, vars map[string]json.RawMessage, secrets map[string]string) error {
	for _, binding := range bindings {
		if _, ok := vars[binding.Binding]; ok {
			return errors.New("dispatch binding conflicts with a variable")
		}
		if _, ok := secrets[binding.Binding]; ok {
			return errors.New("dispatch binding conflicts with a secret")
		}
	}
	return nil
}

func (s *Service) AcquireServiceDispatch(ctx context.Context, transportToken, parentToken, binding string) (DispatchArtifact, error) {
	targetID, err := s.DispatchResource(ctx, parentToken, RuntimeServiceDispatch, binding)
	if err != nil {
		return DispatchArtifact{}, err
	}
	caller, err := s.AuthorizeRuntime(ctx, parentToken, RuntimeServiceDispatch, targetID)
	if err != nil {
		return DispatchArtifact{}, err
	}
	app, err := s.store.GetApp(targetID)
	if err != nil {
		return DispatchArtifact{}, err
	}
	s.dispatch.mu.Lock()
	parent := s.dispatch.leases[parentToken]
	if parent != nil && parent.Depth >= 8 {
		s.dispatch.mu.Unlock()
		return DispatchArtifact{}, ErrDispatchDepth
	}
	if parent == nil || parent.TransportToken != transportToken || parent.Root == "" {
		s.dispatch.mu.Unlock()
		return DispatchArtifact{}, ErrRuntimeGrantDenied
	}
	root, depth := parent.Root, parent.Depth+1
	s.dispatch.mu.Unlock()
	return s.acquireResolvedDispatch(ctx, parentToken, transportToken, caller, app, root, parentToken, depth)
}

// DispatchEffectContext interrupts admitted resource work at the invocation's
// deadline or cancellation, independently of native transport cancellation.
func (s *Service) DispatchEffectContext(ctx context.Context, token string) (context.Context, context.CancelFunc, error) {
	s.dispatch.mu.Lock()
	defer s.dispatch.mu.Unlock()
	lease := s.dispatch.leases[token]
	if !s.dispatch.enabled.Load() || lease == nil || !time.Now().Before(lease.Expires) {
		return nil, nil, ErrInvocationExpired
	}
	effectContext, cancel := context.WithDeadline(ctx, lease.Expires)
	stop := func() bool { return true }
	if lease.Context != nil {
		stop = context.AfterFunc(lease.Context, cancel)
	}
	return effectContext, func() { stop(); cancel() }, nil
}

func (s *Service) AuthorizeDispatchTransport(ctx context.Context, transport, token string) error {
	if _, err := s.authorizeInvocation(ctx, token); err != nil {
		return err
	}
	s.dispatch.mu.Lock()
	defer s.dispatch.mu.Unlock()
	lease := s.dispatch.leases[token]
	if lease == nil || lease.TransportToken != transport {
		return ErrRuntimeGrantDenied
	}
	return nil
}
