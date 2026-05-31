package platform

import (
	"errors"
	"sort"
	"sync"
)

var (
	ErrAppExists         = errors.New("app already exists")
	ErrAppNotFound       = errors.New("app not found")
	ErrInvalidCapability = errors.New("invalid runtime capability")
)

type Repository interface {
	CreateApp(App) error
	ListApps() ([]App, error)
	NextPort() (int, error)
	Activate(Deployment) error
	ActiveDeployments() ([]ActiveDeployment, error)
	AppIDForCapability(string) (string, error)
	KVGet(capability, key string) ([]byte, bool, error)
	KVPut(capability, key string, value []byte) error
	KVDelete(capability, key string) error
}

type Store struct {
	mu              sync.RWMutex
	apps            map[string]App
	deployments     map[string][]Deployment
	active          map[string]string
	capabilityToApp map[string]string
	kv              map[string]map[string][]byte
}

func NewStore() *Store {
	return &Store{
		apps:            make(map[string]App),
		deployments:     make(map[string][]Deployment),
		active:          make(map[string]string),
		capabilityToApp: make(map[string]string),
		kv:              make(map[string]map[string][]byte),
	}
}

func (s *Store) CreateApp(app App) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.apps[app.ID]; exists {
		return ErrAppExists
	}
	s.apps[app.ID] = app
	return nil
}

func (s *Store) ListApps() ([]App, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	apps := make([]App, 0, len(s.apps))
	for _, app := range s.apps {
		apps = append(apps, app)
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].ID < apps[j].ID })
	return apps, nil
}

func (s *Store) NextPort() (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	port := 9001
	for _, deployments := range s.deployments {
		for _, deployment := range deployments {
			if deployment.Port >= port {
				port = deployment.Port + 1
			}
		}
	}
	return port, nil
}

func (s *Store) Activate(deployment Deployment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.apps[deployment.AppID]; !exists {
		return ErrAppNotFound
	}
	for _, previous := range s.deployments[deployment.AppID] {
		if previous.ID == s.active[deployment.AppID] {
			delete(s.capabilityToApp, previous.CapabilityToken)
			break
		}
	}
	s.deployments[deployment.AppID] = append(s.deployments[deployment.AppID], deployment)
	s.active[deployment.AppID] = deployment.ID
	s.capabilityToApp[deployment.CapabilityToken] = deployment.AppID
	return nil
}

func (s *Store) ActiveDeployments() ([]ActiveDeployment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	active := make([]ActiveDeployment, 0, len(s.active))
	for appID, deploymentID := range s.active {
		for _, deployment := range s.deployments[appID] {
			if deployment.ID == deploymentID {
				active = append(active, ActiveDeployment{App: s.apps[appID], Deployment: deployment})
				break
			}
		}
	}
	sort.Slice(active, func(i, j int) bool { return active[i].App.ID < active[j].App.ID })
	return active, nil
}

func (s *Store) AppIDForCapability(capability string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	appID, ok := s.capabilityToApp[capability]
	if !ok {
		return "", ErrInvalidCapability
	}
	return appID, nil
}

func (s *Store) KVGet(capability, key string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	appID, ok := s.capabilityToApp[capability]
	if !ok {
		return nil, false, ErrInvalidCapability
	}
	value, ok := s.kv[appID][key]
	return append([]byte(nil), value...), ok, nil
}

func (s *Store) KVPut(capability, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	appID, ok := s.capabilityToApp[capability]
	if !ok {
		return ErrInvalidCapability
	}
	if s.kv[appID] == nil {
		s.kv[appID] = make(map[string][]byte)
	}
	s.kv[appID][key] = append([]byte(nil), value...)
	return nil
}

func (s *Store) KVDelete(capability, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	appID, ok := s.capabilityToApp[capability]
	if !ok {
		return ErrInvalidCapability
	}
	delete(s.kv[appID], key)
	return nil
}
