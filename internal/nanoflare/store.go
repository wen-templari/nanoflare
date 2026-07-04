package nanoflare

import (
	"errors"
	"sort"
	"sync"
)

var (
	ErrAppExists           = errors.New("app already exists")
	ErrAppNotFound         = errors.New("app not found")
	ErrInvalidCapability   = errors.New("invalid runtime capability")
	ErrObjectNotFound      = errors.New("object not found")
	ErrKVNamespaceExists   = errors.New("kv namespace already exists")
	ErrKVNamespaceNotFound = errors.New("kv namespace not found")
	ErrKVNamespaceInUse    = errors.New("kv namespace is still referenced by a deployment")
	ErrKVNamespaceNotBound = errors.New("kv namespace is not bound by the app's active deployment")
)

type Repository interface {
	CreateApp(App) error
	ListApps() ([]App, error)
	UpdateApp(App) error
	DeleteApp(string) error
	CreateKVNamespace(KVNamespace) error
	ListKVNamespaces() ([]KVNamespace, error)
	GetKVNamespace(string) (KVNamespace, error)
	DeleteKVNamespace(string) error
	NextPort() (int, error)
	Activate(Deployment) error
	DeleteDeployment(id string) error
	SetActive(appID, deploymentID string) error
	ActiveDeployments() ([]ActiveDeployment, error)
	ListDeployments() ([]DeploymentRecord, error)
	AppIDForCapability(string) (string, error)
	KVList(capability, namespaceID string) ([]WorkerKVKey, error)
	KVGet(capability, namespaceID, key string) ([]byte, bool, error)
	KVPut(capability, namespaceID, key string, value []byte) error
	KVDelete(capability, namespaceID, key string) error
}

type Store struct {
	mu              sync.RWMutex
	apps            map[string]App
	kvNamespaces    map[string]KVNamespace
	deployments     map[string][]Deployment
	active          map[string]string
	capabilityToApp map[string]string
	kv              map[string]map[string][]byte
}

