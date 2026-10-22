package nanoflare

import "errors"

// ManagementUpload is an authorized, secret-free source/config snapshot for an
// adapter operation. It never selects a revision using current traffic weights.
type ManagementUpload struct {
	State        ScriptManagementState
	Deployment   DeployInput
	DeploymentID string
	SecretRefs   []SecretRef
}

func (s *Service) ManagementUploadRevisionForOrg(org, worker, revision string) (ManagementUpload, error) {
	if _, err := s.appForOrg(org, worker); err != nil {
		return ManagementUpload{}, err
	}
	artifact, err := s.store.GetDeploymentArtifact(worker, revision)
	if err != nil {
		return ManagementUpload{}, err
	}
	state := ScriptManagementState{WorkerID: worker, OrgID: org, DeploymentID: revision, UpdatedAt: artifact.Deployment.CreatedAt}
	if artifact.Management != nil {
		state.Revision = artifact.Management.ExpectedRevision + 1
		state.Metadata = artifact.Management.Metadata
	} else {
		state.Metadata, err = s.nativeManagementMetadata(artifact.Deployment, artifact.Secrets, ScriptManagementMetadata{})
		if err != nil {
			return ManagementUpload{}, err
		}
	}
	return ManagementUpload{State: state, Deployment: managementDeploymentInput(artifact.Deployment), DeploymentID: revision, SecretRefs: artifact.Manifest().Secrets}, nil
}

func managementDeploymentInput(d Deployment) DeployInput {
	assets := append([]AssetFile(nil), d.Assets...)
	for index := range assets {
		assets[index].ObjectKey = ""
	}
	return DeployInput{Files: d.Files, Assets: assets, Entrypoint: d.Entrypoint, Format: d.Format, CompatibilityDate: d.CompatibilityDate, CompatibilityFlags: d.CompatibilityFlags, Vars: d.Vars, KVNamespaces: d.KVNamespaces, Databases: d.Databases, ObjectStorageBuckets: d.ObjectStorageBuckets, Services: d.Services, AssetConfig: d.AssetConfig, DNS: d.DNS, Triggers: d.Triggers, DispatchNamespaces: d.DispatchNamespaces}
}

func (s *Service) ManagementUploadForOrg(org, worker string, includeAssetBytes bool) (ManagementUpload, bool, error) {
	app, err := s.appForOrg(org, worker)
	if err != nil {
		return ManagementUpload{}, false, err
	}
	state, managed, err := s.store.GetScriptManagementState(worker)
	if err != nil {
		return ManagementUpload{}, false, err
	}
	revision := state.DeploymentID
	if !managed {
		records, err := s.store.ListDeployments()
		if err != nil {
			return ManagementUpload{}, false, err
		}
		var latest Deployment
		for _, record := range records {
			d := record.Deployment
			if d.AppID == worker && (latest.ID == "" || d.CreatedAt.After(latest.CreatedAt)) {
				latest = d
			}
		}
		if latest.ID == "" {
			return ManagementUpload{}, false, nil
		}
		revision = latest.ID
	}
	artifact, err := s.store.GetDeploymentArtifact(worker, revision)
	if err != nil {
		return ManagementUpload{}, false, err
	}
	d := artifact.Deployment
	if !managed {
		metadata, err := s.nativeManagementMetadata(d, artifact.Secrets, ScriptManagementMetadata{})
		if err != nil {
			return ManagementUpload{}, false, err
		}
		state = ScriptManagementState{WorkerID: worker, OrgID: app.OrgID, Metadata: metadata, UpdatedAt: d.CreatedAt}
	}
	assets := append([]AssetFile(nil), d.Assets...)
	if includeAssetBytes {
		total := 0
		for index := range assets {
			if assets[index].ObjectKey != "" {
				if s.objects == nil {
					return ManagementUpload{}, false, errors.New("object storage is not configured")
				}
				object, err := s.objects.Get(worker, assets[index].ObjectKey)
				if err != nil {
					return ManagementUpload{}, false, err
				}
				if hashBytes(object.Body) != artifact.AssetDigests[assets[index].Path] {
					return ManagementUpload{}, false, errors.New("asset integrity check failed")
				}
				assets[index].Data = object.Body
			}
			total += len(assets[index].Data)
			if total > 10<<20 {
				return ManagementUpload{}, false, errors.New("compatibility asset snapshot exceeds 10 MiB")
			}
		}
	}
	for index := range assets {
		assets[index].ObjectKey = ""
	}
	input := DeployInput{Files: d.Files, Assets: assets, Entrypoint: d.Entrypoint, Format: d.Format, CompatibilityDate: d.CompatibilityDate, CompatibilityFlags: d.CompatibilityFlags, Vars: d.Vars, KVNamespaces: d.KVNamespaces, Databases: d.Databases, ObjectStorageBuckets: d.ObjectStorageBuckets, Services: d.Services, AssetConfig: d.AssetConfig, DNS: d.DNS, Triggers: d.Triggers, DispatchNamespaces: d.DispatchNamespaces}
	return ManagementUpload{State: state, Deployment: input, DeploymentID: revision, SecretRefs: artifact.Manifest().Secrets}, true, nil
}
