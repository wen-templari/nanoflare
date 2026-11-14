package nanoflare

import "errors"

// ActiveWorkerBindingsForOrg returns storage and dispatch bindings for every traffic-bearing
// revision. Inactive history is deliberately excluded, including after rollback.
func (s *Service) ActiveWorkerBindingsForOrg(orgID, appID string) ([]WorkerDeployment, error) {
	if _, err := s.appForOrg(orgID, appID); err != nil {
		return nil, err
	}
	active, err := s.store.ActiveDeploymentsForApp(appID)
	if err != nil {
		return nil, err
	}
	result := make([]WorkerDeployment, 0, len(active))
	for _, item := range active {
		if item.TrafficPercent <= 0 {
			continue
		}
		// The immutable artifact is authoritative for retained configuration;
		// PostgreSQL's legacy deployment columns do not store dispatch bindings.
		artifact, err := s.store.GetDeploymentArtifact(appID, item.Deployment.ID)
		if err == nil {
			item.Deployment = artifact.Deployment
		} else if !errors.Is(err, ErrArtifactNotFound) {
			return nil, err
		}
		result = append(result, WorkerDeployment{
			ID: item.Deployment.ID, TrafficPercent: item.TrafficPercent,
			DispatchNamespaces:   CloneDispatchBindings(item.Deployment.DispatchNamespaces),
			KVNamespaces:         append([]KVBinding(nil), item.Deployment.KVNamespaces...),
			Databases:            append([]DatabaseBinding(nil), item.Deployment.Databases...),
			ObjectStorageBuckets: append([]ObjectStorageBucketBinding(nil), item.Deployment.ObjectStorageBuckets...),
		})
	}
	return result, nil
}
