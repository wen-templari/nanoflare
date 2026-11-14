package nanoflare

import (
	"errors"
	"sort"
	"strings"
	"time"
)

// ServiceEnvironment binds an environment to an immutable ordinary Worker.
// ServiceGeneration prevents a recreated service from adopting old environments.
type ServiceEnvironment struct {
	OrgID             string    `json:"org_id"`
	ServiceID         string    `json:"service_id"`
	ServiceGeneration time.Time `json:"service_generation"`
	Name              string    `json:"name"`
	WorkerID          string    `json:"worker_id"`
}

func (s *Service) ServiceEnvironmentsForOrg(org, serviceID string) ([]ServiceEnvironment, error) {
	service, err := s.appForOrg(org, serviceID)
	if err != nil {
		return nil, err
	}
	if service.Platform != nil {
		return nil, ErrAppNotFound
	}
	items, err := s.store.ListServiceEnvironments(org, serviceID)
	if err != nil {
		return nil, err
	}
	out := []ServiceEnvironment{{OrgID: org, ServiceID: serviceID, ServiceGeneration: service.CreatedAt, Name: "production", WorkerID: serviceID}}
	for _, item := range items {
		if item.ServiceGeneration.Equal(service.CreatedAt) {
			if _, err := s.appForOrg(org, item.WorkerID); err == nil {
				out = append(out, item)
			}
		}
	}
	sort.Slice(out[1:], func(i, j int) bool { return out[i+1].Name < out[j+1].Name })
	return out, nil
}
func (s *Service) OrdinaryServiceEnvironmentForOrg(org, name, environment string) (App, error) {
	if !platformName.MatchString(environment) {
		return App{}, errors.New("invalid environment name")
	}
	service, err := s.OrdinaryScriptForOrg(org, name)
	if err != nil {
		return App{}, err
	}
	if environment == "production" {
		return service, nil
	}
	items, err := s.ServiceEnvironmentsForOrg(org, service.ID)
	if err != nil {
		return App{}, err
	}
	for _, item := range items {
		if item.Name == environment {
			return s.appForOrg(org, item.WorkerID)
		}
	}
	return App{}, ErrAppNotFound
}
func (s *Service) EnsureOrdinaryServiceEnvironmentForOrg(org, name, environment, actor string) (App, error) {
	if !platformName.MatchString(environment) {
		return App{}, errors.New("invalid environment name")
	}
	service, err := s.EnsureOrdinaryScriptForOrg(org, name, actor)
	if err != nil {
		return App{}, err
	}
	if environment == "production" {
		return service, nil
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	release, err := s.store.LockDeploymentUpdates()
	if err != nil {
		return App{}, err
	}
	defer release()
	service, err = s.OrdinaryScriptForOrg(org, name)
	if err != nil {
		return App{}, err
	}
	app, err := s.OrdinaryServiceEnvironmentForOrg(org, name, environment)
	if !errors.Is(err, ErrAppNotFound) {
		return app, err
	}
	if err := s.enforceOrgLimit(org, "worker"); err != nil {
		return App{}, err
	}
	token, err := randomToken()
	if err != nil {
		return App{}, err
	}
	id, err := randomToken()
	if err != nil {
		return App{}, err
	}
	app = App{ID: "cf-env-" + id, OrgID: org, Name: name + " (" + environment + ")", RuntimeToken: token, CreatedBy: actor, CreatedAt: time.Now().UTC()}
	relation := ServiceEnvironment{OrgID: org, ServiceID: service.ID, ServiceGeneration: service.CreatedAt, Name: environment, WorkerID: app.ID}
	if err := s.store.CreateServiceEnvironment(relation, app); err != nil {
		return App{}, err
	}
	return app, nil
}

func (s *Store) ListServiceEnvironments(org, service string) ([]ServiceEnvironment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []ServiceEnvironment{}
	for _, item := range s.serviceEnvironments {
		if item.OrgID == org && item.ServiceID == service {
			out = append(out, item)
		}
	}
	return out, nil
}
func (s *Store) CreateServiceEnvironment(item ServiceEnvironment, app App) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	service, ok := s.apps[item.ServiceID]
	if !ok || service.OrgID != item.OrgID || !service.CreatedAt.Equal(item.ServiceGeneration) {
		return ErrAppNotFound
	}
	if s.serviceEnvironments == nil {
		s.serviceEnvironments = map[string]ServiceEnvironment{}
	}
	key := item.ServiceID + "\x00" + item.Name
	if old, ok := s.serviceEnvironments[key]; ok && old.ServiceGeneration.Equal(item.ServiceGeneration) {
		if _, alive := s.apps[old.WorkerID]; alive {
			return ErrAppExists
		}
	}
	s.apps[app.ID] = app
	s.serviceEnvironments[key] = item
	return nil
}

func (s *Service) ServiceEnvironmentForWorkerForOrg(org, workerID string) (ServiceEnvironment, error) {
	app, err := s.appForOrg(org, workerID)
	if err != nil {
		return ServiceEnvironment{}, err
	}
	if app.Platform != nil {
		return ServiceEnvironment{}, ErrAppNotFound
	}
	apps, err := s.store.ListAppsByOrg(org)
	if err != nil {
		return ServiceEnvironment{}, err
	}
	for _, parent := range apps {
		items, err := s.store.ListServiceEnvironments(org, parent.ID)
		if err != nil {
			return ServiceEnvironment{}, err
		}
		for _, item := range items {
			if item.WorkerID == workerID && item.ServiceGeneration.Equal(parent.CreatedAt) {
				return item, nil
			}
		}
	}
	if strings.HasPrefix(app.ID, "cf-env-") {
		return ServiceEnvironment{}, ErrAppNotFound
	}
	return ServiceEnvironment{OrgID: org, ServiceID: app.ID, ServiceGeneration: app.CreatedAt, Name: "production", WorkerID: app.ID}, nil
}

func (s *Store) OrdinaryServiceWorkerID(org, name string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.ordinaryServices[org+"\x00"+name]
	if !ok {
		return "", ErrAppNotFound
	}
	return id, nil
}
func (s *Store) CreateOrdinaryService(org, name string, app App) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ordinaryServices == nil {
		s.ordinaryServices = map[string]string{}
	}
	key := org + "\x00" + name
	if id, ok := s.ordinaryServices[key]; ok {
		if _, alive := s.apps[id]; alive {
			return ErrAppExists
		}
	}
	for _, existing := range s.apps {
		if app.Hostname != "" && existing.Hostname == app.Hostname {
			return ErrAppExists
		}
	}
	s.apps[app.ID] = app
	s.ordinaryServices[key] = app.ID
	return nil
}
