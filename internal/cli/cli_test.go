package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestInitCreatesStarterProject(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	var stdout bytes.Buffer
	runner := NewRunner(&stdout, io.Discard)
	runner.Now = func() time.Time {
		return time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	}

	if err := runner.Run([]string{"init", "--name", "Hello Worker", "hello"}); err != nil {
		t.Fatal(err)
	}

	project := readProject(t, filepath.Join("hello", projectFilename))
	if project.Name != "Hello Worker" || project.Hostname != "" {
		t.Fatalf("project = %#v", project)
	}
	if project.CompatibilityDate != "2026-05-31" || project.Entrypoint != "worker.js" || project.Format != "modules" {
		t.Fatalf("project = %#v", project)
	}
	content, err := os.ReadFile(filepath.Join("hello", "worker.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "hello from nanoflare") {
		t.Fatalf("starter worker = %q", content)
	}
}

func TestInitPreservesExplicitHostname(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	runner := NewRunner(io.Discard, io.Discard)
	runner.Now = func() time.Time {
		return time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	}

	if err := runner.Run([]string{"init", "--name", "Hello Worker", "--hostname", "hello.example.com", "hello"}); err != nil {
		t.Fatal(err)
	}

	project := readProject(t, filepath.Join("hello", projectFilename))
	if project.Hostname != "hello.example.com" {
		t.Fatalf("hostname = %q", project.Hostname)
	}
}

func TestCreateAndDeployWorker(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	var created nanoflare.CreateAppInput
	var updated nanoflare.UpdateAppInput
	var deployed nanoflare.DeployInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/apps":
			decodeRequest(t, r, &created)
			writeJSON(t, w, http.StatusCreated, nanoflare.App{ID: "app-123", Hostname: created.Hostname})
		case "/v1/apps/app-123":
			if r.Method != http.MethodPatch {
				http.NotFound(w, r)
				return
			}
			decodeRequest(t, r, &updated)
			w.WriteHeader(http.StatusOK)
		case "/v1/apps/app-123/deployments":
			decodeRequest(t, r, &deployed)
			writeJSON(t, w, http.StatusCreated, nanoflare.Deployment{ID: "deployment-456"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	writeProjectFile(t, Project{
		Name:              "Hello",
		Hostname:          "hello.example.com",
		APIURL:            server.URL,
		Entrypoint:        "worker.js",
		CompatibilityDate: "2025-12-10",
		Vars: map[string]json.RawMessage{
			"API_HOST": json.RawMessage(`"example.com"`),
		},
		Files: []string{"worker.js"},
		Auth: ProjectAuth{
			ProtectedRoutes: []string{"/admin/*"},
		},
		Assets: ProjectAssets{
			Directory:        "public",
			Binding:          "STATIC",
			NotFoundHandling: "404-page",
		},
	})
	if err := os.WriteFile("worker.js", []byte("addEventListener('fetch', () => {});"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir("public", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("public", "logo.svg"), []byte("<svg />"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := NewRunner(io.Discard, io.Discard)
	if err := runner.Run([]string{"create", "worker"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run([]string{"deploy", "worker"}); err != nil {
		t.Fatal(err)
	}

	project := readProject(t, projectFilename)
	if project.AppID != "app-123" {
		t.Fatalf("app id = %q", project.AppID)
	}
	if project.Hostname != "hello.example.com" {
		t.Fatalf("hostname = %q", project.Hostname)
	}
	if created.Name != "Hello" || created.Hostname != "hello.example.com" {
		t.Fatalf("create payload = %#v", created)
	}
	if len(created.Auth.ProtectedRoutes) != 1 || created.Auth.ProtectedRoutes[0] != "/admin/*" {
		t.Fatalf("create auth = %#v", created.Auth)
	}
	if updated.Auth == nil || len(updated.Auth.ProtectedRoutes) != 1 || updated.Auth.ProtectedRoutes[0] != "/admin/*" {
		t.Fatalf("update auth = %#v", updated.Auth)
	}
	if deployed.Entrypoint != "worker.js" || deployed.CompatibilityDate != "2025-12-10" {
		t.Fatalf("deploy payload = %#v", deployed)
	}
	if got := string(deployed.Vars["API_HOST"]); got != `"example.com"` {
		t.Fatalf("deploy vars = %#v", deployed.Vars)
	}
	if len(deployed.Files) != 1 || deployed.Files[0].Path != "worker.js" || deployed.Files[0].Content == "" {
		t.Fatalf("deploy files = %#v", deployed.Files)
	}
	if len(deployed.Assets) != 1 || deployed.Assets[0].Path != "logo.svg" {
		t.Fatalf("deploy assets = %#v", deployed.Assets)
	}
	if deployed.AssetConfig.Binding != "STATIC" || deployed.AssetConfig.NotFoundHandling != "404-page" {
		t.Fatalf("asset config = %#v", deployed.AssetConfig)
	}
}

func TestSecretListAndDelete(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/apps/app-123/secrets":
			writeJSON(t, w, http.StatusOK, []nanoflare.Secret{{Name: "DB_URL", UpdatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/apps/app-123/secrets/DB_URL":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	writeProjectFile(t, Project{
		Name:              "Hello",
		Hostname:          "hello.example.com",
		AppID:             "app-123",
		APIURL:            server.URL,
		Entrypoint:        "worker.js",
		CompatibilityDate: "2025-12-10",
		Files:             []string{"worker.js"},
	})

	var stdout bytes.Buffer
	runner := NewRunner(&stdout, io.Discard)
	if err := runner.Run([]string{"secret", "list"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "DB_URL\t2026-01-02T03:04:05Z") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	stdout.Reset()
	if err := runner.Run([]string{"secret", "delete", "DB_URL"}); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "Deleted secret DB_URL\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestSecretPutUsesValueArgument(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	var payload nanoflare.PutSecretInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/apps/app-123/secrets/DB_URL":
			decodeRequest(t, r, &payload)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	writeProjectFile(t, Project{
		Name:              "Hello",
		Hostname:          "hello.example.com",
		AppID:             "app-123",
		APIURL:            server.URL,
		Entrypoint:        "worker.js",
		CompatibilityDate: "2025-12-10",
		Files:             []string{"worker.js"},
	})

	var stdout bytes.Buffer
	runner := NewRunner(&stdout, io.Discard)
	if err := runner.Run([]string{"secret", "put", "DB_URL", "postgres://secret"}); err != nil {
		t.Fatal(err)
	}
	if payload.Value != "postgres://secret" {
		t.Fatalf("payload = %#v", payload)
	}
	if got := stdout.String(); got != "Updated secret DB_URL\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestCreatePersistsGeneratedHostname(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	var created nanoflare.CreateAppInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/apps" {
			http.NotFound(w, r)
			return
		}
		decodeRequest(t, r, &created)
		writeJSON(t, w, http.StatusCreated, nanoflare.App{ID: "app-123", Name: created.Name, Hostname: "hello-a1b2c3d4.example.com"})
	}))
	defer server.Close()

	writeProjectFile(t, Project{
		Name:              "Hello",
		APIURL:            server.URL,
		Entrypoint:        "worker.js",
		CompatibilityDate: "2025-12-10",
		Files:             []string{"worker.js"},
	})

	if err := NewRunner(io.Discard, io.Discard).Run([]string{"create"}); err != nil {
		t.Fatal(err)
	}
	project := readProject(t, projectFilename)
	if created.Name != "Hello" || created.Hostname != "" {
		t.Fatalf("create payload = %#v", created)
	}
	if project.AppID != "app-123" || project.Hostname != "hello-a1b2c3d4.example.com" {
		t.Fatalf("project = %#v", project)
	}
}

func TestProjectAssetsRunWorkerFirstJSONShapes(t *testing.T) {
	for _, test := range []struct {
		name       string
		payload    string
		always     bool
		routeCount int
	}{
		{name: "true", payload: `{"name":"Hello","hostname":"hello.example.com","api_url":"http://127.0.0.1:8080","entrypoint":"worker.js","compatibility_date":"2025-12-10","files":["worker.js"],"assets":{"run_worker_first":true}}`, always: true},
		{name: "omitted", payload: `{"name":"Hello","hostname":"hello.example.com","api_url":"http://127.0.0.1:8080","entrypoint":"worker.js","compatibility_date":"2025-12-10","files":["worker.js"],"assets":{}}`},
		{name: "routes", payload: `{"name":"Hello","hostname":"hello.example.com","api_url":"http://127.0.0.1:8080","entrypoint":"worker.js","compatibility_date":"2025-12-10","files":["worker.js"],"assets":{"run_worker_first":["/api/*","!/api/docs/*"]}}`, routeCount: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			var project Project
			if err := json.Unmarshal([]byte(test.payload), &project); err != nil {
				t.Fatal(err)
			}
			if project.Assets.RunWorkerFirst.Always() != test.always {
				t.Fatalf("always = %v, want %v", project.Assets.RunWorkerFirst.Always(), test.always)
			}
			if len(project.Assets.RunWorkerFirst.Routes()) != test.routeCount {
				t.Fatalf("routes = %#v, want %d routes", project.Assets.RunWorkerFirst.Routes(), test.routeCount)
			}
		})
	}
}

func TestListWorkers(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/apps" {
			http.NotFound(w, r)
			return
		}
		writeJSON(t, w, http.StatusOK, []nanoflare.App{
			{ID: "app-123", Name: "Hello", Hostname: "hello.example.com"},
			{ID: "app-456", Name: "World", Hostname: "world.example.com"},
		})
	}))
	defer server.Close()

	var stdout bytes.Buffer
	runner := NewRunner(&stdout, io.Discard)
	if err := runner.Run([]string{"list", "--api-url", server.URL}); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "app-123\tHello\thello.example.com\napp-456\tWorld\tworld.example.com\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestDeleteRegisteredWorkerClearsLocalAppID(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/apps/app-123" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	writeProjectFile(t, Project{
		Name:              "Hello",
		Hostname:          "hello.example.com",
		AppID:             "app-123",
		APIURL:            server.URL,
		Entrypoint:        "worker.js",
		CompatibilityDate: "2025-12-10",
		Files:             []string{"worker.js"},
	})

	var stdout bytes.Buffer
	runner := NewRunner(&stdout, io.Discard)
	if err := runner.Run([]string{"delete"}); err != nil {
		t.Fatal(err)
	}
	project := readProject(t, projectFilename)
	if project.AppID != "" {
		t.Fatalf("app id = %q, want cleared", project.AppID)
	}
	if got := stdout.String(); got != "Deleted worker app-123\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestDeleteWorkerByID(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/apps/app-789" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	runner := NewRunner(io.Discard, io.Discard)
	if err := runner.Run([]string{"delete", "--api-url", server.URL, "app-789"}); err != nil {
		t.Fatal(err)
	}
}

func TestDeployRequiresRegisteredWorker(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	writeProjectFile(t, Project{
		Name:              "Hello",
		Hostname:          "hello.example.com",
		APIURL:            defaultAPIURL,
		Entrypoint:        "worker.js",
		CompatibilityDate: "2025-12-10",
		Files:             []string{"worker.js"},
	})

	err := NewRunner(io.Discard, io.Discard).Run([]string{"deploy"})
	if err == nil || !strings.Contains(err.Error(), "nanoflare create") {
		t.Fatalf("error = %v", err)
	}
}

func TestCreateReportsNanoflareError(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusConflict, map[string]string{"error": "hostname already exists"})
	}))
	defer server.Close()
	writeProjectFile(t, Project{
		Name:              "Hello",
		Hostname:          "hello.example.com",
		APIURL:            server.URL,
		Entrypoint:        "worker.js",
		CompatibilityDate: "2025-12-10",
		Files:             []string{"worker.js"},
	})

	err := NewRunner(io.Discard, io.Discard).Run([]string{"create"})
	if err == nil || !strings.Contains(err.Error(), "hostname already exists") {
		t.Fatalf("error = %v", err)
	}
}

