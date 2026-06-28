package platform

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
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
	Put(appID, path string, contentType string, data []byte) (ObjectInfo, error)
	Get(appID, path string) (ObjectBody, error)
	Head(appID, path string) (ObjectInfo, error)
	Delete(appID, path string) error
}

type WorkerOutputReader interface {
	Output(string) []WorkerOutputLine
}

type WorkerTrafficReader interface {
	Traffic(string) (WorkerTraffic, error)
}

type Service struct {
	store                Repository
	writer               ConfigWriter
	objects              ObjectStore
	output               WorkerOutputReader
	traffic              WorkerTrafficReader
	baseHostname         string
	randomHostnameSuffix func() (string, error)
}

type AssetResponse struct {
	Body        []byte
	ContentType string
	StatusCode  int
}

func NewService(store Repository, writer ConfigWriter) *Service {
	return &Service{store: store, writer: writer, randomHostnameSuffix: randomHostnameSuffix}
}

func NewServiceWithObjects(store Repository, writer ConfigWriter, objects ObjectStore) *Service {
	return &Service{store: store, writer: writer, objects: objects, randomHostnameSuffix: randomHostnameSuffix}
}

func NewServiceWithConsole(store Repository, writer ConfigWriter, objects ObjectStore, output WorkerOutputReader, traffic WorkerTrafficReader) *Service {
	return &Service{store: store, writer: writer, objects: objects, output: output, traffic: traffic, randomHostnameSuffix: randomHostnameSuffix}
}

func (s *Service) SetBaseHostname(hostname string) error {
	normalized, err := normalizeHostname(hostname)
	if err != nil {
		return err
	}
	s.baseHostname = normalized
	return nil
}

func (s *Service) CreateApp(input CreateAppInput) (App, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Hostname = strings.TrimSpace(strings.ToLower(input.Hostname))
	if input.Name == "" {
		return App{}, errors.New("name is required")
	}
	auth, err := normalizeAuthConfig(input.Auth)
	if err != nil {
		return App{}, err
	}
	generated := input.Hostname == ""
	if !generated {
		hostname, err := normalizeHostname(input.Hostname)
		if err != nil {
			return App{}, err
		}
		input.Hostname = hostname
	}
	attempts := 1
	if generated {
		if s.baseHostname == "" {
			return App{}, errors.New("hostname is required when base hostname is not configured")
		}
		attempts = 5
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		hostname := input.Hostname
		if generated {
			var err error
			hostname, err = s.generatedHostname(input.Name)
			if err != nil {
				return App{}, err
			}
		}
		appID, err := randomToken()
		if err != nil {
			return App{}, err
		}
		runtimeToken, err := randomToken()
		if err != nil {
			return App{}, err
		}
		app := App{ID: appID, Name: input.Name, Hostname: hostname, Auth: auth, RuntimeToken: runtimeToken, CreatedAt: time.Now().UTC()}
		if err := s.store.CreateApp(app); err != nil {
			if generated && errors.Is(err, ErrAppExists) {
				lastErr = err
				continue
			}
			return App{}, err
		}
		return app, nil
	}
	if lastErr != nil {
		return App{}, errors.New("could not generate unique hostname")
	}
	return App{}, errors.New("could not create app")
}

func (s *Service) UpdateApp(appID string, input UpdateAppInput) (App, error) {
	app, _, err := s.worker(appID)
	if err != nil {
		return App{}, err
	}
	if input.Auth != nil {
		auth, err := normalizeAuthConfig(*input.Auth)
		if err != nil {
			return App{}, err
		}
		app.Auth = auth
	}
	if err := s.store.UpdateApp(app); err != nil {
		return App{}, err
	}
	active, err := s.activeDeployments()
	if err != nil {
		return App{}, err
	}
	if err := s.writer.Write(active); err != nil {
		return App{}, fmt.Errorf("write generated config: %w", err)
	}
	return app, nil
}

func (s *Service) ListApps() ([]App, error) {
	return s.store.ListApps()
}

func (s *Service) DeleteApp(appID string) error {
	records, err := s.store.ListDeployments()
	if err != nil {
		return err
	}
	apps, err := s.store.ListApps()
	if err != nil {
		return err
	}
	exists := false
	for _, app := range apps {
		if app.ID == appID {
			exists = true
			break
		}
	}
	if !exists {
		return ErrAppNotFound
	}
	for _, record := range records {
		if record.App.ID != appID {
			continue
		}
		s.cleanupDeploymentObject(record.Deployment)
		s.cleanupDeploymentAssets(record.Deployment)
	}
	if err := s.store.DeleteApp(appID); err != nil {
		return err
	}
	active, err := s.activeDeployments()
	if err != nil {
		return err
	}
	if err := s.writer.Write(active); err != nil {
		return fmt.Errorf("write generated config: %w", err)
	}
	return nil
}

