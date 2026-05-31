package platform

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"path"
	"strings"
	"time"
)

type ConfigWriter interface {
	Write([]ActiveDeployment) error
}

type ObjectStore interface {
	PresignUpload(appID, path string, expiry time.Duration) (string, error)
	PresignDownload(appID, path string, expiry time.Duration) (string, error)
	Delete(appID, path string) error
}

type WorkerOutputReader interface {
	Output(string) []WorkerOutputLine
}

type WorkerTrafficReader interface {
	Traffic(string) (WorkerTraffic, error)
}

type Service struct {
	store   Repository
	writer  ConfigWriter
	objects ObjectStore
	output  WorkerOutputReader
	traffic WorkerTrafficReader
}

func NewService(store Repository, writer ConfigWriter) *Service {
	return &Service{store: store, writer: writer}
}

func NewServiceWithObjects(store Repository, writer ConfigWriter, objects ObjectStore) *Service {
	return &Service{store: store, writer: writer, objects: objects}
}

func NewServiceWithConsole(store Repository, writer ConfigWriter, objects ObjectStore, output WorkerOutputReader, traffic WorkerTrafficReader) *Service {
	return &Service{store: store, writer: writer, objects: objects, output: output, traffic: traffic}
}

func (s *Service) CreateApp(input CreateAppInput) (App, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Hostname = strings.TrimSpace(strings.ToLower(input.Hostname))
	if input.Name == "" {
		return App{}, errors.New("name is required")
	}
	if input.Hostname == "" || strings.Contains(input.Hostname, ":") || net.ParseIP(input.Hostname) != nil {
		return App{}, errors.New("hostname must be a DNS name without a port")
	}
	appID, err := randomToken()
	if err != nil {
		return App{}, err
	}
	runtimeToken, err := randomToken()
	if err != nil {
		return App{}, err
	}
	app := App{ID: appID, Name: input.Name, Hostname: input.Hostname, RuntimeToken: runtimeToken, CreatedAt: time.Now().UTC()}
	return app, s.store.CreateApp(app)
}

func (s *Service) ListApps() ([]App, error) {
	return s.store.ListApps()
}

func (s *Service) WorkerDetail(appID string) (WorkerDetail, error) {
	app, active, err := s.worker(appID)
	if err != nil {
		return WorkerDetail{}, err
	}
	detail := WorkerDetail{App: app}
	if active == nil {
		return detail, nil
	}
	var size int64
	for _, file := range active.Deployment.Files {
		size += file.Size
	}
	detail.Deployment = &WorkerDeployment{
		ID:                active.Deployment.ID,
		Entrypoint:        active.Deployment.Entrypoint,
		Format:            active.Deployment.Format,
		BundleSize:        size,
		CompatibilityDate: active.Deployment.CompatibilityDate,
		Port:              active.Deployment.Port,
		CreatedAt:         active.Deployment.CreatedAt,
	}
	return detail, nil
}

func (s *Service) WorkerDeployments(appID string) ([]ConsoleDeployment, error) {
	if _, _, err := s.worker(appID); err != nil {
		return nil, err
	}
	records, err := s.store.ListDeployments()
	if err != nil {
		return nil, err
	}
	deployments := make([]ConsoleDeployment, 0, len(records))
	for _, record := range records {
		if record.App.ID != appID {
			continue
		}
		var size int64
		for _, file := range record.Deployment.Files {
			size += file.Size
		}
		state := "inactive"
		if record.Active {
			state = "active"
		}
		deployments = append(deployments, ConsoleDeployment{
			ID:                record.Deployment.ID,
			AppID:             record.App.ID,
			AppName:           record.App.Name,
			Hostname:          record.App.Hostname,
			Entrypoint:        record.Deployment.Entrypoint,
			Format:            record.Deployment.Format,
			BundleSize:        size,
			CompatibilityDate: record.Deployment.CompatibilityDate,
			State:             state,
			CreatedAt:         record.Deployment.CreatedAt,
		})
	}
	return deployments, nil
}

func (s *Service) WorkerFiles(appID string) ([]WorkerFile, error) {
	_, active, err := s.worker(appID)
	if err != nil {
		return nil, err
	}
	if active == nil {
		return []WorkerFile{}, nil
	}
	return append([]WorkerFile(nil), active.Deployment.Files...), nil
}

func (s *Service) WorkerOutput(appID string) ([]WorkerOutputLine, error) {
	if _, _, err := s.worker(appID); err != nil {
		return nil, err
	}
	if s.output == nil {
		return []WorkerOutputLine{}, nil
	}
	return s.output.Output(appID), nil
}

