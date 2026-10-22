package nanoflare

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type Publication struct {
	RequestIdentity   string              `json:"request_identity,omitempty"`
	AppID             string              `json:"worker_id"`
	Key               string              `json:"key"`
	Fingerprint       string              `json:"fingerprint"`
	DeploymentID      string              `json:"deployment_id"`
	Digest            string              `json:"digest"`
	Stage             string              `json:"stage"`
	RuntimeReady      bool                `json:"runtime_ready"`
	TargetTraffic     []DeploymentTraffic `json:"target_traffic,omitempty"`
	ConfigurationOnly bool                `json:"configuration_only,omitempty"`
	PreviousTraffic   []DeploymentTraffic `json:"previous_traffic"`
	CreatedAt         time.Time           `json:"created_at"`
	UpdatedAt         time.Time           `json:"updated_at"`
}
type PublicationResult struct {
	WorkerID     string    `json:"worker_id"`
	Key          string    `json:"idempotency_key"`
	DeploymentID string    `json:"deployment_id"`
	Digest       string    `json:"digest"`
	Stage        string    `json:"stage"`
	RuntimeReady bool      `json:"runtime_ready_verified"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (p Publication) Result() PublicationResult {
	return PublicationResult{p.AppID, p.Key, p.DeploymentID, p.Digest, p.Stage, p.RuntimeReady, p.CreatedAt, p.UpdatedAt}
}

type PublicationPreparation struct {
	Generation  string
	Deployments []ActiveDeployment
}
type PublicationWriter interface {
	PreparePublication([]ActiveDeployment) (PublicationPreparation, error)
	CommitPublication(PublicationPreparation) error
	AbortPublication(PublicationPreparation) error
}

func (s *Service) PublicationForOrg(org, app, key string) (PublicationResult, error) {
	if _, err := s.appForOrg(org, app); err != nil {
		return PublicationResult{}, err
	}
	p, ok, err := s.store.GetPublication(app, key)
	if err != nil {
		return PublicationResult{}, err
	}
	if !ok {
		return PublicationResult{}, ErrArtifactNotFound
	}
	return p.Result(), nil
}
func publicationFingerprint(in DeployInput) (string, error) {
	in.CreatedBy = ""
	data, err := canonicalPayload(in)
	if err != nil {
		return "", err
	}
	return hashBytes(data), nil
}
func (s *Service) savePublication(p *Publication, stage string) error {
	p.Stage = stage
	p.UpdatedAt = time.Now().UTC()
	return s.store.SavePublication(*p)
}
func (s *Service) replayPublication(p Publication) (Deployment, error) {
	if p.Stage == "building" {
		_ = s.savePublication(&p, "failed")
		return Deployment{}, ErrPublicationFailed
	}
	if p.Stage == "failed" {
		return Deployment{}, ErrPublicationFailed
	}
	a, err := s.store.GetDeploymentArtifact(p.AppID, p.DeploymentID)
	if err != nil {
		return Deployment{}, err
	}
	if p.Stage == "route_published" || p.Stage == "superseded" {
		return a.Deployment, nil
	}
	if err := s.commitManagedArtifact(p, a); err != nil {
		if errors.Is(err, ErrManagementConflict) {
			return a.Deployment, s.savePublication(&p, "superseded")
		}
		return Deployment{}, err
	}
	if p.ConfigurationOnly {
		return s.publishStoredArtifact(&p, a)
	}
	active, err := s.store.ActiveDeploymentsForApp(p.AppID)
	if err != nil {
		return Deployment{}, err
	}
	current := sameTraffic(activeTrafficForApp(active, p.AppID), p.targetTraffic())
	if p.Stage == "rolling_back" {
		if !current && !sameTraffic(activeTrafficForApp(active, p.AppID), p.PreviousTraffic) {
			return a.Deployment, s.savePublication(&p, "superseded")
		}
		if err = s.store.SetActiveTraffic(p.AppID, p.PreviousTraffic); err != nil {
			return Deployment{}, err
		}
		if err = s.publishActiveDeployments(p.AppID); err != nil {
			return Deployment{}, err
		}
		if err = s.savePublication(&p, "failed"); err != nil {
			return Deployment{}, err
		}
		return Deployment{}, ErrPublicationFailed
	}
	if current {
		if err = s.publishActiveDeployments(p.AppID); err != nil {
			return Deployment{}, err
		}
		app, lookupErr := s.store.GetApp(p.AppID)
		if lookupErr != nil {
			return Deployment{}, lookupErr
		}
		p.RuntimeReady = s.writerRuntimeVerifies() && !app.IsDynamic()
		if err = s.savePublication(&p, "route_published"); err != nil {
			return Deployment{}, err
		}
		return a.Deployment, nil
	}

	// Retry only if traffic still matches the state observed before publication.
	if !sameTraffic(activeTrafficForApp(active, p.AppID), p.PreviousTraffic) {
		return a.Deployment, s.savePublication(&p, "superseded")
	}
	if p.Stage == "stored" || p.Stage == "activating" || p.Stage == "runtime_ready" {
		return s.publishStoredArtifact(&p, a)
	}
	return Deployment{}, ErrPublicationFailed
}
func (s *Service) writerRuntimeVerifies() bool { _, ok := s.writer.(PublicationWriter); return ok }
func (s *Service) publishStoredArtifact(p *Publication, a DeploymentArtifact) (Deployment, error) {
	if err := s.commitManagedArtifact(*p, a); err != nil {
		return Deployment{}, err
	}
	app, err := s.store.GetApp(p.AppID)
	if err != nil {
		return Deployment{}, err
	}
	if err = s.checkNamespaceBindings(app, DeployInput{KVNamespaces: a.Deployment.KVNamespaces, Databases: a.Deployment.Databases, ObjectStorageBuckets: a.Deployment.ObjectStorageBuckets}); err != nil {
		_ = s.savePublication(p, "failed")
		return Deployment{}, err
	}
	if p.ConfigurationOnly {
		p.RuntimeReady = false
		return a.Deployment, s.savePublication(p, "route_published")
	}
	active, err := s.activeDeployments()
	if err != nil {
		return Deployment{}, err
	}
	var candidate []ActiveDeployment
	for _, item := range active {
		if item.App.ID != p.AppID {
			candidate = append(candidate, item)
		}
	}
	for _, target := range p.targetTraffic() {
		targetArtifact := a
		if target.ID != a.Deployment.ID {
			targetArtifact, err = s.store.GetDeploymentArtifact(p.AppID, target.ID)
			if err != nil {
				return Deployment{}, err
			}
		}
		if err = s.checkNamespaceBindings(app, DeployInput{KVNamespaces: targetArtifact.Deployment.KVNamespaces, Databases: targetArtifact.Deployment.Databases, ObjectStorageBuckets: targetArtifact.Deployment.ObjectStorageBuckets}); err != nil {
			return Deployment{}, err
		}
		next := ActiveDeployment{App: app, Deployment: targetArtifact.Deployment, TrafficPercent: target.TrafficPercent}
		if err = s.hydrateArtifact(&next); err != nil {
			return Deployment{}, err
		}
		candidate = append(candidate, next)
	}
	var prepared PublicationPreparation
	publisher, twoPhase := s.writer.(PublicationWriter)
	if twoPhase {
		prepared, err = publisher.PreparePublication(candidate)
		if err != nil {
			_ = s.savePublication(p, "failed")
			return Deployment{}, fmt.Errorf("prepare runtime: %w", err)
		}
		p.RuntimeReady = !app.IsDynamic()
		if err = s.savePublication(p, "runtime_ready"); err != nil {
			_ = publisher.AbortPublication(prepared)
			return Deployment{}, err
		}
	}
	if err = s.savePublication(p, "activating"); err != nil {
		if twoPhase {
			_ = publisher.AbortPublication(prepared)
		}
		return Deployment{}, err
	}
	if err = s.store.SetActiveTraffic(p.AppID, p.targetTraffic()); err != nil {
		if twoPhase {
			_ = publisher.AbortPublication(prepared)
		}
		// A database commit error can have an unknown outcome. Retain activation
		// intent so a retry reconciles actual traffic instead of declaring failure.
		return Deployment{}, err
	}
	s.invalidateRuntimeDeployments(p.AppID)
	if twoPhase {
		err = publisher.CommitPublication(prepared)
	} else {
		err = s.writer.Write(candidate)
	}
	if err == nil {
		err = s.acknowledgeCurrentPublications()
	}
	if err != nil {
		if twoPhase {
			_ = publisher.AbortPublication(prepared)
		}
		if e := s.savePublication(p, "rolling_back"); e != nil {
			return Deployment{}, fmt.Errorf("publication failed: %w; persist rollback: %v", err, e)
		}
		if e := s.store.SetActiveTraffic(p.AppID, p.PreviousTraffic); e != nil {
			return Deployment{}, fmt.Errorf("publication failed: %w; restore traffic: %v", err, e)
		}
		if e := s.publishActiveDeployments(p.AppID); e != nil {
			return Deployment{}, fmt.Errorf("publication failed: %w; restore runtime/routes: %v", err, e)
		}
		if e := s.savePublication(p, "failed"); e != nil {
			return Deployment{}, e
		}
		return Deployment{}, fmt.Errorf("write generated config: %w", err)
	}
	if err = s.savePublication(p, "route_published"); err != nil {
		return Deployment{}, fmt.Errorf("publication applied; retry the same idempotency key to recover: %w", err)
	}
	return a.Deployment, nil
}

// deployPublication holds the same cross-process publication guard as vars and
// secrets. Source/config validation still uses the normal deployment pipeline.
func (s *Service) deployPublication(app string, in DeployInput) (Deployment, error) {
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	if len(in.IdempotencyKey) > 256 {
		return Deployment{}, errors.New("idempotency_key max 256 bytes")
	}
	if in.IdempotencyKey == "" {
		key, err := randomToken()
		if err != nil {
			return Deployment{}, err
		}
		in.IdempotencyKey = key
	}
	fingerprint, err := publicationFingerprint(in)
	if err != nil {
		return Deployment{}, err
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	release, err := s.store.LockDeploymentUpdates()
	if err != nil {
		return Deployment{}, err
	}
	defer release()
	if _, err = s.store.GetApp(app); err != nil {
		return Deployment{}, err
	}
	prior, found, err := s.store.GetPublication(app, in.IdempotencyKey)
	if err != nil {
		return Deployment{}, err
	}
	if found {
		if prior.Fingerprint != fingerprint {
			return Deployment{}, ErrIdempotencyConflict
		}
		return s.replayPublication(prior)
	}
	return s.buildAndPublishDeployment(app, in, fingerprint, nil)
}

func sameTraffic(a, b []DeploymentTraffic) bool {
	if len(a) != len(b) {
		return false
	}
	values := map[string]int{}
	for _, t := range a {
		values[t.ID] = t.TrafficPercent
	}
	for _, t := range b {
		if weight, ok := values[t.ID]; !ok || weight != t.TrafficPercent {
			return false
		}
	}
	return true
}

// RecoverPublications reconciles durable publication intent before accepting work.
func (s *Service) RecoverPublications() error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	release, err := s.store.LockDeploymentUpdates()
	if err != nil {
		return err
	}
	defer release()
	return s.recoverPublicationsLocked()
}

// ReconcileRuntime restores a runner that lost volatile generation state. Both
// recovery and the final snapshot/write hold the same cross-process publication
// guard as deploy/delete/traffic changes, so an old snapshot cannot undo them.
func (s *Service) ReconcileRuntime() error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	release, err := s.store.LockDeploymentUpdates()
	if err != nil {
		return err
	}
	defer release()
	if err := s.recoverPublicationsLocked(); err != nil {
		return err
	}
	if err := s.recoverPendingVarsLocked(); err != nil {
		return err
	}
	active, err := s.activeDeployments()
	if err != nil {
		return err
	}
	if err := s.writePublication(active); err != nil {
		return err
	}
	for _, item := range active {
		s.invalidateRuntimeDeployments(item.App.ID)
	}
	return nil
}

func (s *Service) recoverPublicationsLocked() error {
	active, err := s.store.ActiveDeployments()
	if err != nil {
		return err
	}
	for _, item := range active {
		if _, err := s.freezeArtifact(item, nil); err != nil {
			return err
		}
		if err := s.reconcileDerivedManagement(item.App.ID, item.Deployment.ID); err != nil {
			return err
		}
	}
	publications, err := s.store.ListPublications()
	if err != nil {
		return err
	}
	for _, p := range publications {
		if p.Stage == "route_published" || p.Stage == "failed" || p.Stage == "superseded" {
			continue
		}
		if _, err := s.replayPublication(p); err != nil && !errors.Is(err, ErrPublicationFailed) {
			return err
		}
	}
	return nil
}

func (p Publication) targetTraffic() []DeploymentTraffic {
	if p.ConfigurationOnly {
		return nil
	}
	if len(p.TargetTraffic) > 0 {
		return p.TargetTraffic
	}
	return []DeploymentTraffic{{ID: p.DeploymentID, TrafficPercent: 100}}
}