func (s *Service) ActiveDeployments() ([]ActiveDeployment, error) {
	return s.activeDeployments()
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
	detail.Deployment = &WorkerDeployment{
		ID:                active.Deployment.ID,
		Entrypoint:        active.Deployment.Entrypoint,
		Format:            active.Deployment.Format,
		BundleSize:        active.Deployment.BundleSize,
		AssetCount:        len(active.Deployment.Assets),
		CompatibilityDate: active.Deployment.CompatibilityDate,
		AssetConfig:       active.Deployment.AssetConfig,
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
			BundleSize:        record.Deployment.BundleSize,
			AssetCount:        len(record.Deployment.Assets),
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
	active, err := s.activeDeployments()
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
	assets, err := deploymentAssets(input.Assets, input.AssetConfig)
	if err != nil {
		return Deployment{}, err
	}
	assetConfig, err := normalizeAssetConfig(input.AssetConfig)
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
		Assets:            assets,
		Entrypoint:        entrypoint,
		Format:            format,
		CompatibilityDate: input.CompatibilityDate,
		AssetConfig:       assetConfig,
		BundleSize:        bundleSize(files),
		Port:              port,
		CreatedAt:         time.Now().UTC(),
	}
	if len(deployment.Assets) > 0 && s.objects == nil {
		return Deployment{}, errors.New("object storage is not configured")
	}
	if s.objects != nil {
		payload, err := json.Marshal(files)
		if err != nil {
			return Deployment{}, err
		}
		deployment.ObjectKey = deploymentBundleObjectPath(deploymentID)
		if _, err := s.objects.Put(appID, deployment.ObjectKey, "application/json", payload); err != nil {
			return Deployment{}, err
		}
		for i := range deployment.Assets {
			deployment.Assets[i].ObjectKey = deploymentAssetObjectPath(deploymentID, deployment.Assets[i].Path)
			if _, err := s.objects.Put(appID, deployment.Assets[i].ObjectKey, deployment.Assets[i].ContentType, deployment.Assets[i].Data); err != nil {
				s.cleanupDeploymentAssets(deployment)
				s.cleanupDeploymentObject(deployment)
				return Deployment{}, err
			}
			deployment.Assets[i].Data = nil
		}
	}
	activeBefore, err := s.activeDeployments()
	if err != nil {
		s.cleanupDeploymentObject(deployment)
		return Deployment{}, err
	}
	previousID := activeDeploymentID(activeBefore, appID)
	if err := s.store.Activate(deployment); err != nil {
		s.cleanupDeploymentObject(deployment)
		return Deployment{}, err
	}
	active, err := s.activeDeployments()
	if err != nil {
		if rollbackErr := s.rollbackDeployment(appID, previousID, deployment); rollbackErr != nil {
			return Deployment{}, fmt.Errorf("list active deployments: %w; rollback active deployment: %v", err, rollbackErr)
		}
		return Deployment{}, err
	}
	if err := s.writer.Write(active); err != nil {
		if rollbackErr := s.rollbackDeployment(appID, previousID, deployment); rollbackErr != nil {
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

func deploymentAssets(files []AssetFile, config AssetConfig) ([]AssetFile, error) {
	if len(files) == 0 {
		return nil, nil
	}
	result := make([]AssetFile, 0, len(files))
	seen := make(map[string]bool, len(files))
	for _, file := range files {
		file.Path = path.Clean(strings.TrimSpace(file.Path))
		if file.Path == "." || strings.HasPrefix(file.Path, "/") || strings.HasPrefix(file.Path, "../") {
			return nil, errors.New("asset paths must be relative and remain inside the asset directory")
		}
		if seen[file.Path] {
			return nil, fmt.Errorf("duplicate asset path %q", file.Path)
		}
		seen[file.Path] = true
		file.Size = int64(len(file.Data))
		if file.Size == 0 {
			return nil, fmt.Errorf("asset %q is empty", file.Path)
		}
		if file.ContentType == "" {
			file.ContentType = detectAssetContentType(file.Path)
		}
		result = append(result, file)
	}
	if len(result) > 0 && strings.TrimSpace(config.HTMLHandling) == "" {
		config.HTMLHandling = "auto-trailing-slash"
	}
	return result, nil
}

func normalizeAssetConfig(config AssetConfig) (AssetConfig, error) {
	config.Binding = strings.TrimSpace(config.Binding)
	if config.Binding == "" {
		config.Binding = "ASSETS"
	}
	if config.HTMLHandling == "" {
		config.HTMLHandling = "auto-trailing-slash"
	}
	switch config.HTMLHandling {
	case "none", "auto-trailing-slash":
	default:
		return AssetConfig{}, errors.New(`asset_config.html_handling must be "none" or "auto-trailing-slash"`)
	}
	if config.NotFoundHandling == "" {
		config.NotFoundHandling = "404-page"
	}
	switch config.NotFoundHandling {
	case "none", "404-page", "single-page-application":
	default:
		return AssetConfig{}, errors.New(`asset_config.not_found_handling must be "none", "404-page", or "single-page-application"`)
	}
	if err := validateRunWorkerFirst(config.RunWorkerFirst); err != nil {
		return AssetConfig{}, err
	}
	return config, nil
}

func validateRunWorkerFirst(runWorkerFirst RunWorkerFirst) error {
	if runWorkerFirst.Always() {
		return nil
	}
	for _, route := range runWorkerFirst.Routes() {
		pattern := strings.TrimPrefix(route, "!")
		if !strings.HasPrefix(pattern, "/") || pattern == "/" || strings.Contains(strings.TrimSuffix(pattern, "*"), "*") {
			return fmt.Errorf("asset_config.run_worker_first route %q must be an absolute path with an optional trailing wildcard", route)
		}
	}
	return nil
}

func normalizeAuthConfig(config AuthConfig) (AuthConfig, error) {
	if len(config.ProtectedRoutes) == 0 {
		return AuthConfig{}, nil
	}
	routes := make([]string, 0, len(config.ProtectedRoutes))
	seen := make(map[string]bool, len(config.ProtectedRoutes))
	for _, route := range config.ProtectedRoutes {
		route = strings.TrimSpace(route)
		if err := validateProtectedRoute(route); err != nil {
			return AuthConfig{}, err
		}
		if seen[route] {
			continue
		}
		seen[route] = true
		routes = append(routes, route)
	}
	return AuthConfig{ProtectedRoutes: routes}, nil
}

func validateProtectedRoute(route string) error {
	if route == "" {
		return errors.New("auth.protected_routes cannot contain empty values")
	}
	if !strings.HasPrefix(route, "/") || route == "/" {
		return fmt.Errorf("auth.protected_routes route %q must be an absolute path and cannot be root", route)
	}
	if strings.Contains(strings.TrimSuffix(route, "*"), "*") {
		return fmt.Errorf("auth.protected_routes route %q must use at most one trailing wildcard", route)
	}
	if strings.HasSuffix(route, "*") && !strings.HasSuffix(route, "/*") {
		return fmt.Errorf("auth.protected_routes route %q wildcard must be written as /*", route)
	}
	return nil
}

func detectAssetContentType(assetPath string) string {
	if value := mime.TypeByExtension(strings.ToLower(path.Ext(assetPath))); value != "" {
		return value
	}
	return "application/octet-stream"
}

func activeDeploymentID(active []ActiveDeployment, appID string) string {
	for _, item := range active {
		if item.App.ID == appID {
			return item.Deployment.ID
		}
	}
	return ""
}

func (s *Service) activeDeployments() ([]ActiveDeployment, error) {
	active, err := s.store.ActiveDeployments()
	if err != nil {
		return nil, err
	}
	for i := range active {
		if err := s.hydrateDeploymentFiles(&active[i].Deployment); err != nil {
			return nil, err
		}
	}
	return active, nil
}

func (s *Service) hydrateDeploymentFiles(deployment *Deployment) error {
	if len(deployment.Files) > 0 {
		if deployment.BundleSize == 0 {
			deployment.BundleSize = bundleSize(deployment.Files)
		}
		return nil
	}
	if deployment.ObjectKey == "" {
		return nil
	}
	if s.objects == nil {
		return errors.New("object storage is not configured")
	}
	object, err := s.objects.Get(deployment.AppID, deployment.ObjectKey)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(object.Body, &deployment.Files); err != nil {
		return err
	}
	if deployment.BundleSize == 0 {
		deployment.BundleSize = bundleSize(deployment.Files)
	}
	return nil
}

func (s *Service) cleanupDeploymentObject(deployment Deployment) {
	if s.objects == nil || deployment.ObjectKey == "" {
		return
	}
	_ = s.objects.Delete(deployment.AppID, deployment.ObjectKey)
}

func (s *Service) cleanupDeploymentAssets(deployment Deployment) {
	if s.objects == nil {
		return
	}
	for _, asset := range deployment.Assets {
		if asset.ObjectKey == "" {
			continue
		}
		_ = s.objects.Delete(deployment.AppID, asset.ObjectKey)
	}
}

func (s *Service) rollbackDeployment(appID, previousID string, deployment Deployment) error {
	if err := s.store.SetActive(appID, previousID); err != nil {
		return err
	}
	if err := s.store.DeleteDeployment(deployment.ID); err != nil {
		return err
	}
	s.cleanupDeploymentObject(deployment)
	s.cleanupDeploymentAssets(deployment)
	return nil
}

func bundleSize(files []WorkerFile) int64 {
	var total int64
	for _, file := range files {
		total += file.Size
	}
	return total
}

func deploymentBundleObjectPath(deploymentID string) string {
	return path.Join("deployments", deploymentID, "bundle.json")
}

func deploymentAssetObjectPath(deploymentID, assetPath string) string {
	return path.Join("deployments", deploymentID, "assets", assetPath)
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

func (s *Service) ObjectPut(capability, objectPath, contentType string, data []byte) (ObjectInfo, error) {
	appID, err := s.appIDForCapability(capability)
	if err != nil {
		return ObjectInfo{}, err
	}
	return s.objects.Put(appID, objectPath, contentType, data)
}

func (s *Service) ObjectGet(capability, objectPath string) (ObjectBody, bool, error) {
	appID, err := s.appIDForCapability(capability)
	if err != nil {
		return ObjectBody{}, false, err
	}
	object, err := s.objects.Get(appID, objectPath)
	if errors.Is(err, ErrObjectNotFound) {
		return ObjectBody{}, false, nil
	}
	return object, err == nil, err
}

func (s *Service) ObjectHead(capability, objectPath string) (ObjectInfo, bool, error) {
	appID, err := s.appIDForCapability(capability)
	if err != nil {
		return ObjectInfo{}, false, err
	}
	object, err := s.objects.Head(appID, objectPath)
	if errors.Is(err, ErrObjectNotFound) {
		return ObjectInfo{}, false, nil
	}
	return object, err == nil, err
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

func (s *Service) WorkerKVList(appID string) ([]WorkerKVKey, error) {
	app, _, err := s.worker(appID)
	if err != nil {
		return nil, err
	}
	return s.store.KVList(app.RuntimeToken)
}

func (s *Service) WorkerKVGet(appID, key string) ([]byte, bool, error) {
	app, _, err := s.worker(appID)
	if err != nil {
		return nil, false, err
	}
	return s.store.KVGet(app.RuntimeToken, key)
}

func (s *Service) WorkerKVPut(appID, key string, value []byte) error {
	app, _, err := s.worker(appID)
	if err != nil {
		return err
	}
	return s.store.KVPut(app.RuntimeToken, key, value)
}

func (s *Service) WorkerKVDelete(appID, key string) error {
	app, _, err := s.worker(appID)
	if err != nil {
		return err
	}
	return s.store.KVDelete(app.RuntimeToken, key)
}

func (s *Service) AssetFetch(capability, assetPath string) (AssetResponse, error) {
	appID, err := s.appIDForCapability(capability)
	if err != nil {
		return AssetResponse{}, err
	}
	return s.assetResponse(appID, assetPath)
}

func (s *Service) PublicAsset(appID, requestPath string) (AssetResponse, bool, error) {
	if _, active, err := s.worker(appID); err != nil {
		return AssetResponse{}, false, err
	} else if active == nil || len(active.Deployment.Assets) == 0 {
		return AssetResponse{}, false, nil
	}
	response, err := s.assetResponse(appID, requestPath)
	if err != nil {
		return AssetResponse{}, false, err
	}
	return response, true, nil
}

func (s *Service) WorkerPort(appID, requestPath string) (int, bool, error) {
	_, active, err := s.worker(appID)
	if err != nil {
		return 0, false, err
	}
	if active == nil {
		return 0, false, nil
	}
	return active.Deployment.Port, shouldRunWorkerFirst(active.Deployment.AssetConfig.RunWorkerFirst, requestPath), nil
}

func shouldRunWorkerFirst(runWorkerFirst RunWorkerFirst, requestPath string) bool {
	if runWorkerFirst.Always() {
		return true
	}
	clean := routeRequestPath(requestPath)
	runFirst := false
	for _, route := range runWorkerFirst.Routes() {
		negated := strings.HasPrefix(route, "!")
		pattern := strings.TrimPrefix(route, "!")
		if routePatternMatches(pattern, clean) {
			runFirst = !negated
		}
	}
	return runFirst
}

func routeRequestPath(requestPath string) string {
	trimmed := strings.TrimSpace(requestPath)
	clean := path.Clean("/" + trimmed)
	if clean == "." {
		clean = "/"
	}
	if strings.HasSuffix(trimmed, "/") && clean != "/" {
		return clean + "/"
	}
	return clean
}

func routePatternMatches(pattern, requestPath string) bool {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(requestPath, strings.TrimSuffix(pattern, "*"))
	}
	return requestPath == pattern
}

func (s *Service) assetResponse(appID, requestPath string) (AssetResponse, error) {
	if s.objects == nil {
		return AssetResponse{}, errors.New("object storage is not configured")
	}
	_, active, err := s.worker(appID)
	if err != nil {
		return AssetResponse{}, err
	}
	if active == nil {
		return AssetResponse{}, ErrAppNotFound
	}
	if len(active.Deployment.Assets) == 0 {
		return AssetResponse{StatusCode: 404}, nil
	}
	resolved, status, ok := resolveAsset(active.Deployment, requestPath)
	if !ok {
		return AssetResponse{StatusCode: 404}, nil
	}
	object, err := s.objects.Get(appID, resolved.ObjectKey)
	if err != nil {
		return AssetResponse{}, err
	}
	return AssetResponse{
		Body:        object.Body,
		ContentType: resolved.ContentType,
		StatusCode:  status,
	}, nil
}

func resolveAsset(deployment Deployment, requestPath string) (AssetFile, int, bool) {
	lookup := make(map[string]AssetFile, len(deployment.Assets))
	for _, asset := range deployment.Assets {
		lookup["/"+strings.TrimPrefix(asset.Path, "/")] = asset
	}
	clean := path.Clean("/" + strings.TrimSpace(requestPath))
	if clean == "." {
		clean = "/"
	}
	if asset, ok := lookup[clean]; ok {
		return asset, 200, true
	}
	if deployment.AssetConfig.HTMLHandling == "auto-trailing-slash" {
		for _, candidate := range []string{
			htmlIndexPath(clean),
			strings.TrimSuffix(clean, "/") + ".html",
		} {
			if asset, ok := lookup[candidate]; ok {
				return asset, 200, true
			}
		}
	}
	switch deployment.AssetConfig.NotFoundHandling {
	case "single-page-application":
		if asset, ok := lookup["/index.html"]; ok {
			return asset, 200, true
		}
	case "404-page":
		if asset, ok := lookup["/404.html"]; ok {
			return asset, 404, true
		}
	}
	return AssetFile{}, 404, false
}

func htmlIndexPath(requestPath string) string {
	if requestPath == "/" {
		return "/index.html"
	}
	return strings.TrimSuffix(requestPath, "/") + "/index.html"
}

func (s *Service) generatedHostname(name string) (string, error) {
	suffix, err := s.randomHostnameSuffix()
	if err != nil {
		return "", err
	}
	prefix := slug(name)
	if prefix == "" {
		prefix = "worker"
	}
	return prefix + "-" + suffix + "." + s.baseHostname, nil
}

func normalizeHostname(hostname string) (string, error) {
	hostname = strings.TrimSuffix(strings.TrimSpace(strings.ToLower(hostname)), ".")
	if hostname == "" || strings.Contains(hostname, ":") || net.ParseIP(hostname) != nil {
		return "", errors.New("hostname must be a DNS name without a port")
	}
	return hostname, nil
}

func slug(value string) string {
	var result strings.Builder
	dash := false
	for _, char := range strings.ToLower(value) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			result.WriteRune(char)
			dash = false
		} else if result.Len() > 0 && !dash {
			result.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(result.String(), "-")
}

func randomHostnameSuffix() (string, error) {
	value := make([]byte, 4)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func randomToken() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