func (s *Service) WorkerTraffic(appID string) (WorkerTraffic, error) {
	if _, _, err := s.worker(appID); err != nil {
		return WorkerTraffic{}, err
	}
	if s.traffic == nil {
		return WorkerTraffic{}, nil
	}
	return s.traffic.Traffic(appID)
}

func (s *Service) worker(appID string) (App, *ActiveDeployment, error) {
	apps, err := s.store.ListApps()
	if err != nil {
		return App{}, nil, err
	}
	var app *App
	for i := range apps {
		if apps[i].ID == appID {
			app = &apps[i]
			break
		}
	}
	if app == nil {
		return App{}, nil, ErrAppNotFound
	}
	active, err := s.store.ActiveDeployments()
	if err != nil {
		return App{}, nil, err
	}
	for i := range active {
		if active[i].App.ID == appID {
			return *app, &active[i], nil
		}
	}
	return *app, nil, nil
}

func (s *Service) Deploy(appID string, input DeployInput) (Deployment, error) {
	files, entrypoint, err := deploymentFiles(input.Files, input.Entrypoint)
	if err != nil {
		return Deployment{}, err
	}
	format, err := workerFormat(input.Format, len(files))
	if err != nil {
		return Deployment{}, err
	}
	if _, err := time.Parse("2006-01-02", input.CompatibilityDate); err != nil {
		return Deployment{}, errors.New("compatibility_date must use YYYY-MM-DD")
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
		ID:                deploymentID,
		AppID:             appID,
		Files:             files,
		Entrypoint:        entrypoint,
		Format:            format,
		CompatibilityDate: input.CompatibilityDate,
		Port:              port,
		CreatedAt:         time.Now().UTC(),
	}
	activeBefore, err := s.store.ActiveDeployments()
	if err != nil {
		return Deployment{}, err
	}
	previousID := activeDeploymentID(activeBefore, appID)
	if err := s.store.Activate(deployment); err != nil {
		return Deployment{}, err
	}
	active, err := s.store.ActiveDeployments()
	if err != nil {
		if rollbackErr := s.store.SetActive(appID, previousID); rollbackErr != nil {
			return Deployment{}, fmt.Errorf("list active deployments: %w; rollback active deployment: %v", err, rollbackErr)
		}
		return Deployment{}, err
	}
	if err := s.writer.Write(active); err != nil {
		if rollbackErr := s.store.SetActive(appID, previousID); rollbackErr != nil {
			return Deployment{}, fmt.Errorf("write generated config: %w; rollback active deployment: %v", err, rollbackErr)
		}
		return Deployment{}, fmt.Errorf("write generated config: %w", err)
	}
	return deployment, nil
}

func workerFormat(format string, fileCount int) (string, error) {
	switch strings.TrimSpace(format) {
	case "":
		if fileCount == 1 {
			return "service-worker", nil
		}
		return "modules", nil
	case "modules", "service-worker":
		return format, nil
	default:
		return "", errors.New(`format must be "modules" or "service-worker"`)
	}
}

func deploymentFiles(files []WorkerFile, entrypoint string) ([]WorkerFile, string, error) {
	const maxBundleSize = 1 << 20
	if len(files) == 0 {
		return nil, "", errors.New("files are required")
	}
	result := make([]WorkerFile, 0, len(files))
	seen := make(map[string]bool, len(files))
	totalSize := 0
	for _, file := range files {
		file.Path = path.Clean(strings.TrimSpace(file.Path))
		if file.Path == "." || strings.HasPrefix(file.Path, "/") || strings.HasPrefix(file.Path, "../") {
			return nil, "", errors.New("file paths must be relative and remain inside the worker")
		}
		if seen[file.Path] {
			return nil, "", fmt.Errorf("duplicate worker file path %q", file.Path)
		}
		seen[file.Path] = true
		file.Name = path.Base(file.Path)
		file.Size = int64(len(file.Content))
		totalSize += len(file.Content)
		if totalSize > maxBundleSize {
			return nil, "", fmt.Errorf("worker files exceed %d byte limit", maxBundleSize)
		}
		result = append(result, file)
	}
	entrypoint = path.Clean(strings.TrimSpace(entrypoint))
	if entrypoint == "." {
		entrypoint = result[0].Path
	}
	if !seen[entrypoint] {
		return nil, "", errors.New("entrypoint must name a deployed worker file")
	}
	return result, entrypoint, nil
}

func activeDeploymentID(active []ActiveDeployment, appID string) string {
	for _, item := range active {
		if item.App.ID == appID {
			return item.Deployment.ID
		}
	}
	return ""
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
