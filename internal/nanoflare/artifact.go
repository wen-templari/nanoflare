package nanoflare

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

var (
	ErrArtifactNotFound  = errors.New("deployment artifact not found")
	ErrArtifactConflict  = errors.New("immutable deployment artifact already exists with different content")
	ErrPublicationFailed = errors.New("publication failed; use a new idempotency key for a new attempt")
)

// ArtifactSecret is private persistence data. Public APIs expose only SecretRef.
type ArtifactSecret struct {
	Name       string    `json:"name"`
	Version    string    `json:"version"`
	Nonce      []byte    `json:"nonce"`
	Ciphertext []byte    `json:"ciphertext"`
	CreatedAt  time.Time `json:"created_at,omitempty"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}
type SecretRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type DeploymentArtifact struct {
	Management   *ArtifactManagement `json:"management,omitempty"`
	Deployment   Deployment          `json:"deployment"`
	ObjectKey    string              `json:"object_key"`
	BundleSize   int64               `json:"bundle_size"`
	Digest       string              `json:"digest"`
	AssetDigests map[string]string   `json:"asset_digests"`
	Secrets      []ArtifactSecret    `json:"sealed_secrets"`
	Policy       *NamespacePolicy    `json:"namespace_policy,omitempty"`
}
type NamespacePolicyRef struct {
	NamespaceID string `json:"namespace_id"`
	Revision    int64  `json:"revision"`
}
type ArtifactManifest struct {
	DeploymentID string              `json:"deployment_id"`
	WorkerID     string              `json:"worker_id"`
	Digest       string              `json:"digest"`
	AssetDigests map[string]string   `json:"asset_digests"`
	Secrets      []SecretRef         `json:"secret_refs"`
	Policy       *NamespacePolicyRef `json:"namespace_policy,omitempty"`
	CreatedAt    time.Time           `json:"created_at"`
}

func (a DeploymentArtifact) Manifest() ArtifactManifest {
	refs := make([]SecretRef, 0, len(a.Secrets))
	for _, s := range a.Secrets {
		refs = append(refs, SecretRef{s.Name, s.Version})
	}
	var policy *NamespacePolicyRef
	if a.Policy != nil {
		policy = &NamespacePolicyRef{NamespaceID: a.Policy.NamespaceID, Revision: a.Policy.Revision}
	}
	return ArtifactManifest{DeploymentID: a.Deployment.ID, WorkerID: a.Deployment.AppID, Digest: a.Digest, AssetDigests: a.AssetDigests, Secrets: refs, Policy: policy, CreatedAt: a.Deployment.CreatedAt}
}
func cloneArtifact(a DeploymentArtifact) DeploymentArtifact {
	data, _ := json.Marshal(a)
	var out DeploymentArtifact
	_ = json.Unmarshal(data, &out)
	out.Deployment.ObjectKey = out.ObjectKey
	out.Deployment.BundleSize = out.BundleSize
	return out
}
func hashBytes(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
func sealRecords(records []SecretRecord) []ArtifactSecret {
	out := make([]ArtifactSecret, 0, len(records))
	for _, r := range records {
		payload := append(append([]byte{}, r.Nonce...), r.Ciphertext...)
		out = append(out, ArtifactSecret{Name: r.Name, Version: hashBytes(payload), Nonce: append([]byte{}, r.Nonce...), Ciphertext: append([]byte{}, r.Ciphertext...), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func (s *Service) captureArtifact(item ActiveDeployment, secrets []ArtifactSecret) (DeploymentArtifact, error) {
	d := item.Deployment
	if err := s.hydrateDeploymentFiles(&d); err != nil {
		return DeploymentArtifact{}, err
	}
	a := DeploymentArtifact{Deployment: d, ObjectKey: d.ObjectKey, BundleSize: d.BundleSize, AssetDigests: map[string]string{}, Secrets: secrets}
	if item.App.Platform != nil {
		ns, err := s.store.GetDispatchNamespace(item.App.Platform.NamespaceID)
		if err != nil {
			return a, err
		}
		a.Policy = &NamespacePolicy{NamespaceID: ns.ID, Revision: ns.PolicyRevision, Grants: ns.Grants, CreatedAt: ns.CreatedAt}
	}
	for _, asset := range d.Assets {
		data := asset.Data
		if asset.ObjectKey != "" {
			if s.objects == nil {
				return a, errors.New("object storage is not configured")
			}
			object, err := s.objects.Get(d.AppID, asset.ObjectKey)
			if err != nil {
				return a, err
			}
			data = object.Body
		}
		a.AssetDigests[asset.Path] = hashBytes(data)
	}
	// The digest describes effective code/config, asset bytes and opaque secret
	// versions. Deployment IDs, storage keys and wall-clock audit fields are excluded.
	content := d
	content.ID = ""
	content.AppID = ""
	content.Port = 0
	content.CreatedAt = time.Time{}
	content.CreatedBy = ""
	content.CommitHash = ""
	content.CommitMessage = ""
	content.ObjectKey = ""
	content.BundleSize = 0
	content.Assets = append([]AssetFile{}, d.Assets...)
	for i := range content.Assets {
		content.Assets[i].ObjectKey = ""
		content.Assets[i].Data = nil
	}
	refs := a.Manifest().Secrets
	data, err := json.Marshal(struct {
		Deployment Deployment
		Assets     map[string]string
		Secrets    []SecretRef
		Policy     *NamespacePolicy
	}{content, a.AssetDigests, refs, a.Policy})
	if err != nil {
		return a, err
	}
	var canonical any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err = decoder.Decode(&canonical); err != nil {
		return a, err
	}
	data, err = json.Marshal(canonical)
	if err != nil {
		return a, err
	}
	a.Digest = hashBytes(data)
	return a, nil
}
func (s *Service) freezeArtifact(item ActiveDeployment, inherited *DeploymentArtifact) (DeploymentArtifact, error) {
	existing, err := s.store.GetDeploymentArtifact(item.App.ID, item.Deployment.ID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrArtifactNotFound) {
		return existing, err
	}
	var secrets []ArtifactSecret
	if inherited != nil {
		secrets = inherited.Secrets
	} else {
		records, e := s.store.ListSecrets(item.App.ID)
		if e != nil {
			return existing, e
		}
		secrets = sealRecords(records)
	}
	a, err := s.captureArtifact(item, secrets)
	if err != nil {
		return a, err
	}
	if inherited != nil {
		if len(a.AssetDigests) != len(inherited.AssetDigests) {
			return a, errors.New("derived deployment changed immutable assets")
		}
		for path, digest := range inherited.AssetDigests {
			if a.AssetDigests[path] != digest {
				return a, errors.New("derived deployment asset integrity check failed")
			}
		}
		state, managed, err := s.store.GetScriptManagementState(item.App.ID)
		if err != nil {
			return a, err
		}
		if managed {
			metadata, err := s.nativeManagementMetadata(a.Deployment, a.Secrets, state.Metadata)
			if err != nil {
				return a, err
			}
			a.Management = &ArtifactManagement{ExpectedRevision: state.Revision, Metadata: metadata}
		}
	}
	return a, s.store.PutDeploymentArtifact(a)
}
func (s *Service) artifactSecretValues(a DeploymentArtifact) (map[string]string, error) {
	values := map[string]string{}
	if len(a.Secrets) > 0 && s.secrets == nil {
		return nil, errors.New("NANOFLARE_SECRET_KEY is required for secret operations")
	}
	for _, secret := range a.Secrets {
		value, err := s.secrets.Decrypt(secret.Nonce, secret.Ciphertext)
		if err != nil {
			return nil, err
		}
		values[secret.Name] = value
	}
	return values, nil
}
func (s *Service) hydrateArtifact(item *ActiveDeployment) error {
	a, err := s.store.GetDeploymentArtifact(item.App.ID, item.Deployment.ID)
	if errors.Is(err, ErrArtifactNotFound) {
		if err = s.hydrateDeploymentFiles(&item.Deployment); err != nil {
			return err
		}
		item.App.SecretValues, err = s.resolvedSecretValues(item.App.ID)
		return err
	}
	if err != nil {
		return err
	}
	item.Deployment = a.Deployment
	item.App.SecretValues, err = s.artifactSecretValues(a)
	return err
}
func (s *Service) ArtifactForOrg(org, worker, revision string) (ArtifactManifest, error) {
	if _, err := s.appForOrg(org, worker); err != nil {
		return ArtifactManifest{}, err
	}
	a, err := s.store.GetDeploymentArtifact(worker, revision)
	if err != nil {
		return ArtifactManifest{}, err
	}
	return a.Manifest(), nil
}
func (s *Service) ResolveNamespaceArtifactForOrg(org, namespace, script, preferred string) (ArtifactManifest, error) {
	app, err := s.NamespaceScript(org, namespace, script)
	if err != nil {
		return ArtifactManifest{}, err
	}
	active, err := s.store.ActiveDeploymentsForApp(app.ID)
	if err != nil {
		return ArtifactManifest{}, err
	}
	selected := selectWeightedDeploymentWithPreference(active, preferred)
	if selected == nil {
		return ArtifactManifest{}, ErrArtifactNotFound
	}
	return s.ArtifactForOrg(org, app.ID, selected.Deployment.ID)
}

func canonicalPayload(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var decoded any
	if err = decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	return json.Marshal(decoded)
}
func sameArtifact(a, b DeploymentArtifact) bool {
	left, e1 := canonicalPayload(a)
	right, e2 := canonicalPayload(b)
	return e1 == nil && e2 == nil && bytes.Equal(left, right)
}
