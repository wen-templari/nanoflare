package nanoflare

// ActiveWorkerBindingsForOrg returns storage bindings for every traffic-bearing
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
		result = append(result, WorkerDeployment{
			ID: item.Deployment.ID, TrafficPercent: item.TrafficPercent,
			KVNamespaces:         append([]KVBinding(nil), item.Deployment.KVNamespaces...),
			Databases:            append([]DatabaseBinding(nil), item.Deployment.Databases...),
			ObjectStorageBuckets: append([]ObjectStorageBucketBinding(nil), item.Deployment.ObjectStorageBuckets...),
		})
	}
	return result, nil
}
