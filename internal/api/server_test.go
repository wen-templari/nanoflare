package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/clas/platform/internal/config"
	"github.com/clas/platform/internal/platform"
)

func TestCreateDeployAndScopedKV(t *testing.T) {
	dir := t.TempDir()
	service := platform.NewService(platform.NewStore(), config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://platformd/internal/auth/verify",
		"127.0.0.1",
	))
	server := NewServer(service)

	appOne := createApp(t, server, "App One", "one.example.com")
	appTwo := createApp(t, server, "App Two", "two.example.com")
	one := deploy(t, server, appOne.ID)
	two := deploy(t, server, appTwo.ID)

	request(t, server, http.MethodPost, "/internal/runtime/kv/put", one.CapabilityToken, `{"key":"color","value":"blue"}`, http.StatusNoContent)
	response := request(t, server, http.MethodPost, "/internal/runtime/kv/get", one.CapabilityToken, `{"key":"color"}`, http.StatusOK)
	if response["value"] != "blue" {
		t.Fatalf("got %#v, want app-one value", response)
	}
	response = request(t, server, http.MethodPost, "/internal/runtime/kv/get", two.CapabilityToken, `{"key":"color"}`, http.StatusOK)
	if response["value"] != nil {
		t.Fatalf("got %#v, want app-two KV to remain isolated", response)
	}
}

func TestWorkerConsoleAPIs(t *testing.T) {
	dir := t.TempDir()
	bundle := `addEventListener("fetch", () => {});`
	service := platform.NewServiceWithConsole(platform.NewStore(), config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://platformd/internal/auth/verify",
		"127.0.0.1",
	), nil, fakeOutput{}, fakeTraffic{})
	server := NewServer(service)
	app := createApp(t, server, "Console App", "console.example.com")
	deployContent(t, server, app.ID, []platform.WorkerFile{{Path: "worker.js", Content: bundle}}, "")

	var detail platform.WorkerDetail
	requestJSON(t, server, http.MethodGet, "/v1/apps/"+app.ID, http.StatusOK, &detail)
	if detail.Deployment == nil || detail.Deployment.Entrypoint != "worker.js" || detail.Deployment.BundleSize != int64(len(bundle)) {
		t.Fatalf("unexpected worker detail: %#v", detail)
	}
	if detail.Deployment.CompatibilityDate != "2026-05-31" {
		t.Fatalf("compatibility date = %q", detail.Deployment.CompatibilityDate)
	}

	var files []platform.WorkerFile
	requestJSON(t, server, http.MethodGet, "/v1/apps/"+app.ID+"/files", http.StatusOK, &files)
	if len(files) != 1 || files[0].Name != "worker.js" || files[0].Content != bundle {
		t.Fatalf("unexpected worker files: %#v", files)
	}

	var output []platform.WorkerOutputLine
	requestJSON(t, server, http.MethodGet, "/v1/apps/"+app.ID+"/output", http.StatusOK, &output)
	if len(output) != 1 || output[0].Message != "runtime ready" {
		t.Fatalf("unexpected worker output: %#v", output)
	}

	var traffic platform.WorkerTraffic
	requestJSON(t, server, http.MethodGet, "/v1/apps/"+app.ID+"/traffic", http.StatusOK, &traffic)
	if !traffic.Available || traffic.RequestsPerSecond != 4.25 || len(traffic.Traffic) != 2 {
		t.Fatalf("unexpected worker traffic: %#v", traffic)
	}
}

func TestWorkerConsoleAPIsForRegisteredWorkerWithoutDeployment(t *testing.T) {
	service := platform.NewService(platform.NewStore(), config.NewWriter(
		filepath.Join(t.TempDir(), "workerd.capnp"),
		filepath.Join(t.TempDir(), "traefik.yml"),
		"http://platformd/internal/auth/verify",
		"127.0.0.1",
	))
	server := NewServer(service)
	app := createApp(t, server, "Draft App", "draft.example.com")

	var detail platform.WorkerDetail
	requestJSON(t, server, http.MethodGet, "/v1/apps/"+app.ID, http.StatusOK, &detail)
	if detail.Deployment != nil {
		t.Fatalf("unexpected deployment: %#v", detail.Deployment)
	}
	var files []platform.WorkerFile
	requestJSON(t, server, http.MethodGet, "/v1/apps/"+app.ID+"/files", http.StatusOK, &files)
	if len(files) != 0 {
		t.Fatalf("unexpected files: %#v", files)
	}
	requestJSON(t, server, http.MethodGet, "/v1/apps/missing", http.StatusNotFound, &map[string]string{})
}

