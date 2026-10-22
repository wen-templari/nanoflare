package nanoflare

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	ErrDispatchNamespaceNotFound   = errors.New("dispatch namespace not found")
	ErrDispatchNamespaceExists     = errors.New("dispatch namespace or script name already exists")
	ErrDispatchNamespaceInUse      = errors.New("dispatch namespace still has scripts or resource grants")
	ErrNamespacePolicyConflict     = errors.New("namespace policy revision changed")
	ErrNamespaceGrantDenied        = errors.New("namespace resource grant denied")
	ErrUnsupportedExecutionProfile = errors.New("only trusted execution is currently available")
	ErrAmbiguousService            = errors.New("ambiguous legacy service name; use an immutable Worker ID")
)

type WorkerPlatform struct {
	Execution   string `json:"execution,omitempty"`
	NamespaceID string `json:"namespace_id"`
	ScriptName  string `json:"script_name"`
	Kind        string `json:"kind"`
	Exposure    string `json:"exposure"`
	CustomerID  string `json:"customer_id,omitempty"`
}

func (a App) IsPrivate() bool { return a.Platform != nil && a.Platform.Exposure == "private" }

type NamespaceResourceGrant struct {
	Kind       string `json:"kind"`
	ResourceID string `json:"resource_id"`
	CustomerID string `json:"customer_id,omitempty"`
}
type DispatchNamespace struct {
	ID             string                   `json:"id"`
	OrgID          string                   `json:"org_id"`
	Name           string                   `json:"name"`
	Trust          string                   `json:"trust"`
	CustomerMode   string                   `json:"customer_mode"`
	PolicyRevision int64                    `json:"policy_revision"`
	Grants         []NamespaceResourceGrant `json:"grants"`
	CreatedAt      time.Time                `json:"created_at"`
}
type NamespacePolicy struct {
	NamespaceID string                   `json:"namespace_id"`
	Revision    int64                    `json:"revision"`
	Grants      []NamespaceResourceGrant `json:"grants"`
	CreatedAt   time.Time                `json:"created_at"`
}
type CreateDispatchNamespaceInput struct {
	Name         string `json:"name"`
	Trust        string `json:"trust,omitempty"`
	CustomerMode string `json:"customer_mode,omitempty"`
}
type RenameDispatchNamespaceInput struct {
	Name string `json:"name"`
}
type NamespaceGrantsInput struct {
	ExpectedRevision int64                    `json:"expected_revision"`
	Grants           []NamespaceResourceGrant `json:"grants"`
}
type CreateNamespaceScriptInput struct {
	Execution  string `json:"execution,omitempty"`
	Name       string `json:"name,omitempty"`
	ScriptName string `json:"script_name"`
	Kind       string `json:"kind,omitempty"`
	CustomerID string `json:"customer_id,omitempty"`
}

var platformName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

