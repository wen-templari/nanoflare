package nanoflare

import "fmt"

// WorkerPublication is the durable desired publication. Traffic and Generation
// commit in the same transaction as routing; PublishedGeneration is an ack only.
// An interrupted publication always reconciles current routing, never an older
// desired record. Rollbacks allocate a new generation too.
type WorkerPublication struct {
	AppID               string              `json:"app_id"`
	Generation          uint64              `json:"generation"`
	PublishedGeneration uint64              `json:"published_generation"`
	Traffic             []DeploymentTraffic `json:"traffic"`
}

// PublicationValidator prepares and validates immutable candidates without
// admitting public traffic. Write publishes the already validated candidate.
type PublicationValidator interface {
	ValidatePublication([]ActiveDeployment) error
}

// WorkerPublicationValidator validates a target without assuming preparation
// created a pending generation. Compatibility runtimes may reuse active state.
type WorkerPublicationValidator interface {
	ValidateWorkerPublication([]ActiveDeployment, string) error
}

func (s *Store) advancePublication(appID string) {
	state := s.workerPublications[appID]
	state.AppID = appID
	state.Generation++
	state.Traffic = nil
	for id, percent := range s.active[appID] {
		state.Traffic = append(state.Traffic, DeploymentTraffic{ID: id, TrafficPercent: percent})
	}
	s.workerPublications[appID] = state
}

func (s *Store) PublicationStates() ([]WorkerPublication, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	states := make([]WorkerPublication, 0, len(s.workerPublications))
	for _, state := range s.workerPublications {
		state.Traffic = append([]DeploymentTraffic(nil), state.Traffic...)
		states = append(states, state)
	}
	return states, nil
}

func (s *Store) AcknowledgePublication(appID string, generation uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, exists := s.workerPublications[appID]
	if !exists || generation > state.Generation {
		return ErrDeploymentConflict
	}
	if generation > state.PublishedGeneration {
		state.PublishedGeneration = generation
		s.workerPublications[appID] = state
	}
	return nil
}

func (s *Service) writePublication(active []ActiveDeployment) error {
	states, err := s.store.PublicationStates()
	if err != nil {
		return err
	}
	if err := s.writer.Write(active); err != nil {
		return err
	}
	for _, state := range states {
		if state.Generation == state.PublishedGeneration {
			continue
		}
		if err := s.store.AcknowledgePublication(state.AppID, state.Generation); err != nil {
			return fmt.Errorf("acknowledge worker publication: %w", err)
		}
	}
	return nil
}

// ReconcilePublications is the compatibility name for the unified startup path.
func (s *Service) ReconcilePublications() error { return s.ReconcileRuntime() }

func (s *Service) recoverPendingVarsLocked() error {
	pending, err := s.store.PendingVarsUpdates()
	if err != nil {
		return err
	}
	for _, op := range pending {
		if op.Status == "rolling_back" {
			if err := s.recoverVarsRollback(op); err != nil {
				return err
			}
		}
	}
	records, err := s.store.ListDeployments()
	if err != nil {
		return err
	}
	for _, op := range pending {
		if op.Status != "pending" {
			continue
		}
		status := "failed"
		for _, record := range records {
			if record.Deployment.AppID == op.AppID && record.Deployment.ID == op.Result.ID {
				status = "succeeded"
				break
			}
		}
		if status == "succeeded" {
			if err := s.reconcileDerivedManagement(op.AppID, op.Result.ID); err != nil {
				return err
			}
		}
		if err := s.store.FinishVarsUpdate(op.AppID, op.Key, status); err != nil {
			return err
		}
	}
	return nil
}

// validatePublication builds the candidate without changing repository routing.
// selected deployments include hydrated code and resolved secrets.
func (s *Service) validatePublication(appID string, previous []ActiveDeployment, selected []ActiveDeployment) error {
	_, validates := s.writer.(PublicationValidator)
	_, targets := s.writer.(WorkerPublicationValidator)
	if !validates && !targets {
		return nil
	}
	candidate := make([]ActiveDeployment, 0, len(previous)+len(selected))
	for _, active := range previous {
		if active.App.ID != appID {
			candidate = append(candidate, active)
		}
	}
	for _, active := range selected {
		if err := s.hydrateDeploymentFiles(&active.Deployment); err != nil {
			return err
		}
		secrets, err := s.resolvedSecretValues(appID)
		if err != nil {
			return err
		}
		active.App.SecretValues = secrets
		candidate = append(candidate, active)
	}
	return s.validatePreparedPublication(appID, candidate)
}

// candidate already contains hydrated artifacts and their pinned secret values.
func (s *Service) validatePreparedPublication(appID string, candidate []ActiveDeployment) error {
	if validator, ok := s.writer.(WorkerPublicationValidator); ok {
		return validator.ValidateWorkerPublication(candidate, appID)
	}
	if validator, ok := s.writer.(PublicationValidator); ok {
		return validator.ValidatePublication(candidate)
	}
	return nil
}

func (s *Service) acknowledgeCurrentPublications() error {
	states, err := s.store.PublicationStates()
	if err != nil {
		return err
	}
	for _, state := range states {
		if state.Generation > state.PublishedGeneration {
			if err := s.store.AcknowledgePublication(state.AppID, state.Generation); err != nil {
				return err
			}
		}
	}
	return nil
}
