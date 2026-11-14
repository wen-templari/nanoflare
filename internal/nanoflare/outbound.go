package nanoflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

var ErrInvalidDispatchOptions = errors.New("invalid dispatch options")

func dispatchArgumentError(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidDispatchOptions, message)
}

// DispatchOutbound is deployment configuration, never customer-supplied authority.
type DispatchOutbound struct {
	WorkerID   string   `json:"worker_id"`
	Entrypoint string   `json:"entrypoint,omitempty"`
	Parameters []string `json:"parameters,omitempty"`
}

func CloneDispatchBindings(bindings []DispatchBinding) []DispatchBinding {
	result := slices.Clone(bindings)
	for i := range result {
		if result[i].Outbound != nil {
			copy := *result[i].Outbound
			copy.Parameters = slices.Clone(copy.Parameters)
			result[i].Outbound = &copy
		}
	}
	return result
}

type dispatchOutboundSnapshot struct {
	active     ActiveDeployment
	parameters map[string]json.RawMessage
	names      []string
	entrypoint string
}
type dispatchAdmission struct {
	outbound              *dispatchOutboundSnapshot
	outboundExecution     bool
	entrypoint            string
	selected              *ActiveDeployment
	parameters            map[string]json.RawMessage
	parameterNames        []string
	namespace, customerID string
}

func (lease *dispatchLease) admissionIdentity() (string, string) {
	return lease.Namespace, lease.CustomerID
}