func (s *Service) CreateDispatchNamespace(org string, in CreateDispatchNamespaceInput) (DispatchNamespace, error) {
	if _, err := s.store.GetOrganization(org); err != nil {
		return DispatchNamespace{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if !platformName.MatchString(in.Name) {
		return DispatchNamespace{}, errors.New("namespace name must be 1-63 lowercase letters, digits, underscores or hyphens")
	}
	if in.Trust == "" {
		in.Trust = "trusted"
	}
	if in.Trust != "trusted" {
		return DispatchNamespace{}, ErrUnsupportedExecutionProfile
	}
	if in.CustomerMode == "" {
		in.CustomerMode = "organization"
	}
	if in.CustomerMode != "organization" && in.CustomerMode != "platform" {
		return DispatchNamespace{}, errors.New("customer_mode must be organization or platform")
	}
	id, err := randomToken()
	if err != nil {
		return DispatchNamespace{}, err
	}
	ns := DispatchNamespace{ID: id, OrgID: org, Name: in.Name, Trust: in.Trust, CustomerMode: in.CustomerMode, PolicyRevision: 1, Grants: []NamespaceResourceGrant{}, CreatedAt: time.Now().UTC()}
	return ns, s.store.CreateDispatchNamespace(ns)
}
func (s *Service) DispatchNamespaceForOrg(org, id string) (DispatchNamespace, error) {
	ns, err := s.store.GetDispatchNamespace(id)
	if err == nil && ns.OrgID != org {
		err = ErrDispatchNamespaceNotFound
	}
	return ns, err
}
func (s *Service) DispatchNamespacesForOrg(org string) ([]DispatchNamespace, error) {
	return s.store.ListDispatchNamespaces(org)
}
func (s *Service) RenameDispatchNamespace(org, id, name string) (DispatchNamespace, error) {
	ns, err := s.DispatchNamespaceForOrg(org, id)
	if err != nil {
		return ns, err
	}
	if !platformName.MatchString(name) {
		return ns, errors.New("invalid namespace name")
	}
	if err := s.store.RenameDispatchNamespace(id, name); err != nil {
		return ns, err
	}
	return s.store.GetDispatchNamespace(id)
}
func (s *Service) DeleteDispatchNamespace(org, id string) error {
	if _, err := s.DispatchNamespaceForOrg(org, id); err != nil {
		return err
	}
	return s.store.DeleteDispatchNamespace(id)
}
func (s *Service) NamespacePoliciesForOrg(org, id string) ([]NamespacePolicy, error) {
	if _, err := s.DispatchNamespaceForOrg(org, id); err != nil {
		return nil, err
	}
	return s.store.NamespacePolicies(id)
}
func (s *Service) SetNamespaceGrants(org, id string, input NamespaceGrantsInput) (DispatchNamespace, error) {
	ns, err := s.DispatchNamespaceForOrg(org, id)
	if err != nil {
		return ns, err
	}
	seen := map[string]bool{}
	for _, g := range input.Grants {
		if (ns.CustomerMode == "organization" && g.CustomerID != "") || (g.CustomerID != "" && !platformName.MatchString(g.CustomerID)) {
			return ns, errors.New("invalid grant customer_id")
		}
		if err := s.resourceOwnedByOrg(org, g.Kind, g.ResourceID); err != nil {
			return ns, err
		}
		key := g.Kind + "\x00" + g.ResourceID + "\x00" + g.CustomerID
		if seen[key] {
			return ns, errors.New("duplicate resource grant")
		}
		seen[key] = true
	}
	if input.Grants == nil {
		input.Grants = []NamespaceResourceGrant{}
	}
	return s.store.SetNamespaceGrants(id, input.ExpectedRevision, input.Grants)
}
func (s *Service) resourceOwnedByOrg(org, kind, id string) error {
	switch kind {
	case RuntimeKV:
		r, e := s.store.GetKVNamespace(id)
		if e != nil {
			return e
		}
		if r.OrgID == org {
			return nil
		}
	case RuntimeDatabase:
		r, e := s.store.GetDatabase(id)
		if e != nil {
			return e
		}
		if r.OrgID == org {
			return nil
		}
	case RuntimeObjects:
		r, e := s.store.GetObjectStorageBucket(id)
		if e != nil {
			return e
		}
		if r.OrgID == org {
			return nil
		}
	default:
		return errors.New("resource kind must be kv, db or objects")
	}
	return ErrNamespaceGrantDenied
}
func (s *Service) CreateNamespaceScript(org, id string, in CreateNamespaceScriptInput) (App, error) {
	ns, err := s.DispatchNamespaceForOrg(org, id)
	if err != nil {
		return App{}, err
	}
	if !platformName.MatchString(in.ScriptName) {
		return App{}, errors.New("invalid script_name")
	}
	if in.Kind == "" {
		in.Kind = "customer"
	}
	if in.Kind != "platform" && in.Kind != "customer" {
		return App{}, errors.New("kind must be platform or customer")
	}
	if in.CustomerID != "" && !platformName.MatchString(in.CustomerID) {
		return App{}, errors.New("invalid customer_id")
	}
	if ns.CustomerMode == "organization" && in.CustomerID != "" {
		return App{}, errors.New("organization namespaces do not accept customer_id")
	}
	if ns.CustomerMode == "platform" && in.Kind == "customer" && in.CustomerID == "" {
		return App{}, errors.New("customer_id is required for platform-hosted customer scripts")
	}
	if in.Kind == "platform" && in.CustomerID != "" {
		return App{}, errors.New("platform scripts cannot claim a customer_id")
	}
	if in.Execution != "" && in.Execution != "static" && in.Execution != "dynamic" {
		return App{}, errors.New("execution must be static or dynamic")
	}
	if in.Execution == "dynamic" && (!s.dispatch.enabled.Load() || in.Kind != "customer") {
		return App{}, ErrDispatchUnavailable
	}
	if err := s.enforceOrgLimit(org, "worker"); err != nil {
		return App{}, err
	}
	workerID, err := randomToken()
	if err != nil {
		return App{}, err
	}
	token, err := randomToken()
	if err != nil {
		return App{}, err
	}
	if in.Name == "" {
		in.Name = in.ScriptName
	}
	app := App{ID: workerID, OrgID: org, Name: in.Name, RuntimeToken: token, CreatedAt: time.Now().UTC(), Platform: &WorkerPlatform{NamespaceID: id, ScriptName: in.ScriptName, Kind: in.Kind, Exposure: "private", CustomerID: in.CustomerID, Execution: in.Execution}}
	return app, s.store.CreateApp(app)
}
func (s *Service) NamespaceScripts(org, id string) ([]App, error) {
	if _, err := s.DispatchNamespaceForOrg(org, id); err != nil {
		return nil, err
	}
	apps, err := s.store.ListAppsByOrg(org)
	if err != nil {
		return nil, err
	}
	out := []App{}
	for _, a := range apps {
		if a.Platform != nil && a.Platform.NamespaceID == id {
			out = append(out, a)
		}
	}
	return out, nil
}
func (s *Service) NamespaceScript(org, id, name string) (App, error) {
	apps, err := s.NamespaceScripts(org, id)
	if err != nil {
		return App{}, err
	}
	for _, a := range apps {
		if a.Platform.ScriptName == name {
			return a, nil
		}
	}
	return App{}, ErrAppNotFound
}
func (s *Service) namespaceResourceAllowed(app App, kind, resource string) error {
	if app.Platform == nil {
		return nil
	}
	ns, err := s.store.GetDispatchNamespace(app.Platform.NamespaceID)
	if err != nil {
		return err
	}
	if ns.OrgID != app.OrgID || ns.Trust != "trusted" {
		return ErrNamespaceGrantDenied
	}
	for _, g := range ns.Grants {
		if g.Kind == kind && g.ResourceID == resource && g.CustomerID == app.Platform.CustomerID {
			return nil
		}
	}
	return ErrNamespaceGrantDenied
}
func (s *Service) checkNamespaceBindings(app App, input DeployInput) error {
	if app.Platform == nil {
		return nil
	}
	for _, b := range input.KVNamespaces {
		if e := s.namespaceResourceAllowed(app, RuntimeKV, b.ID); e != nil {
			return e
		}
	}
	for _, b := range input.Databases {
		if e := s.namespaceResourceAllowed(app, RuntimeDatabase, b.DatabaseID); e != nil {
			return e
		}
	}
	for _, b := range input.ObjectStorageBuckets {
		if e := s.namespaceResourceAllowed(app, RuntimeObjects, b.BucketID); e != nil {
			return e
		}
	}
	return nil
}
func ValidatePlatformMembership(app App, ns DispatchNamespace) error {
	p := app.Platform
	if p == nil {
		return nil
	}
	if ns.ID != p.NamespaceID || ns.OrgID != app.OrgID {
		return ErrDispatchNamespaceNotFound
	}
	if (ns.CustomerMode == "organization" && p.CustomerID != "") || (ns.CustomerMode == "platform" && p.Kind == "customer" && p.CustomerID == "") || (p.Kind == "platform" && p.CustomerID != "") {
		return errors.New("invalid customer membership")
	}
	if ns.Trust != "trusted" || p.Exposure != "private" || app.Hostname != "" || !platformName.MatchString(p.ScriptName) || (p.Kind != "platform" && p.Kind != "customer") {
		return fmt.Errorf("invalid immutable platform membership")
	}
	return nil
}

// ServiceTargetAllowed preserves legacy org boundaries and confines private calls
// to one namespace/customer. Namespace-wide dynamic dispatch is a separate grant.
func ServiceTargetAllowed(source, target App) bool {
	if source.OrgID != target.OrgID {
		return false
	}
	if source.Platform == nil && target.Platform == nil {
		return true
	}
	return source.Platform != nil && target.Platform != nil && source.Platform.NamespaceID == target.Platform.NamespaceID && source.Platform.CustomerID == target.Platform.CustomerID
}

func clonePlatformApp(app App) App {
	if app.Platform != nil {
		value := *app.Platform
		app.Platform = &value
	}
	return app
}
