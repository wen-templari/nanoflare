package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

func TestRuntimeMuxRoutesObjectRequestsToObjectHandler(t *testing.T) {
	store := nanoflare.NewStore()
	service := nanoflare.NewServiceWithObjects(store, config.NewWriter("", "", "", ""), nil)
	server := api.NewServer(service)
	runtimeMux := newRuntimeMux(service, server, runtime.NewDurationTelemetry())
	defer runtimeMux.Close()

	request := httptest.NewRequest(http.MethodGet, "/internal/runtime/objects/demo.txt", nil)
	recorder := httptest.NewRecorder()
	runtimeMux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "missing object storage bucket id") {
		t.Fatalf("body = %q, want object storage bucket error", recorder.Body.String())
	}
}

func TestRuntimeExecutionControlIsPrivateAndAuthenticated(t *testing.T) {
	service := nanoflare.NewService(nanoflare.NewStore(), config.NewWriter("", "", "", ""))
	public := api.NewServer(service)
	private := newRuntimeMux(service, public, runtime.NewDurationTelemetry(), "runner-control")
	defer private.Close()
	for _, token := range []string{"", "wrong", "runner-control"} {
		request := httptest.NewRequest(http.MethodPost, "/internal/runtime/executions/open-session", strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Nanoflare-Runner-Token", token)
		response := httptest.NewRecorder()
		private.ServeHTTP(response, request)
		if token != "runner-control" && response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated private control status = %d", response.Code)
		}
		if token == "runner-control" && response.Code != http.StatusOK {
			t.Fatalf("authenticated private control status = %d: %s", response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/runtime/executions/open-session", strings.NewReader(`{}`))
	request.Header.Set("X-Nanoflare-Runner-Token", "runner-control")
	response := httptest.NewRecorder()
	public.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("execution control exposed publicly: %d", response.Code)
	}
}

func TestRuntimeResourcePrefixPreservesReservedKVKeys(t *testing.T) {
	store := nanoflare.NewStore()
	service := nanoflare.NewService(store, config.NewWriter("", "", "", ""))
	kv, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{Name: "reserved-paths"})
	if err != nil {
		t.Fatal(err)
	}
	private := newRuntimeMux(service, api.NewServer(service), runtime.NewDurationTelemetry(), "runner-control")
	defer private.Close()
	for _, key := range []string{"internal/runtime/durations", "internal/runtime/executions/open-session", "internal/runtime/objects/demo.txt"} {
		if err := store.KVPut("", kv.ID, key, []byte("resource-value")); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodGet, "/internal/runtime/resources/"+key+"?urlencoded=true", nil)
		request.Header.Set("X-Nanoflare-KV-Namespace-ID", kv.ID)
		response := httptest.NewRecorder()
		private.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != "resource-value" {
			t.Fatalf("reserved KV key %q: HTTP %d %s", key, response.Code, response.Body.String())
		}
	}
}
