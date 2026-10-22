package nanoflare

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"time"
	"unicode/utf8"
)

// nativeManagementMetadata projects native mutations into the adapter's latest
// upload view. Existing JSON string bindings retain their explicit JSON type.
func (s *Service) nativeManagementMetadata(d Deployment, secrets []ArtifactSecret, previous ScriptManagementMetadata) (ScriptManagementMetadata, error) {
	metadata := ScriptManagementMetadata{Tags: append([]string{}, previous.Tags...)}
	oldKinds := map[string]string{}
	for _, raw := range previous.Bindings {
		var item struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &item)
		oldKinds[item.Name] = item.Type
	}
	bindings := map[string]json.RawMessage{}
	add := func(name, kind string, fields map[string]any) {
		fields["name"] = name
		fields["type"] = kind
		encoded, _ := json.Marshal(fields)
		bindings[name] = encoded
	}
	for name, raw := range d.Vars {
		var text string
		value := bytes.TrimSpace(raw)
		if len(value) > 0 && value[0] == '"' && json.Unmarshal(raw, &text) == nil && oldKinds[name] != "json" {
			add(name, "plain_text", map[string]any{"text": text})
		} else {
			add(name, "json", map[string]any{"json": raw})
		}
	}
	for _, binding := range d.KVNamespaces {
		add(binding.Binding, "kv_namespace", map[string]any{"namespace_id": binding.ID})
	}
	for _, binding := range d.Databases {
		add(binding.Binding, "d1", map[string]any{"id": binding.DatabaseID})
	}
	for _, binding := range d.ObjectStorageBuckets {
		bucket, err := s.store.GetObjectStorageBucket(binding.BucketID)
		if err != nil {
			return metadata, err
		}
		add(binding.Binding, "r2_bucket", map[string]any{"bucket_name": bucket.Name})
	}
	for _, binding := range d.Services {
		service := binding.WorkerID
		if service == "" {
			service = binding.Service
		}
		add(binding.Binding, "service", map[string]any{"service": service})
	}
	if len(d.Assets) > 0 && d.AssetConfig.Binding != "" {
		add(d.AssetConfig.Binding, "assets", map[string]any{})
	}
	for _, secret := range secrets {
		add(secret.Name, "secret_text", map[string]any{})
	}
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		metadata.Bindings = append(metadata.Bindings, bindings[name])
	}
	if err := validateManagementMetadata(metadata); err != nil {
		return metadata, err
	}
	return metadata, nil
}

// Called under the shared publication guard. Legacy workers keep their existing
// saved-secret behavior; managed Workers preserve the latest uploaded secret set.
func (s *Service) nativeManagedPublication(app App, d Deployment) (*managedPublication, error) {
	state, found, err := s.store.GetScriptManagementState(app.ID)
	if err != nil || !found {
		return nil, err
	}
	base, err := s.store.GetDeploymentArtifact(app.ID, state.DeploymentID)
	if err != nil {
		return nil, err
	}
	metadata, err := s.nativeManagementMetadata(d, base.Secrets, state.Metadata)
	if err != nil {
		return nil, err
	}
	return &managedPublication{ExpectedRevision: state.Revision, Metadata: metadata, Secrets: base.Secrets}, nil
}

func (s *Service) managedLatestArtifact(worker string) (DeploymentArtifact, bool, error) {
	state, found, err := s.store.GetScriptManagementState(worker)
	if err != nil || !found {
		return DeploymentArtifact{}, found, err
	}
	artifact, err := s.store.GetDeploymentArtifact(worker, state.DeploymentID)
	return artifact, true, err
}

