package nanoflare

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
)

const (
	RuntimeOutbound         = "outbound"
	RuntimeDNS              = "dns"
	RuntimeKV               = "kv"
	RuntimeDatabase         = "db"
	RuntimeObjects          = "objects"
	RuntimeAssets           = "assets"
	RuntimeDurations        = "durations"
	RuntimeLogs             = "logs"
	RuntimeAccess           = "access"
	runtimeCapabilityPrefix = "nfc1."
)

var ErrRuntimeGrantDenied = errors.New("runtime capability does not permit this operation")

type RuntimeGrant struct {
	InstanceID   string `json:"instance,omitempty"`
	OrgID        string `json:"org"`
	AppID        string `json:"app"`
	DeploymentID string `json:"deployment"`
	Kind         string `json:"kind"`
	ResourceID   string `json:"resource"`
}

type RuntimePrincipal struct {
	Grant      RuntimeGrant
	App        App
	Deployment Deployment
}

// RuntimeCapability is minted only by trusted configuration generation. The app's
// root key never leaves the control plane/runner in a Worker-facing credential.
func RuntimeCapability(item ActiveDeployment, kind, resourceID string) string {
	if item.App.RuntimeToken == "" || item.App.ID == "" || item.Deployment.ID == "" {
		return ""
	}
	grant := RuntimeGrant{OrgID: item.App.OrgID, AppID: item.App.ID, DeploymentID: item.Deployment.ID, Kind: kind, ResourceID: resourceID, InstanceID: item.RuntimeInstance}
	data, _ := json.Marshal(grant)
	payload := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, []byte(item.App.RuntimeToken))
	mac.Write([]byte(runtimeCapabilityPrefix + payload))
	return runtimeCapabilityPrefix + payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

type runtimeAuthorization struct {
	mu       sync.RWMutex
	live     func(context.Context, string, string) (bool, error)
	instance func(context.Context, string, string, string) (bool, error)
}

// SetRuntimeDeploymentChecker connects authorization to actual runner lifetime.
// Without a runner (e.g. a standalone adapter), only active stored revisions work.
func (s *Service) SetRuntimeDeploymentChecker(check func(context.Context, string, string) (bool, error)) {
	s.runtimeAuthorization.mu.Lock()
	defer s.runtimeAuthorization.mu.Unlock()
	s.runtimeAuthorization.live = check
}

