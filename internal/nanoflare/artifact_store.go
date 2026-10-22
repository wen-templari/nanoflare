package nanoflare

import (
	"encoding/json"
	"errors"
	"sort"
)

func (s *Store) PutDeploymentArtifact(a DeploymentArtifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[a.Deployment.AppID]; !ok {
		return ErrAppNotFound
	}
	if old, ok := s.artifacts[a.Deployment.ID]; ok {
		if !sameArtifact(old, a) {
			return ErrArtifactConflict
		}
		return nil
	}
	s.artifacts[a.Deployment.ID] = cloneArtifact(a)
	return nil
}
func (s *Store) GetDeploymentArtifact(app, id string) (DeploymentArtifact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.artifacts[id]
	if !ok || a.Deployment.AppID != app {
		return DeploymentArtifact{}, ErrArtifactNotFound
	}
	return cloneArtifact(a), nil
}
func (s *Store) StoreDeployment(d Deployment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[d.AppID]; !ok {
		return ErrAppNotFound
	}
	for _, prior := range s.deployments[d.AppID] {
		if prior.ID == d.ID {
			return errors.New("deployment already exists")
		}
	}
	data, _ := json.Marshal(d)
	var copy Deployment
	_ = json.Unmarshal(data, &copy)
	copy.ObjectKey = d.ObjectKey
	copy.BundleSize = d.BundleSize
	s.deployments[d.AppID] = append(s.deployments[d.AppID], copy)
	return nil
}
func (s *Store) GetPublication(app, key string) (Publication, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.publications[app+"\x00"+key]
	p.PreviousTraffic = append([]DeploymentTraffic{}, p.PreviousTraffic...)
	p.TargetTraffic = append([]DeploymentTraffic{}, p.TargetTraffic...)
	return p, ok, nil
}
func (s *Store) SavePublication(p Publication) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[p.AppID]; !ok {
		return ErrAppNotFound
	}
	key := p.AppID + "\x00" + p.Key
	if old, ok := s.publications[key]; ok && (old.Fingerprint != p.Fingerprint || old.DeploymentID != p.DeploymentID) {
		return ErrIdempotencyConflict
	}
	p.PreviousTraffic = append([]DeploymentTraffic{}, p.PreviousTraffic...)
	p.TargetTraffic = append([]DeploymentTraffic{}, p.TargetTraffic...)
	s.publications[key] = p
	return nil
}

func (s *Store) ListPublications() ([]Publication, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Publication, 0, len(s.publications))
	for _, p := range s.publications {
		p.PreviousTraffic = append([]DeploymentTraffic{}, p.PreviousTraffic...)
		p.TargetTraffic = append([]DeploymentTraffic{}, p.TargetTraffic...)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
