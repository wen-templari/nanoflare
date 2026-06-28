package platform

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestDeployStoresFilesInObjectStorageAndHydratesActiveDeployment(t *testing.T) {
	store := newObjectBackedRepo()
	objects := newMemoryObjectStore()
	service := NewServiceWithObjects(store, &recordingWriter{}, objects)

	app, err := service.CreateApp(CreateAppInput{Name: "Hello", Hostname: "hello.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := service.Deploy(app.ID, DeployInput{
		Files:             []WorkerFile{{Path: "worker.js", Content: "export default {}"}},
		CompatibilityDate: "2025-12-10",
	})
	if err != nil {
		t.Fatal(err)
	}
	if deployment.ObjectKey != "deployments/"+deployment.ID+"/bundle.json" {
		t.Fatalf("deployment object key = %q", deployment.ObjectKey)
	}
	payload := store.snapshotObjectPayload(t, objects, app.ID, deployment.ObjectKey)
	if len(payload) != 1 || payload[0].Content != "export default {}" {
		t.Fatalf("uploaded object payload = %#v", payload)
	}

	files, err := service.WorkerFiles(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Content != "export default {}" {
		t.Fatalf("worker files = %#v", files)
	}

	active, err := service.ActiveDeployments()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].Deployment.BundleSize != int64(len("export default {}")) {
		t.Fatalf("active deployments = %#v", active)
	}
}