func TestListWorkerDeploymentsIncludesInactiveRecords(t *testing.T) {
	dir := t.TempDir()
	service := platform.NewService(platform.NewStore(), config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://platformd/internal/auth/verify",
		"127.0.0.1",
	))
	server := NewServer(service)
	app := createApp(t, server, "Ledger App", "ledger.example.com")
	first := deployContent(t, server, app.ID, []platform.WorkerFile{{Path: "first.js", Content: "first"}}, "")
	second := deployContent(t, server, app.ID, []platform.WorkerFile{{Path: "second.js", Content: "second"}}, "")

	var deployments []platform.ConsoleDeployment
	requestJSON(t, server, http.MethodGet, "/v1/apps/"+app.ID+"/deployments", http.StatusOK, &deployments)
	if len(deployments) != 2 {
		t.Fatalf("deployments = %#v, want two records", deployments)
	}
	states := map[string]string{}
	for _, deployment := range deployments {
		states[deployment.ID] = deployment.State
	}
	if states[first.ID] != "inactive" || states[second.ID] != "active" {
		t.Fatalf("deployment states = %#v, want first inactive and second active", states)
	}
	requestJSON(t, server, http.MethodGet, "/v1/apps/missing/deployments", http.StatusNotFound, &map[string]string{})
}

func createApp(t *testing.T, server http.Handler, name, hostname string) platform.App {
	t.Helper()
	body := `{"name":"` + name + `","hostname":"` + hostname + `"}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/apps", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create app status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var app platform.App
	if err := json.NewDecoder(recorder.Body).Decode(&app); err != nil {
		t.Fatal(err)
	}
	if len(app.ID) != 48 {
		t.Fatalf("generated app id = %q, want 48 character token", app.ID)
	}
	return app
}

func deploy(t *testing.T, server http.Handler, appID string) platform.Deployment {
	t.Helper()
	return deployContent(t, server, appID, []platform.WorkerFile{{Path: "worker.js", Content: `addEventListener("fetch", () => {});`}}, "")
}

func deployContent(t *testing.T, server http.Handler, appID string, files []platform.WorkerFile, entrypoint string) platform.Deployment {
	t.Helper()
	body, err := json.Marshal(platform.DeployInput{Files: files, Entrypoint: entrypoint, CompatibilityDate: "2026-05-31"})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/apps/"+appID+"/deployments", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("deploy status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var deployment platform.Deployment
	if err := json.NewDecoder(recorder.Body).Decode(&deployment); err != nil {
		t.Fatal(err)
	}
	if len(deployment.ID) != 48 {
		t.Fatalf("generated deployment id = %q, want 48 character token", deployment.ID)
	}
	return deployment
}

func requestJSON(t *testing.T, server http.Handler, method, path string, wantStatus int, target any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s status = %d, body = %s", method, path, recorder.Code, recorder.Body.String())
	}
	if err := json.NewDecoder(recorder.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

type fakeOutput struct{}

func (fakeOutput) Output(string) []platform.WorkerOutputLine {
	return []platform.WorkerOutputLine{{Timestamp: time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC), Level: "info", Message: "runtime ready"}}
}

type fakeTraffic struct{}

func (fakeTraffic) Traffic(string) (platform.WorkerTraffic, error) {
	return platform.WorkerTraffic{Available: true, RequestsPerSecond: 4.25, Traffic: []float64{3, 4}}, nil
}

func request(t *testing.T, server http.Handler, method, path, capability, body string, wantStatus int) map[string]any {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	if capability != "" {
		request.Header.Set("Authorization", "Bearer "+capability)
	}
	server.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s status = %d, body = %s", method, path, recorder.Code, recorder.Body.String())
	}
	if recorder.Body.Len() == 0 {
		return nil
	}
	var response map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}