// Native secret operations retain every active code revision and traffic weight.
// The latest uploaded code gets its own derived artifact even when it is inactive
// after rollback. All derived artifacts share one complete selected secret set.
func (s *Service) mutateManagedSecretLocked(worker, name string, value *string) (bool, error) {
	state, found, err := s.store.GetScriptManagementState(worker)
	if err != nil || !found {
		return found, err
	}
	base, err := s.store.GetDeploymentArtifact(worker, state.DeploymentID)
	if err != nil {
		return true, err
	}
	selected := []ArtifactSecret{}
	exists := false
	var createdAt time.Time
	for _, secret := range base.Secrets {
		if secret.Name == name {
			exists = true
			createdAt = secret.CreatedAt
			if createdAt.IsZero() {
				createdAt = base.Deployment.CreatedAt
			}
			continue
		}
		selected = append(selected, secret)
	}
	if value == nil && !exists {
		return true, ErrSecretNotFound
	}
	if value != nil {
		if !utf8.ValidString(*value) || len(*value) > 1<<20 {
			return true, errors.New("secret values must be UTF-8 and at most 1 MiB")
		}
		if s.secrets == nil {
			return true, errors.New("NANOFLARE_SECRET_KEY is required for secret operations")
		}
		nonce, ciphertext, err := s.secrets.Encrypt(*value)
		if err != nil {
			return true, err
		}
		now := time.Now().UTC()
		if createdAt.IsZero() {
			createdAt = now
		}
		selected = append(selected, ArtifactSecret{Name: name, Version: hashBytes(append(append([]byte{}, nonce...), ciphertext...)), Nonce: nonce, Ciphertext: ciphertext, CreatedAt: createdAt, UpdatedAt: now})
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })
	active, err := s.activeDeployments()
	if err != nil {
		return true, err
	}
	app, err := s.store.GetApp(worker)
	if err != nil {
		return true, err
	}
	sources := map[string]Deployment{base.Deployment.ID: base.Deployment}
	for _, current := range active {
		if current.App.ID == worker {
			sources[current.Deployment.ID] = current.Deployment
		}
	}
	secretNames := map[string]string{}
	for _, secret := range selected {
		secretNames[secret.Name] = ""
	}
	for _, source := range sources {
		if err := validateBindingCollisions(source.Vars, secretNames, source.KVNamespaces, source.Databases, source.ObjectStorageBuckets, source.Services, source.AssetConfig); err != nil {
			return true, err
		}
	}
	ids := make([]string, 0, len(sources))
	for id := range sources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	derived := map[string]DeploymentArtifact{}
	for _, id := range ids {
		next := cloneArtifact(DeploymentArtifact{Deployment: sources[id], ObjectKey: sources[id].ObjectKey, BundleSize: sources[id].BundleSize}).Deployment
		next.ID, err = randomToken()
		if err != nil {
			return true, err
		}
		next.Port = 0
		next.CreatedAt = time.Now().UTC()
		artifact, err := s.captureArtifact(ActiveDeployment{App: app, Deployment: next}, selected)
		if err != nil {
			return true, err
		}
		if id == state.DeploymentID {
			metadata, err := s.nativeManagementMetadata(next, selected, state.Metadata)
			if err != nil {
				return true, err
			}
			artifact.Management = &ArtifactManagement{ExpectedRevision: state.Revision, Metadata: metadata}
		}
		if err := s.store.PutDeploymentArtifact(artifact); err != nil {
			return true, err
		}
		if err := s.store.StoreDeployment(next); err != nil {
			return true, err
		}
		derived[id] = artifact
	}
	traffic := []DeploymentTraffic{}
	for _, current := range active {
		if current.App.ID == worker {
			traffic = append(traffic, DeploymentTraffic{ID: derived[current.Deployment.ID].Deployment.ID, TrafficPercent: current.TrafficPercent})
		}
	}
	latest := derived[state.DeploymentID]
	key, err := randomToken()
	if err != nil {
		return true, err
	}
	p := Publication{AppID: worker, Key: "managed-secret-" + key, Fingerprint: key, DeploymentID: latest.Deployment.ID, Digest: latest.Digest, Stage: "stored", PreviousTraffic: activeTrafficForApp(active, worker), TargetTraffic: traffic, ConfigurationOnly: len(traffic) == 0, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := s.store.SavePublication(p); err != nil {
		return true, err
	}
	_, err = s.publishStoredArtifact(&p, latest)
	return true, err
}

func (s *Service) reconcileDerivedManagement(worker, revision string) error {
	state, managed, err := s.store.GetScriptManagementState(worker)
	if err != nil || !managed {
		return err
	}
	artifact, err := s.store.GetDeploymentArtifact(worker, revision)
	if err != nil {
		return err
	}
	if artifact.Management == nil {
		return nil
	}
	// A later upload must never be overwritten by retrying an older vars edit.
	if state.Revision > artifact.Management.ExpectedRevision {
		return nil
	}
	return s.commitManagedArtifact(Publication{AppID: worker, DeploymentID: revision}, artifact)
}
