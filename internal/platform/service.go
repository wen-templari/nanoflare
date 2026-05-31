package platform

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

var appIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type ConfigWriter interface {
	Write([]ActiveDeployment) error
}

type Service struct {
	store       *Store
	writer      ConfigWriter
	deployments atomic.Uint64
}

func NewService(store *Store, writer ConfigWriter) *Service {
	return &Service{store: store, writer: writer}
}

func (s *Service) CreateApp(input CreateAppInput) (App, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.Hostname = strings.TrimSpace(strings.ToLower(input.Hostname))
	if !appIDPattern.MatchString(input.ID) {
		return App{}, errors.New("id must start with a letter and contain only lowercase letters, numbers, or hyphens")
	}
	if input.Hostname == "" || strings.Contains(input.Hostname, ":") || net.ParseIP(input.Hostname) != nil {
		return App{}, errors.New("hostname must be a DNS name without a port")
	}
	app := App{ID: input.ID, Hostname: input.Hostname, CreatedAt: time.Now().UTC()}
	return app, s.store.CreateApp(app)
}

func (s *Service) ListApps() []App {
	return s.store.ListApps()
}

func (s *Service) Deploy(appID string, input DeployInput) (Deployment, error) {
	if strings.TrimSpace(input.BundlePath) == "" {
		return Deployment{}, errors.New("bundle_path is required")
	}
	if _, err := time.Parse("2006-01-02", input.CompatibilityDate); err != nil {
		return Deployment{}, errors.New("compatibility_date must use YYYY-MM-DD")
	}
	n := s.deployments.Add(1)
	capability, err := randomToken()
	if err != nil {
		return Deployment{}, err
	}
	deployment := Deployment{
		ID:                fmt.Sprintf("deployment-%06d", n),
		AppID:             appID,
		BundlePath:        input.BundlePath,
		CompatibilityDate: input.CompatibilityDate,
		Port:              9000 + int(n),
		CapabilityToken:   capability,
		CreatedAt:         time.Now().UTC(),
	}
	if err := s.store.Activate(deployment); err != nil {
		return Deployment{}, err
	}
	if err := s.writer.Write(s.store.ActiveDeployments()); err != nil {
		return Deployment{}, fmt.Errorf("write generated config: %w", err)
	}
	return deployment, nil
}

func (s *Service) KVGet(capability, key string) ([]byte, bool, error) {
	return s.store.KVGet(capability, key)
}

func (s *Service) KVPut(capability, key string, value []byte) error {
	return s.store.KVPut(capability, key, value)
}

func (s *Service) KVDelete(capability, key string) error {
	return s.store.KVDelete(capability, key)
}

func randomToken() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
