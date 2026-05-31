package platform

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

var appIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type ConfigWriter interface {
	Write([]ActiveDeployment) error
}

type ObjectStore interface {
	PresignUpload(appID, path string, expiry time.Duration) (string, error)
	PresignDownload(appID, path string, expiry time.Duration) (string, error)
	Delete(appID, path string) error
}

type Service struct {
	store   Repository
	writer  ConfigWriter
	objects ObjectStore
}

func NewService(store Repository, writer ConfigWriter) *Service {
	return &Service{store: store, writer: writer}
}

func NewServiceWithObjects(store Repository, writer ConfigWriter, objects ObjectStore) *Service {
	return &Service{store: store, writer: writer, objects: objects}
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

func (s *Service) ListApps() ([]App, error) {
	return s.store.ListApps()
}

func (s *Service) Deploy(appID string, input DeployInput) (Deployment, error) {
	if strings.TrimSpace(input.BundlePath) == "" {
		return Deployment{}, errors.New("bundle_path is required")
	}
	if _, err := time.Parse("2006-01-02", input.CompatibilityDate); err != nil {
		return Deployment{}, errors.New("compatibility_date must use YYYY-MM-DD")
	}
	capability, err := randomToken()
	if err != nil {
		return Deployment{}, err
	}
	port, err := s.store.NextPort()
	if err != nil {
		return Deployment{}, err
	}
	deploymentID, err := randomToken()
	if err != nil {
		return Deployment{}, err
	}
	deployment := Deployment{
		ID:                "deployment-" + deploymentID[:16],
		AppID:             appID,
		BundlePath:        input.BundlePath,
		CompatibilityDate: input.CompatibilityDate,
		Port:              port,
		CapabilityToken:   capability,
		CreatedAt:         time.Now().UTC(),
	}
	if err := s.store.Activate(deployment); err != nil {
		return Deployment{}, err
	}
	active, err := s.store.ActiveDeployments()
	if err != nil {
		return Deployment{}, err
	}
	if err := s.writer.Write(active); err != nil {
		return Deployment{}, fmt.Errorf("write generated config: %w", err)
	}
	return deployment, nil
}

func (s *Service) PresignUpload(capability, path string) (string, error) {
	appID, err := s.appIDForCapability(capability)
	if err != nil {
		return "", err
	}
	return s.objects.PresignUpload(appID, path, 15*time.Minute)
}

func (s *Service) PresignDownload(capability, path string) (string, error) {
	appID, err := s.appIDForCapability(capability)
	if err != nil {
		return "", err
	}
	return s.objects.PresignDownload(appID, path, 15*time.Minute)
}

func (s *Service) DeleteObject(capability, path string) error {
	appID, err := s.appIDForCapability(capability)
	if err != nil {
		return err
	}
	return s.objects.Delete(appID, path)
}

func (s *Service) appIDForCapability(capability string) (string, error) {
	if s.objects == nil {
		return "", errors.New("object storage is not configured")
	}
	return s.store.AppIDForCapability(capability)
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
