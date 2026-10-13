package nanoflare

// LockDeploymentUpdates serializes configuration publication across services
// sharing this repository. It is separate from mu because publication reads it.
func (s *Store) LockDeploymentUpdates() (func(), error) {
	s.deploymentMu.Lock()
	return s.deploymentMu.Unlock, nil
}

func (s *Store) VarsUpdate(appID, key string) (VarsUpdateOperation, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	op, found := s.varsUpdates[appID][key]
	return op, found, nil
}

func (s *Store) BeginVarsUpdate(op VarsUpdateOperation, next Deployment, expected string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.apps[op.AppID]; !exists {
		return ErrAppNotFound
	}
	if _, exists := s.varsUpdates[op.AppID][op.Key]; exists {
		return ErrIdempotencyConflict
	}
	traffic := s.active[op.AppID]
	if len(traffic) != 1 || traffic[expected] != 100 {
		return ErrDeploymentConflict
	}
	s.deployments[op.AppID] = append(s.deployments[op.AppID], next)
	s.active[op.AppID] = map[string]int{next.ID: 100}
	if s.varsUpdates[op.AppID] == nil {
		s.varsUpdates[op.AppID] = make(map[string]VarsUpdateOperation)
	}
	s.varsUpdates[op.AppID][op.Key] = op
	return nil
}

func (s *Store) FinishVarsUpdate(appID, key, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, exists := s.varsUpdates[appID][key]
	if !exists {
		return ErrAppNotFound
	}
	op.Status = status
	s.varsUpdates[appID][key] = op
	return nil
}
