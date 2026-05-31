package config

import (
	"sync"

	"github.com/clas/platform/internal/platform"
)

type TraefikStore struct {
	mu         sync.RWMutex
	authURL    string
	workerHost string
	config     []byte
}

func NewTraefikStore(authURL, workerHost string) *TraefikStore {
	store := &TraefikStore{authURL: authURL, workerHost: workerHost}
	store.WriteTraefik(nil)
	return store
}

func (s *TraefikStore) WriteTraefik(active []platform.ActiveDeployment) error {
	config := []byte(Traefik(active, s.authURL, s.workerHost))
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
