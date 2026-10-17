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

func (s *Store) advancePublication(appID string) {
	state := s.publications[appID]
	state.AppID = appID
	state.Generation++
	state.Traffic = nil
	for id, percent := range s.active[appID] {
		state.Traffic = append(state.Traffic, DeploymentTraffic{ID: id, TrafficPercent: percent})
	}
	s.publications[appID] = state
}

func (s *Store) PublicationStates() ([]WorkerPublication, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	states := make([]WorkerPublication, 0, len(s.publications))
	for _, state := range s.publications {
		state.Traffic = append([]DeploymentTraffic(nil), state.Traffic...)
		states = append(states, state)
	}
	return states, nil
}

func (s *Store) AcknowledgePublication(appID string, generation uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, exists := s.publications[appID]
	if !exists || generation > state.Generation {
		return ErrDeploymentConflict
	}
	if generation > state.PublishedGeneration {
		state.PublishedGeneration = generation
		s.publications[appID] = state
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

// ReconcilePublications repairs interrupted publication after runtime startup.
// The same lock as deployment mutations prevents stale recovery overwriting a
// newer traffic decision. Startup must call this even when every ack is current:
// a new runtime still needs all current revisions installed.
func (s *Service) ReconcilePublications() error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	release, err := s.store.LockDeploymentUpdates()
	if err != nil {
		return err
	}
	defer release()
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
	active, err := s.activeDeployments()
	if err != nil {
		return err
	}
	if validator, ok := s.writer.(PublicationValidator); ok {
		if err := validator.ValidatePublication(active); err != nil {
			return err
		}
	}
	if err := s.writePublication(active); err != nil {
		return err
	}
	for _, item := range active {
		s.invalidateRuntimeDeployments(item.App.ID)
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
		if err := s.store.FinishVarsUpdate(op.AppID, op.Key, status); err != nil {
			return err
		}
	}
	return nil
}

// validatePublication builds the candidate without changing repository routing.
// selected deployments include hydrated code and resolved secrets.
func (s *Service) validatePublication(appID string, previous []ActiveDeployment, selected []ActiveDeployment) error {
	validator, ok := s.writer.(PublicationValidator)
	if !ok {
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
	return validator.ValidatePublication(candidate)
}