func (s *Service) AuthorizeRuntime(ctx context.Context, token, kind, resourceID string) (RuntimePrincipal, error) {
	if strings.HasPrefix(token, "nfexec_") || strings.HasPrefix(token, "nfgrant_") {
		resourceKind := map[string]ExecutionResourceKind{RuntimeKV: ExecutionKV, RuntimeDatabase: ExecutionDatabase, RuntimeObjects: ExecutionObject, RuntimeAssets: ExecutionAssets}[kind]
		var active ActiveDeployment
		var err error
		if resourceKind != "" {
			active, _, err = s.executionAuthorization(token, resourceKind, resourceID)
		} else {
			active, err = s.executions.AuthorizeInternal(token)
		}
		if err != nil {
			return RuntimePrincipal{}, err
		}
		grant := RuntimeGrant{OrgID: active.App.OrgID, AppID: active.App.ID, DeploymentID: active.Deployment.ID, InstanceID: active.RuntimeInstance, Kind: kind, ResourceID: resourceID}
		return s.authorizeBoundRuntime(RuntimePrincipal{grant, active.App, active.Deployment}, kind)
	}

	if strings.HasPrefix(token, invocationCapabilityPrefix) {
		p, err := s.authorizeInvocation(ctx, token)
		if err != nil {
			return p, err
		}
		p.Grant.Kind = kind
		p.Grant.ResourceID = resourceID
		return s.authorizeBoundRuntime(p, kind)
	}
	deny := func(err error) (RuntimePrincipal, error) { return RuntimePrincipal{}, err }
	if len(token) > 4096 || !strings.HasPrefix(token, runtimeCapabilityPrefix) {
		return deny(ErrInvalidCapability)
	}
	payload, signature, ok := strings.Cut(strings.TrimPrefix(token, runtimeCapabilityPrefix), ".")
	if !ok {
		return deny(ErrInvalidCapability)
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return deny(ErrInvalidCapability)
	}
	var grant RuntimeGrant
	if json.Unmarshal(data, &grant) != nil || grant.AppID == "" || grant.DeploymentID == "" {
		return deny(ErrInvalidCapability)
	}
	app, err := s.store.GetApp(grant.AppID)
	if err != nil || app.RuntimeToken == "" {
		return deny(ErrInvalidCapability)
	}
	mac := hmac.New(sha256.New, []byte(app.RuntimeToken))
	mac.Write([]byte(runtimeCapabilityPrefix + payload))
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(mac.Sum(nil), sig) {
		return deny(ErrInvalidCapability)
	}
	if app.OrgID != grant.OrgID || grant.Kind != kind || (resourceID != "" && resourceID != grant.ResourceID) {
		return deny(ErrRuntimeGrantDenied)
	}
	deployment, err := s.store.GetRuntimeDeployment(app.ID, grant.DeploymentID)
	if artifact, e := s.store.GetDeploymentArtifact(app.ID, grant.DeploymentID); e == nil {
		deployment = artifact.Deployment
	} else if !errors.Is(e, ErrArtifactNotFound) {
		return deny(e)
	}
	if err != nil {
		return deny(ErrRuntimeGrantDenied)
	}
	s.runtimeAuthorization.mu.RLock()
	check := s.runtimeAuthorization.live
	instanceCheck := s.runtimeAuthorization.instance
	s.runtimeAuthorization.mu.RUnlock()
	live := false
	if grant.InstanceID != "" {
		if instanceCheck == nil {
			return deny(ErrRuntimeGrantDenied)
		}
		live, err = instanceCheck(ctx, app.ID, deployment.ID, grant.InstanceID)
	} else if check != nil {
		live, err = check(ctx, app.ID, deployment.ID)
	} else {
		var active []ActiveDeployment
		active, err = s.store.ActiveDeploymentsForApp(app.ID)
		for _, item := range active {
			if item.Deployment.ID == deployment.ID {
				live = true
				break
			}
		}
	}
	if err != nil {
		return deny(err)
	}
	if !live {
		return deny(ErrRuntimeGrantDenied)
	}
	return s.authorizeBoundRuntime(RuntimePrincipal{grant, app, deployment}, kind)
}
func (s *Service) authorizeBoundRuntime(p RuntimePrincipal, kind string) (RuntimePrincipal, error) {
	app, deployment, grant := p.App, p.Deployment, p.Grant
	deny := func(err error) (RuntimePrincipal, error) { return RuntimePrincipal{}, err }
	bound := false
	switch kind {
	case RuntimeDispatch:
		if !s.dispatch.enabled.Load() || app.IsDynamic() {
			break
		}
		namespace, err := s.DispatchNamespaceForOrg(app.OrgID, grant.ResourceID)
		if err != nil || namespace.Trust != "trusted" {
			break
		}
		for _, b := range deployment.DispatchNamespaces {
			if b.NamespaceID == namespace.ID {
				bound = true
				break
			}
		}
	case RuntimeServiceDispatch:
		if !s.dispatch.enabled.Load() {
			break
		}
		target, err := s.store.GetApp(grant.ResourceID)
		if err != nil || !target.IsDynamic() || !ServiceTargetAllowed(app, target) {
			break
		}
		for _, binding := range deployment.Services {
			if binding.WorkerID == target.ID && binding.Execution == "dynamic" {
				bound = true
				break
			}
		}
	case RuntimeOutbound, RuntimeDNS:
		bound = app.IsDynamic() && grant.ResourceID == ""
	case RuntimeKV:
		resource, e := s.store.GetKVNamespace(grant.ResourceID)
		if e != nil || resource.OrgID != app.OrgID {
			break
		}
		for _, b := range deployment.KVNamespaces {
			if b.ID == resource.ID {
				bound = true
				break
			}
		}
	case RuntimeDatabase:
		resource, e := s.store.GetDatabase(grant.ResourceID)
		if e != nil || resource.OrgID != app.OrgID {
			break
		}
		for _, b := range deployment.Databases {
			if b.DatabaseID == resource.ID {
				bound = true
				break
			}
		}
	case RuntimeObjects:
		resource, e := s.store.GetObjectStorageBucket(grant.ResourceID)
		if e != nil || resource.OrgID != app.OrgID {
			break
		}
		for _, b := range deployment.ObjectStorageBuckets {
			if b.BucketID == resource.ID {
				bound = true
				break
			}
		}
	case RuntimeAssets:
		bound = grant.ResourceID == deployment.ID
	case RuntimeAccess:
		bound = grant.ResourceID == "" && !app.IsDynamic()
	case RuntimeDurations, RuntimeLogs:
		bound = grant.ResourceID == ""
	}
	if bound && (kind == RuntimeKV || kind == RuntimeDatabase || kind == RuntimeObjects) {
		if err := s.namespaceResourceAllowed(app, kind, grant.ResourceID); err != nil {
			return deny(ErrRuntimeGrantDenied)
		}
	}
	if !bound {
		return deny(ErrRuntimeGrantDenied)
	}
	return RuntimePrincipal{grant, app, deployment}, nil
}

func (s *Service) objectCapabilityAppID(capability, bucketID string) (string, error) {
	return s.objectCapabilityAppIDContext(context.Background(), capability, bucketID)
}
func (s *Service) objectCapabilityAppIDContext(ctx context.Context, capability, bucketID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if isRevisionCapability(capability) {
		p, err := s.AuthorizeRuntime(ctx, capability, RuntimeObjects, bucketID)
		if err != nil {
			return "", err
		}
		if s.objects == nil {
			return "", errors.New("object storage is not configured")
		}
		return p.App.ID, nil
	}
	if s.objects == nil {
		return "", errors.New("object storage is not configured")
	}
	appID, err := s.appIDForCapability(capability)
	if err != nil {
		return "", err
	}
	if err = s.ensureCapabilityBindsObjectStorageBucket(appID, bucketID); err != nil {
		return "", err
	}
	return appID, nil
}

func (s *Service) SetRuntimeInstanceChecker(check func(context.Context, string, string, string) (bool, error)) {
	s.runtimeAuthorization.mu.Lock()
	defer s.runtimeAuthorization.mu.Unlock()
	s.runtimeAuthorization.instance = check
}