func (s *Service) validateDispatchOutbound(host App, config *DispatchOutbound) error {
	if config == nil {
		return nil
	}
	if config.WorkerID == host.ID {
		return errors.New("outbound Worker cannot reference itself")
	}
	target, err := s.appForOrg(host.OrgID, config.WorkerID)
	if err != nil {
		return ErrRuntimeGrantDenied
	}
	if target.Platform != nil {
		return errors.New("outbound target must be an ordinary Worker")
	}
	active, err := s.store.ActiveDeploymentsForApp(target.ID)
	if err != nil {
		return err
	}
	if len(active) == 0 {
		return ErrArtifactNotFound
	}
	for _, item := range active {
		if err := s.validateOutboundRevision(config, item.Deployment); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) validateOutboundRevision(config *DispatchOutbound, d Deployment) error {
	// These features require the ordinary execution bridge, rather than the
	// bounded fetch/resource adapter used for the initial outbound profile.
	if d.Format != "modules" {
		return errors.New("outbound Worker requires modules")
	}
	if len(config.Parameters) > 64 {
		return errors.New("outbound Worker supports at most 64 parameter names")
	}
	names := map[string]bool{}
	for name := range d.Vars {
		names[name] = true
	}
	secrets, err := s.store.ListSecrets(d.AppID)
	if err != nil {
		return err
	}
	for _, secret := range secrets {
		names[secret.Name] = true
	}
	for _, binding := range d.Services {
		names[binding.Binding] = true
	}
	for _, binding := range d.DispatchNamespaces {
		names[binding.Binding] = true
	}
	for _, binding := range d.KVNamespaces {
		names[binding.Binding] = true
	}
	for _, binding := range d.Databases {
		names[binding.Binding] = true
	}
	for _, binding := range d.ObjectStorageBuckets {
		names[binding.Binding] = true
	}
	names[assetBindingNameForDispatch(d.AssetConfig)] = true
	for _, name := range config.Parameters {
		if !dispatchBindingNamePattern.MatchString(name) || strings.HasPrefix(name, "__NANOFLARE_") || name == "__proto__" || name == "prototype" || name == "constructor" || names[name] {
			return errors.New("invalid, duplicate or conflicting outbound parameter name")
		}
		names[name] = true
	}
	return nil
}

func (s *Service) AcquireDispatchBinding(ctx context.Context, token, namespace, script, root, binding string, parameters map[string]json.RawMessage) (DispatchArtifact, error) {
	caller, err := s.AuthorizeRuntime(ctx, token, RuntimeDispatch, namespace)
	if err != nil {
		return DispatchArtifact{}, err
	}
	app, err := s.NamespaceScript(caller.App.OrgID, namespace, script)
	if err != nil {
		return DispatchArtifact{}, err
	}
	return s.acquireDispatchBinding(ctx, token, caller, app, namespace, root, binding, parameters)
}

func (s *Service) acquireDispatchBinding(ctx context.Context, token string, caller RuntimePrincipal, app App, namespace, root, name string, parameters map[string]json.RawMessage) (DispatchArtifact, error) {
	var binding *DispatchBinding
	for i := range caller.Deployment.DispatchNamespaces {
		b := &caller.Deployment.DispatchNamespaces[i]
		if b.NamespaceID == namespace && (name == "" || b.Binding == name) {
			if binding != nil {
				return DispatchArtifact{}, dispatchArgumentError("dispatch binding name is required for an ambiguous namespace")
			}
			binding = b
		}
	}
	if binding == nil {
		return DispatchArtifact{}, ErrRuntimeGrantDenied
	}
	var policy *dispatchOutboundSnapshot
	if binding.Outbound != nil {
		if err := s.validateDispatchOutbound(caller.App, binding.Outbound); err != nil {
			return DispatchArtifact{}, err
		}
		encoded, err := json.Marshal(parameters)
		if err != nil || len(encoded) > 16*1024 {
			return DispatchArtifact{}, dispatchArgumentError("outbound parameters exceed 16 KiB")
		}
		for name, value := range parameters {
			if !slices.Contains(binding.Outbound.Parameters, name) || !json.Valid(value) {
				return DispatchArtifact{}, dispatchArgumentError("undeclared or invalid outbound parameter")
			}
		}
		active, err := s.store.ActiveDeploymentsForApp(binding.Outbound.WorkerID)
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
		// Hydrated historical secret versions participate in collision checks too.
		for _, name := range binding.Outbound.Parameters {
			if _, ok := selected.App.SecretValues[name]; ok {
				return DispatchArtifact{}, errors.New("outbound parameter conflicts with a secret")
			}
		}
		if err = s.validateOutboundRevision(binding.Outbound, selected.Deployment); err != nil {
			return DispatchArtifact{}, err
		}
		var copied map[string]json.RawMessage
		if err = json.Unmarshal(encoded, &copied); err != nil {
			return DispatchArtifact{}, err
		}
		policy = &dispatchOutboundSnapshot{active: cloneExecutionActive(*selected), parameters: copied, names: slices.Clone(binding.Outbound.Parameters), entrypoint: binding.Outbound.Entrypoint}
	} else if parameters != nil {
		return DispatchArtifact{}, dispatchArgumentError("outbound arguments require an outbound Worker")
	}
	transport, parent, depth := token, "", 0
	if strings.HasPrefix(token, invocationCapabilityPrefix) {
		s.dispatch.mu.Lock()
		lease := s.dispatch.leases[token]
		if lease == nil || !lease.OutboundExecution {
			s.dispatch.mu.Unlock()
			return DispatchArtifact{}, ErrRuntimeGrantDenied
		}
		transport, parent, depth, root = lease.TransportToken, token, lease.Depth+1, lease.Root
		s.dispatch.mu.Unlock()
		if depth > 8 {
			return DispatchArtifact{}, ErrDispatchDepth
		}
	}
	return s.acquireResolvedDispatch(ctx, token, transport, caller, app, root, parent, depth, dispatchAdmission{outbound: policy})
}

func (s *Service) AcquireOutboundDispatch(ctx context.Context, transport, parentToken string) (DispatchArtifact, error) {
	caller, err := s.AuthorizeRuntime(ctx, parentToken, RuntimeOutbound, "")
	if err != nil {
		return DispatchArtifact{}, err
	}
	if err := s.AuthorizeDispatchTransport(ctx, transport, parentToken); err != nil {
		return DispatchArtifact{}, err
	}
	s.dispatch.mu.Lock()
	parent := s.dispatch.leases[parentToken]
	if parent == nil || parent.Outbound == nil || parent.OutboundExecution || parent.Root == "" {
		s.dispatch.mu.Unlock()
		return DispatchArtifact{}, ErrRuntimeGrantDenied
	}
	policy, root, depth := parent.Outbound, parent.Root, parent.Depth+1
	namespace, customer := parent.admissionIdentity()
	s.dispatch.mu.Unlock()
	if depth > 8 {
		return DispatchArtifact{}, ErrDispatchDepth
	}
	// Name reuse cannot redirect an admitted target. Current deletion revokes it.
	if _, err = s.appForOrg(caller.App.OrgID, policy.active.App.ID); err != nil {
		return DispatchArtifact{}, err
	}
	return s.acquireResolvedDispatch(ctx, parentToken, transport, caller, policy.active.App, root, parentToken, depth, dispatchAdmission{outboundExecution: true, selected: &policy.active, parameters: policy.parameters, parameterNames: policy.names, entrypoint: policy.entrypoint, namespace: namespace, customerID: customer})
}

// Direct network forwarding is unavailable on customer leases with a policy.
func (s *Service) AuthorizeDirectDispatchOutbound(ctx context.Context, transport, token string) error {
	if err := s.AuthorizeDispatchTransport(ctx, transport, token); err != nil {
		return err
	}
	s.dispatch.mu.Lock()
	defer s.dispatch.mu.Unlock()
	lease := s.dispatch.leases[token]
	if lease == nil || lease.Outbound != nil {
		return ErrRuntimeGrantDenied
	}
	return nil
}

// AcquireOutboundNestedDispatch admits the target's own namespace binding under
// the existing root, never under the customer's namespace authority.
func (s *Service) AcquireOutboundNestedDispatch(ctx context.Context, transport, parent, binding, script string, parameters map[string]json.RawMessage) (DispatchArtifact, error) {
	if err := s.AuthorizeDispatchTransport(ctx, transport, parent); err != nil {
		return DispatchArtifact{}, err
	}
	principal, err := s.authorizeInvocation(ctx, parent)
	if err != nil {
		return DispatchArtifact{}, err
	}
	if !principal.Grant.outboundExecution {
		return DispatchArtifact{}, ErrRuntimeGrantDenied
	}
	for _, b := range principal.Deployment.DispatchNamespaces {
		if b.Binding == binding {
			return s.AcquireDispatchBinding(ctx, parent, b.NamespaceID, script, "", binding, parameters)
		}
	}
	return DispatchArtifact{}, ErrRuntimeGrantDenied
}