func TestKVNamespaceCommands(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	var created nanoflare.CreateKVNamespaceInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/kv/namespaces":
			decodeRequest(t, r, &created)
			writeJSON(t, w, http.StatusCreated, nanoflare.KVNamespace{ID: "kvns-123", Name: created.Name})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/kv/namespaces":
			writeJSON(t, w, http.StatusOK, []nanoflare.KVNamespace{{ID: "kvns-123", Name: "sessions"}})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/kv/namespaces/kvns-123":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	runner := NewRunner(&stdout, io.Discard)
	if err := runner.Run([]string{"kv", "namespace", "create", "--api-url", server.URL, "sessions"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run([]string{"kv", "namespace", "list", "--api-url", server.URL}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run([]string{"kv", "namespace", "delete", "--api-url", server.URL, "kvns-123"}); err != nil {
		t.Fatal(err)
	}
	if created.Name != "sessions" {
		t.Fatalf("create payload = %#v", created)
	}
	if got := stdout.String(); got != "Created KV namespace kvns-123\tsessions\nkvns-123\tsessions\nDeleted KV namespace kvns-123\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestObjectStorageBucketCommands(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	var created nanoflare.CreateObjectStorageBucketInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/object-storage-buckets":
			decodeRequest(t, r, &created)
			writeJSON(t, w, http.StatusCreated, nanoflare.ObjectStorageBucket{ID: "bucket-123", Name: created.Name})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/object-storage-buckets":
			writeJSON(t, w, http.StatusOK, []nanoflare.ObjectStorageBucket{{ID: "bucket-123", Name: "customer-files"}})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/object-storage-buckets/bucket-123":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	runner := NewRunner(&stdout, io.Discard)
	if err := runner.Run([]string{"object-storage", "bucket", "create", "--api-url", server.URL, "customer-files"}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run([]string{"object-storage", "bucket", "list", "--api-url", server.URL}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run([]string{"object-storage", "bucket", "delete", "--api-url", server.URL, "bucket-123"}); err != nil {
		t.Fatal(err)
	}
	if created.Name != "customer-files" {
		t.Fatalf("create payload = %#v", created)
	}
	if got := stdout.String(); got != "Created object storage bucket bucket-123\tcustomer-files\nbucket-123\tcustomer-files\nDeleted object storage bucket bucket-123\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestLoadProjectAcceptsLegacyObjectStorageBucketShape(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	if err := os.WriteFile(projectFilename, []byte(`{
  "name": "Hello",
  "hostname": "hello.example.com",
  "api_url": "http://127.0.0.1:8080",
  "entrypoint": "worker.js",
  "compatibility_date": "2025-12-10",
  "files": ["worker.js"],
  "object_storage_buckets": [
    { "binding": "OBJECTS", "id": "bucket-123" }
  ]
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, project, err := loadProject()
	if err != nil {
		t.Fatal(err)
	}
	if len(project.ObjectStorageBuckets) != 1 {
		t.Fatalf("object storage buckets = %#v", project.ObjectStorageBuckets)
	}
	if project.ObjectStorageBuckets[0].Binding != "OBJECTS" || project.ObjectStorageBuckets[0].BucketID != "bucket-123" {
		t.Fatalf("legacy object storage bucket = %#v", project.ObjectStorageBuckets[0])
	}
}

func withWorkingDirectory(t *testing.T, dir string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatal(err)
		}
	})
}

func writeProjectFile(t *testing.T, project Project) {
	t.Helper()
	if err := writeProject(projectFilename, project, os.O_TRUNC); err != nil {
		t.Fatal(err)
	}
}

func readProject(t *testing.T, path string) Project {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var project Project
	if err := json.Unmarshal(content, &project); err != nil {
		t.Fatal(err)
	}
	return project
}

func decodeRequest(t *testing.T, request *http.Request, target any) {
	t.Helper()
	if err := json.NewDecoder(request.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, writer http.ResponseWriter, status int, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Fatal(err)
	}
}
