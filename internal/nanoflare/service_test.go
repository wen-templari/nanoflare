package nanoflare

import (
	"encoding/json"
	"errors"
	"strings"
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

func TestDeployValidatesCronTriggers(t *testing.T) {
	service := NewService(NewStore(), &recordingWriter{})
	app, err := service.CreateApp(CreateAppInput{Name: "Hello", Hostname: "hello.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := service.Deploy(app.ID, DeployInput{
		Files:             []WorkerFile{{Path: "worker.js", Content: "export default {}"}},
		CompatibilityDate: "2025-12-10",
		Triggers:          TriggerConfig{Crons: []string{" */5 * * * * "}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(deployment.Triggers.Crons) != 1 || deployment.Triggers.Crons[0] != "*/5 * * * *" {
		t.Fatalf("deployment triggers = %#v", deployment.Triggers)
	}
	if _, err := service.Deploy(app.ID, DeployInput{
		Files:             []WorkerFile{{Path: "worker.js", Content: "export default {}"}},
		CompatibilityDate: "2025-12-10",
		Triggers:          TriggerConfig{Crons: []string{"0 0 LW * *"}},
	}); err == nil {
		t.Fatal("Deploy() error = nil, want invalid cron error")
	}
}

func TestObjectStorageBucketObjectsSurviveWorkerRecreate(t *testing.T) {
	store := newObjectBackedRepo()
	objects := newMemoryObjectStore()
	service := NewServiceWithObjects(store, &recordingWriter{}, objects)

	bucket, err := service.CreateObjectStorageBucket(CreateObjectStorageBucketInput{Name: "gallery-images"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.CreateApp(CreateAppInput{Name: "Gallery", Hostname: "gallery.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Deploy(first.ID, DeployInput{
		Files:                []WorkerFile{{Path: "worker.js", Content: "export default {}"}},
		CompatibilityDate:    "2025-12-10",
		ObjectStorageBuckets: []ObjectStorageBucketBinding{{Binding: "OBJECTS", BucketID: bucket.ID}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ObjectPut(first.RuntimeToken, bucket.ID, "photos/sunrise.jpg", "image/jpeg", []byte("image bytes")); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteApp(first.ID); err != nil {
		t.Fatal(err)
	}

	second, err := service.CreateApp(CreateAppInput{Name: "Gallery", Hostname: "gallery.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Deploy(second.ID, DeployInput{
		Files:                []WorkerFile{{Path: "worker.js", Content: "export default {}"}},
		CompatibilityDate:    "2025-12-10",
		ObjectStorageBuckets: []ObjectStorageBucketBinding{{Binding: "OBJECTS", BucketID: bucket.ID}},
	}); err != nil {
		t.Fatal(err)
	}

	object, ok, err := service.ObjectGet(second.RuntimeToken, bucket.ID, "photos/sunrise.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || string(object.Body) != "image bytes" {
		t.Fatalf("object after worker recreate = ok:%v body:%q", ok, object.Body)
	}
	items, err := service.ObjectList(second.RuntimeToken, bucket.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Key != "photos/sunrise.jpg" {
		t.Fatalf("object list after worker recreate = %#v", items)
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

func TestCreateAppNormalizesExplicitHostname(t *testing.T) {
	service := NewService(NewStore(), &recordingWriter{})
	app, err := service.CreateApp(CreateAppInput{Name: "Hello", Hostname: " Hello.EXAMPLE.com. "})
	if err != nil {
		t.Fatal(err)
	}
	if app.Hostname != "hello.example.com" {
		t.Fatalf("hostname = %q, want normalized hostname", app.Hostname)
	}
}

func TestCreateAppGeneratesHostnameFromBase(t *testing.T) {
	store := NewStore()
	if err := store.CreateOrganization(Organization{ID: "8b07d103bf36c2528d4d23359b6e220d3141eeaa48fe8baf", Name: "Acme Org"}); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, &recordingWriter{})
	if err := service.SetBaseHostname(" Workers.EXAMPLE.com. "); err != nil {
		t.Fatal(err)
	}

	app, err := service.CreateApp(CreateAppInput{Name: "Hello Worker", OrgID: "8b07d103bf36c2528d4d23359b6e220d3141eeaa48fe8baf"})
	if err != nil {
		t.Fatal(err)
	}
	if app.Hostname != "hello-worker-acme-org.workers.example.com" {
		t.Fatalf("hostname = %q", app.Hostname)
	}
}

func TestCreateAppRequiresBaseHostnameWhenHostnameOmitted(t *testing.T) {
	service := NewService(NewStore(), &recordingWriter{})
	_, err := service.CreateApp(CreateAppInput{Name: "Hello Worker"})
	if err == nil || !strings.Contains(err.Error(), "base hostname is not configured") {
		t.Fatalf("error = %v", err)
	}
}

func TestCreateAppUsesWorkerSlugFallback(t *testing.T) {
	service := NewService(NewStore(), &recordingWriter{})
	if err := service.SetBaseHostname("example.com"); err != nil {
		t.Fatal(err)
	}
	service.randomHostnameSuffix = func() (string, error) { return "a1b2c3d4", nil }

	app, err := service.CreateApp(CreateAppInput{Name: "!!!"})
	if err != nil {
		t.Fatal(err)
	}
	if app.Hostname != "worker.example.com" {
		t.Fatalf("hostname = %q", app.Hostname)
	}
}

func TestCreateAppDoesNotRetryExplicitHostnameConflict(t *testing.T) {
	service := NewService(NewStore(), &recordingWriter{})
	if _, err := service.CreateApp(CreateAppInput{Name: "Hello", Hostname: "hello.example.com"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service.randomHostnameSuffix = func() (string, error) {
		calls++
		return "a1b2c3d4", nil
	}

	_, err := service.CreateApp(CreateAppInput{Name: "Hello Again", Hostname: "hello.example.com"})
	if !errors.Is(err, ErrAppExists) {
		t.Fatalf("error = %v, want ErrAppExists", err)
	}
	if calls != 0 {
		t.Fatalf("random suffix calls = %d, want 0", calls)
	}
}

func TestCreateAppRetriesGeneratedHostnameConflict(t *testing.T) {
	store := NewStore()
	if err := store.CreateOrganization(Organization{ID: "org-123", Name: "Acme"}); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, &recordingWriter{})
	if err := service.SetBaseHostname("example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateApp(CreateAppInput{Name: "Hello", Hostname: "hello-acme.example.com"}); err != nil {
		t.Fatal(err)
	}
	suffixes := []string{"a1b2c3d4e5", "d4c3b2a1f0"}
	service.randomHostnameSuffix = func() (string, error) {
		next := suffixes[0]
		suffixes = suffixes[1:]
		return next, nil
	}

	app, err := service.CreateApp(CreateAppInput{Name: "Hello", OrgID: "org-123"})
	if err != nil {
		t.Fatal(err)
	}
	if app.Hostname != "hello-a1b2c3d4e5-acme.example.com" {
		t.Fatalf("hostname = %q", app.Hostname)
	}
}

func TestCreateAppNormalizesProtectedRoutes(t *testing.T) {
	service := NewService(NewStore(), &recordingWriter{})
	app, err := service.CreateApp(CreateAppInput{
		Name:     "Secure",
		Hostname: "secure.example.com",
		Auth:     AuthConfig{ProtectedRoutes: []string{" /admin/* ", "/admin/*", "/reports"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(app.Auth.ProtectedRoutes) != 2 || app.Auth.ProtectedRoutes[0] != "/admin/*" || app.Auth.ProtectedRoutes[1] != "/reports" {
		t.Fatalf("protected routes = %#v", app.Auth.ProtectedRoutes)
	}
}

func TestUpdateAppRejectsInvalidProtectedRoutes(t *testing.T) {
	service := NewService(NewStore(), &recordingWriter{})
	app, err := service.CreateApp(CreateAppInput{Name: "Secure", Hostname: "secure.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.UpdateApp(app.ID, UpdateAppInput{Auth: &AuthConfig{ProtectedRoutes: []string{"admin/*"}}})
	if err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("error = %v", err)
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

func TestDeployRejectsUnknownKVNamespace(t *testing.T) {
	service := NewService(NewStore(), &recordingWriter{})
	app, err := service.CreateApp(CreateAppInput{Name: "Hello", Hostname: "hello.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Deploy(app.ID, DeployInput{
		Files:             []WorkerFile{{Path: "worker.js", Content: "export default {}"}},
		CompatibilityDate: "2025-12-10",
		KVNamespaces:      []KVBinding{{Binding: "KV", ID: "missing"}},
	})
	if !errors.Is(err, ErrKVNamespaceNotFound) {
		t.Fatalf("error = %v, want ErrKVNamespaceNotFound", err)
	}
}

func TestDeployRejectsDuplicateKVBindings(t *testing.T) {
	service := NewService(NewStore(), &recordingWriter{})
	app, err := service.CreateApp(CreateAppInput{Name: "Hello", Hostname: "hello.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.CreateKVNamespace(CreateKVNamespaceInput{Name: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateKVNamespace(CreateKVNamespaceInput{Name: "second"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Deploy(app.ID, DeployInput{
		Files:             []WorkerFile{{Path: "worker.js", Content: "export default {}"}},
		CompatibilityDate: "2025-12-10",
		KVNamespaces: []KVBinding{
			{Binding: "KV", ID: first.ID},
			{Binding: "KV", ID: second.ID},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("error = %v, want duplicate binding error", err)
	}
}

func TestPutSecretRollsActiveDeploymentAndExposesVars(t *testing.T) {
	service := NewService(NewStore(), &recordingWriter{})
	codec, err := NewSecretCodec("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	service.SetSecretCodec(codec)

	app, err := service.CreateApp(CreateAppInput{Name: "Hello", Hostname: "hello.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Deploy(app.ID, DeployInput{
		Files:             []WorkerFile{{Path: "worker.js", Content: "export default {}"}},
		CompatibilityDate: "2025-12-10",
		Vars: map[string]json.RawMessage{
			"API_HOST":      json.RawMessage(`"example.com"`),
			"FEATURE_FLAGS": json.RawMessage(`{"beta":true}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PutSecret(app.ID, "DB_CONNECTION_STRING", "postgres://secret"); err != nil {
		t.Fatal(err)
	}

	detail, err := service.WorkerDetail(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Deployment == nil || detail.Deployment.ID == first.ID {
		t.Fatalf("deployment = %#v, want secret rollout to create a new deployment", detail.Deployment)
	}
	if got := string(detail.Deployment.Vars["API_HOST"]); got != `"example.com"` {
		t.Fatalf("API_HOST = %s", got)
	}
	if len(detail.Secrets) != 1 || detail.Secrets[0].Name != "DB_CONNECTION_STRING" {
		t.Fatalf("secrets = %#v", detail.Secrets)
	}
	active, err := service.ActiveDeployments()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].App.SecretValues["DB_CONNECTION_STRING"] != "postgres://secret" {
		t.Fatalf("active = %#v", active)
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
	objects map[string]ObjectBody
}

func newMemoryObjectStore() *memoryObjectStore {
	return &memoryObjectStore{objects: make(map[string]ObjectBody)}
}

func (s *memoryObjectStore) PresignUpload(string, string, time.Duration) (string, error) {
	return "", nil
}

func (s *memoryObjectStore) PresignDownload(string, string, time.Duration) (string, error) {
	return "", nil
}

func (s *memoryObjectStore) Put(appID, path string, contentType string, data []byte) (ObjectInfo, error) {
	object := ObjectBody{
		ObjectInfo: ObjectInfo{
			Key:      path,
			Size:     int64(len(data)),
			ETag:     "etag-" + path,
			HTTPETag: `"etag-` + path + `"`,
			Uploaded: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			HTTPMetadata: ObjectHTTPMetadata{
				ContentType: contentType,
			},
		},
		Body: append([]byte(nil), data...),
	}
	s.objects[appID+":"+path] = object
	return object.ObjectInfo, nil
}

func (s *memoryObjectStore) Get(appID, path string) (ObjectBody, error) {
	data, ok := s.objects[appID+":"+path]
	if !ok {
		return ObjectBody{}, ErrObjectNotFound
	}
	data.Body = append([]byte(nil), data.Body...)
	return data, nil
}

func (s *memoryObjectStore) Head(appID, path string) (ObjectInfo, error) {
	data, ok := s.objects[appID+":"+path]
	if !ok {
		return ObjectInfo{}, ErrObjectNotFound
	}
	return data.ObjectInfo, nil
}

func (s *memoryObjectStore) List(appID, prefix string) ([]ObjectInfo, error) {
	items := make([]ObjectInfo, 0)
	for key, data := range s.objects {
		storedPrefix := appID + ":" + prefix + "/"
		if !strings.HasPrefix(key, storedPrefix) {
			continue
		}
		object := data.ObjectInfo
		object.Key = strings.TrimPrefix(object.Key, prefix+"/")
		items = append(items, object)
	}
	return items, nil
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
	if err := json.Unmarshal(raw.Body, &files); err != nil {
		t.Fatal(err)
	}
	return files
}
