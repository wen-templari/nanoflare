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

	"github.com/clas/platform/internal/platform"
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
	if project.Name != "Hello Worker" || project.Hostname != "hello-worker.example.com" {
		t.Fatalf("project = %#v", project)
	}
	if project.CompatibilityDate != "2026-05-31" || project.Entrypoint != "worker.js" || project.Format != "modules" {
		t.Fatalf("project = %#v", project)
	}
	content, err := os.ReadFile(filepath.Join("hello", "worker.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "hello from platform") {
		t.Fatalf("starter worker = %q", content)
	}
}

func TestCreateAndDeployWorker(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	var created platform.CreateAppInput
	var deployed platform.DeployInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/apps":
			decodeRequest(t, r, &created)
			writeJSON(t, w, http.StatusCreated, platform.App{ID: "app-123", Hostname: created.Hostname})
		case "/v1/apps/app-123/deployments":
			decodeRequest(t, r, &deployed)
			writeJSON(t, w, http.StatusCreated, platform.Deployment{ID: "deployment-456"})
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
		Files:             []string{"worker.js"},
	})
	if err := os.WriteFile("worker.js", []byte("addEventListener('fetch', () => {});"), 0o644); err != nil {
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
	if created.Name != "Hello" || created.Hostname != "hello.example.com" {
		t.Fatalf("create payload = %#v", created)
	}
	if deployed.Entrypoint != "worker.js" || deployed.CompatibilityDate != "2025-12-10" {
		t.Fatalf("deploy payload = %#v", deployed)
	}
	if len(deployed.Files) != 1 || deployed.Files[0].Path != "worker.js" || deployed.Files[0].Content == "" {
		t.Fatalf("deploy files = %#v", deployed.Files)
	}
}

func TestListWorkers(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/apps" {
			http.NotFound(w, r)
			return
		}
		writeJSON(t, w, http.StatusOK, []platform.App{
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
	if err == nil || !strings.Contains(err.Error(), "platform create") {
		t.Fatalf("error = %v", err)
	}
}

func TestCreateReportsPlatformError(t *testing.T) {
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
