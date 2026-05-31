package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/clas/platform/internal/config"
	"github.com/clas/platform/internal/platform"
)

func TestCreateDeployAndScopedKV(t *testing.T) {
	dir := t.TempDir()
	service := platform.NewService(platform.NewStore(), config.NewWriter(
		filepath.Join(dir, "workerd.capnp"),
		filepath.Join(dir, "traefik.yml"),
		"http://platformd/internal/auth/verify",
	))
	server := NewServer(service)

	createApp(t, server, "app-one", "one.example.com")
	createApp(t, server, "app-two", "two.example.com")
	one := deploy(t, server, "app-one")
	two := deploy(t, server, "app-two")

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

func createApp(t *testing.T, server http.Handler, id, hostname string) {
	t.Helper()
	body := `{"id":"` + id + `","hostname":"` + hostname + `"}`
	request(t, server, http.MethodPost, "/v1/apps", "", body, http.StatusCreated)
}

func deploy(t *testing.T, server http.Handler, appID string) platform.Deployment {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/apps/"+appID+"/deployments", bytes.NewBufferString(
		`{"bundle_path":"/srv/apps/`+appID+`/worker.js","compatibility_date":"2026-05-31"}`,
	))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("deploy status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var deployment platform.Deployment
	if err := json.NewDecoder(recorder.Body).Decode(&deployment); err != nil {
		t.Fatal(err)
	}
	return deployment
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