func TestDeployRestoresPreviousActivationWhenRuntimePublicationFails(t *testing.T) {
	store := newObjectBackedRepo()
	objects := newMemoryObjectStore()
	service := NewServiceWithObjects(store, &failAfterWriter{remaining: 1}, objects)

	app, err := service.CreateApp(CreateAppInput{Name: "Hello", Hostname: "hello.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Deploy(app.ID, DeployInput{Files: []WorkerFile{{Path: "first.js", Content: "first"}}, CompatibilityDate: "2025-12-10"})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := service.Deploy(app.ID, DeployInput{Files: []WorkerFile{{Path: "second.js", Content: "second"}}, CompatibilityDate: "2025-12-10"})
	if err == nil {
		t.Fatal("Deploy() error = nil, want runtime publication error")
	}

	active, err := service.ActiveDeployments()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].Deployment.ID != first.ID {
		t.Fatalf("active deployments = %#v, want first deployment restored", active)
	}
	records, err := store.ListDeployments()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Deployment.ID != first.ID {
		t.Fatalf("deployment records = %#v, want failed deployment removed", records)
	}
	if _, ok := objects.objects[app.ID+":"+failed.ObjectKey]; ok {
		t.Fatalf("failed deployment object %q was not cleaned up", failed.ObjectKey)
	}

	apps, err := store.ListApps()
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 || apps[0].RuntimeToken == "" {
		t.Fatalf("apps = %#v, want stable runtime token", apps)
	}
	if _, err := store.AppIDForCapability(apps[0].RuntimeToken); err != nil {
		t.Fatalf("app runtime capability was not preserved: %v", err)
	}
}

func TestActiveDeploymentsFallsBackToLegacyInlineFiles(t *testing.T) {
	store := NewStore()
	service := NewService(store, &recordingWriter{})

	app, err := service.CreateApp(CreateAppInput{Name: "Legacy", Hostname: "legacy.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	deployment := Deployment{
		ID:                "legacy-deployment",
		AppID:             app.ID,
		Files:             []WorkerFile{{Name: "worker.js", Path: "worker.js", Size: 6, Content: "legacy"}},
		Entrypoint:        "worker.js",
		Format:            "service-worker",
		CompatibilityDate: "2025-12-10",
		BundleSize:        6,
		Port:              9001,
		CreatedAt:         time.Now().UTC(),
	}
	if err := store.Activate(deployment); err != nil {
		t.Fatal(err)
	}

	files, err := service.WorkerFiles(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Content != "legacy" {
		t.Fatalf("legacy worker files = %#v", files)
	}
}

func TestDeployStoresAttachedAssetsAndServesThemThroughResolution(t *testing.T) {
	store := newObjectBackedRepo()
	objects := newMemoryObjectStore()
	service := NewServiceWithObjects(store, &recordingWriter{}, objects)

	app, err := service.CreateApp(CreateAppInput{Name: "Assets", Hostname: "assets.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := service.Deploy(app.ID, DeployInput{
		Files:             []WorkerFile{{Path: "worker.js", Content: "export default {}"}},
		Assets:            []AssetFile{{Path: "index.html", ContentType: "text/html; charset=utf-8", Data: []byte("<h1>Hello</h1>")}},
		CompatibilityDate: "2025-12-10",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(deployment.Assets) != 1 || deployment.Assets[0].ObjectKey == "" {
		t.Fatalf("deployment assets = %#v", deployment.Assets)
	}
	response, handled, err := service.PublicAsset(app.ID, "/")
	if err != nil {
		t.Fatal(err)
	}
	if !handled || response.StatusCode != 200 || string(response.Body) != "<h1>Hello</h1>" {
		t.Fatalf("public asset response = %#v handled=%v", response, handled)
	}
}

func TestAssetConfigRunWorkerFirstJSONShapes(t *testing.T) {
	for _, test := range []struct {
		name       string
		payload    string
		always     bool
		routeCount int
	}{
		{name: "true", payload: `{"run_worker_first":true}`, always: true},
		{name: "omitted", payload: `{}`},
		{name: "routes", payload: `{"run_worker_first":["/api/*","!/api/docs/*"]}`, routeCount: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			var config AssetConfig
			if err := json.Unmarshal([]byte(test.payload), &config); err != nil {
				t.Fatal(err)
			}
			if config.RunWorkerFirst.Always() != test.always {
				t.Fatalf("always = %v, want %v", config.RunWorkerFirst.Always(), test.always)
			}
			if len(config.RunWorkerFirst.Routes()) != test.routeCount {
				t.Fatalf("routes = %#v, want %d routes", config.RunWorkerFirst.Routes(), test.routeCount)
			}
		})
	}
}

func TestPublicAssetFallsBackToCustom404Page(t *testing.T) {
	store := newObjectBackedRepo()
	objects := newMemoryObjectStore()
	service := NewServiceWithObjects(store, &recordingWriter{}, objects)

	app, err := service.CreateApp(CreateAppInput{Name: "Assets", Hostname: "assets.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Deploy(app.ID, DeployInput{
		Files: []WorkerFile{{Path: "worker.js", Content: "export default {}"}},
		Assets: []AssetFile{
			{Path: "index.html", ContentType: "text/html; charset=utf-8", Data: []byte("<h1>Hello</h1>")},
			{Path: "404.html", ContentType: "text/html; charset=utf-8", Data: []byte("<h1>Missing</h1>")},
		},
		CompatibilityDate: "2025-12-10",
	})
	if err != nil {
		t.Fatal(err)
	}
	response, handled, err := service.PublicAsset(app.ID, "/missing")
	if err != nil {
		t.Fatal(err)
	}
	if !handled || response.StatusCode != 404 || string(response.Body) != "<h1>Missing</h1>" {
		t.Fatalf("public asset response = %#v handled=%v", response, handled)
	}
}

func TestPublicAssetFallsBackToSPAIndex(t *testing.T) {
	store := newObjectBackedRepo()
	objects := newMemoryObjectStore()
	service := NewServiceWithObjects(store, &recordingWriter{}, objects)

	app, err := service.CreateApp(CreateAppInput{Name: "Assets", Hostname: "assets.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Deploy(app.ID, DeployInput{
		Files:             []WorkerFile{{Path: "worker.js", Content: "export default {}"}},
		Assets:            []AssetFile{{Path: "index.html", ContentType: "text/html; charset=utf-8", Data: []byte("<h1>Hello</h1>")}},
		CompatibilityDate: "2025-12-10",
		AssetConfig:       AssetConfig{NotFoundHandling: "single-page-application"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, handled, err := service.PublicAsset(app.ID, "/settings/profile")
	if err != nil {
		t.Fatal(err)
	}
	if !handled || response.StatusCode != 200 || string(response.Body) != "<h1>Hello</h1>" {
		t.Fatalf("public asset response = %#v handled=%v", response, handled)
	}
}

func TestDeleteAppRemovesDeploymentsAndCapability(t *testing.T) {
	store := NewStore()
	service := NewService(store, &recordingWriter{})

	app, err := service.CreateApp(CreateAppInput{Name: "Hello", Hostname: "hello.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Deploy(app.ID, DeployInput{
		Files:             []WorkerFile{{Path: "worker.js", Content: "export default {}"}},
		CompatibilityDate: "2025-12-10",
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteApp(app.ID); err != nil {
		t.Fatal(err)
	}
	apps, err := service.ListApps()
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 0 {
		t.Fatalf("apps = %#v, want empty", apps)
	}
	if _, _, err := service.worker(app.ID); !errors.Is(err, ErrAppNotFound) {
		t.Fatalf("worker() error = %v, want ErrAppNotFound", err)
	}
	if _, err := store.AppIDForCapability(app.RuntimeToken); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("capability error = %v, want ErrInvalidCapability", err)
	}
}

type recordingWriter struct{}

func (w *recordingWriter) Write([]ActiveDeployment) error {
	return nil
}

type failAfterWriter struct {
	remaining int
}

func (w *failAfterWriter) Write([]ActiveDeployment) error {
	if w.remaining == 0 {
		return errors.New("runtime unavailable")
	}
	w.remaining--
	return nil
}

type memoryObjectStore struct {
	objects map[string][]byte
}

func newMemoryObjectStore() *memoryObjectStore {
	return &memoryObjectStore{objects: make(map[string][]byte)}
}

func (s *memoryObjectStore) PresignUpload(string, string, time.Duration) (string, error) {
	return "", nil
}

func (s *memoryObjectStore) PresignDownload(string, string, time.Duration) (string, error) {
	return "", nil
}

func (s *memoryObjectStore) Put(appID, path string, _ string, data []byte) error {
	s.objects[appID+":"+path] = append([]byte(nil), data...)
	return nil
}

func (s *memoryObjectStore) Get(appID, path string) ([]byte, error) {
	data, ok := s.objects[appID+":"+path]
	if !ok {
		return nil, errors.New("object not found")
	}
	return append([]byte(nil), data...), nil
}

func (s *memoryObjectStore) Delete(appID, path string) error {
	delete(s.objects, appID+":"+path)
	return nil
}

type objectBackedRepo struct {
	*Store
}

func newObjectBackedRepo() *objectBackedRepo {
	return &objectBackedRepo{Store: NewStore()}
}

func (r *objectBackedRepo) Activate(deployment Deployment) error {
	copy := deployment
	if copy.ObjectKey != "" {
		copy.Files = nil
	}
	return r.Store.Activate(copy)
}

func (r *objectBackedRepo) ActiveDeployments() ([]ActiveDeployment, error) {
	active, err := r.Store.ActiveDeployments()
	if err != nil {
		return nil, err
	}
	for i := range active {
		active[i].Deployment.Files = append([]WorkerFile(nil), active[i].Deployment.Files...)
		if active[i].Deployment.ObjectKey != "" {
			active[i].Deployment.Files = nil
		}
	}
	return active, nil
}

func (r *objectBackedRepo) ListDeployments() ([]DeploymentRecord, error) {
	records, err := r.Store.ListDeployments()
	if err != nil {
		return nil, err
	}
	for i := range records {
		if records[i].Deployment.ObjectKey != "" {
			records[i].Deployment.Files = nil
		}
	}
	return records, nil
}

func (r *objectBackedRepo) snapshotObjectPayload(t *testing.T, objects *memoryObjectStore, appID, objectKey string) []WorkerFile {
	t.Helper()
	raw, err := objects.Get(appID, objectKey)
	if err != nil {
		t.Fatal(err)
	}
	var files []WorkerFile
	if err := json.Unmarshal(raw, &files); err != nil {
		t.Fatal(err)
	}
	return files
}