func NewStore() *Store {
	return &Store{
		apps:            make(map[string]App),
		kvNamespaces:    make(map[string]KVNamespace),
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
	for _, existing := range s.apps {
		if existing.Hostname == app.Hostname {
			return ErrAppExists
		}
	}
	s.apps[app.ID] = app
	s.capabilityToApp[app.RuntimeToken] = app.ID
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

func (s *Store) UpdateApp(app App) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, exists := s.apps[app.ID]
	if !exists {
		return ErrAppNotFound
	}
	for _, candidate := range s.apps {
		if candidate.ID != app.ID && candidate.Hostname == app.Hostname {
			return ErrAppExists
		}
	}
	app.RuntimeToken = existing.RuntimeToken
	app.CreatedAt = existing.CreatedAt
	s.apps[app.ID] = app
	return nil
}

func (s *Store) DeleteApp(appID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	app, exists := s.apps[appID]
	if !exists {
		return ErrAppNotFound
	}
	delete(s.apps, appID)
	delete(s.deployments, appID)
	delete(s.active, appID)
	delete(s.capabilityToApp, app.RuntimeToken)
	return nil
}

func (s *Store) CreateKVNamespace(namespace KVNamespace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.kvNamespaces[namespace.ID]; exists {
		return ErrKVNamespaceExists
	}
	for _, existing := range s.kvNamespaces {
		if existing.Name == namespace.Name {
			return ErrKVNamespaceExists
		}
	}
	s.kvNamespaces[namespace.ID] = namespace
	return nil
}

func (s *Store) ListKVNamespaces() ([]KVNamespace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	namespaces := make([]KVNamespace, 0, len(s.kvNamespaces))
	for _, namespace := range s.kvNamespaces {
		namespaces = append(namespaces, namespace)
	}
	sort.Slice(namespaces, func(i, j int) bool { return namespaces[i].Name < namespaces[j].Name })
	return namespaces, nil
}

func (s *Store) GetKVNamespace(namespaceID string) (KVNamespace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	namespace, ok := s.kvNamespaces[namespaceID]
	if !ok {
		return KVNamespace{}, ErrKVNamespaceNotFound
	}
	return namespace, nil
}

func (s *Store) DeleteKVNamespace(namespaceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.kvNamespaces[namespaceID]; !ok {
		return ErrKVNamespaceNotFound
	}
	for _, deployments := range s.deployments {
		for _, deployment := range deployments {
			for _, binding := range deployment.KVNamespaces {
				if binding.ID == namespaceID {
					return ErrKVNamespaceInUse
				}
			}
		}
	}
	delete(s.kvNamespaces, namespaceID)
	delete(s.kv, namespaceID)
	return nil
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
	s.deployments[deployment.AppID] = append(s.deployments[deployment.AppID], deployment)
	s.active[deployment.AppID] = deployment.ID
	return nil
}

func (s *Store) SetActive(appID, deploymentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.apps[appID]; !exists {
		return ErrAppNotFound
	}
	for _, deployment := range s.deployments[appID] {
		if deployment.ID == deploymentID {
			s.active[appID] = deployment.ID
			return nil
		}
	}
	if deploymentID == "" {
		delete(s.active, appID)
		return nil
	}
	return errors.New("deployment not found")
}

func (s *Store) DeleteDeployment(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for appID, deployments := range s.deployments {
		for i, deployment := range deployments {
			if deployment.ID != id {
				continue
			}
			s.deployments[appID] = append(deployments[:i], deployments[i+1:]...)
			if s.active[appID] == id {
				delete(s.active, appID)
			}
			return nil
		}
	}
	return errors.New("deployment not found")
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

func (s *Store) ListDeployments() ([]DeploymentRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var records []DeploymentRecord
	for appID, deployments := range s.deployments {
		for _, deployment := range deployments {
			records = append(records, DeploymentRecord{
				App:        s.apps[appID],
				Deployment: deployment,
				Active:     deployment.ID == s.active[appID],
			})
		}
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Deployment.CreatedAt.After(records[j].Deployment.CreatedAt)
	})
	return records, nil
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

func (s *Store) KVGet(capability, namespaceID, key string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.capabilityToApp[capability]; !ok {
		return nil, false, ErrInvalidCapability
	}
	if _, ok := s.kvNamespaces[namespaceID]; !ok {
		return nil, false, ErrKVNamespaceNotFound
	}
	value, ok := s.kv[namespaceID][key]
	return append([]byte(nil), value...), ok, nil
}

func (s *Store) KVList(capability, namespaceID string) ([]WorkerKVKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.capabilityToApp[capability]; !ok {
		return nil, ErrInvalidCapability
	}
	if _, ok := s.kvNamespaces[namespaceID]; !ok {
		return nil, ErrKVNamespaceNotFound
	}
	items := make([]WorkerKVKey, 0, len(s.kv[namespaceID]))
	for key, value := range s.kv[namespaceID] {
		items = append(items, WorkerKVKey{Key: key, Size: int64(len(value))})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items, nil
}

func (s *Store) KVPut(capability, namespaceID, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.capabilityToApp[capability]; !ok {
		return ErrInvalidCapability
	}
	if _, ok := s.kvNamespaces[namespaceID]; !ok {
		return ErrKVNamespaceNotFound
	}
	if s.kv[namespaceID] == nil {
		s.kv[namespaceID] = make(map[string][]byte)
	}
	s.kv[namespaceID][key] = append([]byte(nil), value...)
	return nil
}

func (s *Store) KVDelete(capability, namespaceID, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.capabilityToApp[capability]; !ok {
		return ErrInvalidCapability
	}
	if _, ok := s.kvNamespaces[namespaceID]; !ok {
		return ErrKVNamespaceNotFound
	}
	delete(s.kv[namespaceID], key)
	return nil
}
