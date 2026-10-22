package nanoflare

// GetRuntimeDeployment returns the immutable authorization/asset snapshot, not a
// selected active revision. Runtime lifetime is checked independently by the runner.
func (s *Store) GetRuntimeDeployment(appID, deploymentID string) (Deployment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.deployments[appID] {
		if d.ID == deploymentID {
			return d, nil
		}
	}
	return Deployment{}, ErrAppNotFound
}
