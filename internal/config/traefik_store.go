package config

import (
	"sync"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type TraefikStore struct {
	mu         sync.RWMutex
	authURL    string
	authHost   string
	workerHost string
	config     []byte
}

func NewTraefikStore(authURL, workerHost string) *TraefikStore {
	store := &TraefikStore{authURL: authURL, workerHost: workerHost}
	store.WriteTraefik(nil)
	return store
}

func (s *TraefikStore) SetAuthHost(host string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authHost = host
}

func (s *TraefikStore) WriteTraefik(active []nanoflare.ActiveDeployment) error {
	s.mu.RLock()
	authHost := s.authHost
	s.mu.RUnlock()
	config := []byte(Traefik(active, s.authURL, authHost, s.workerHost))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config = config
	return nil
}

func (s *TraefikStore) TraefikConfig() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]byte(nil), s.config...)
}
