package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestRuntimeKVSupportsNativeCoreOperations(t *testing.T) {
	store, token, namespaceID, server := runtimeKVFixture(t)
	_ = store

	runtimeKVRequest(t, server, http.MethodPut, "/message?urlencoded=true", token, namespaceID, []byte("hello"), http.StatusNoContent)
	if got := runtimeKVRequest(t, server, http.MethodGet, "/message?urlencoded=true", token, namespaceID, nil, http.StatusOK); string(got) != "hello" {
		t.Fatalf("GET body = %q, want hello", got)
	}
	jsonValue := []byte(`{"ok":true}`)
	runtimeKVRequest(t, server, http.MethodPut, "/json?urlencoded=true", token, namespaceID, jsonValue, http.StatusNoContent)
	if got := runtimeKVRequest(t, server, http.MethodGet, "/json?urlencoded=true", token, namespaceID, nil, http.StatusOK); !bytes.Equal(got, jsonValue) {
		t.Fatalf("GET JSON body = %q, want %q", got, jsonValue)
	}
	binary := []byte{0, 1, 2, 255}
	runtimeKVRequest(t, server, http.MethodPut, "/binary?urlencoded=true", token, namespaceID, binary, http.StatusNoContent)
	if got := runtimeKVRequest(t, server, http.MethodGet, "/binary?urlencoded=true", token, namespaceID, nil, http.StatusOK); !bytes.Equal(got, binary) {
		t.Fatalf("GET binary body = %v, want %v", got, binary)
	}
	runtimeKVRequest(t, server, http.MethodDelete, "/message?urlencoded=true", token, namespaceID, nil, http.StatusNoContent)
	runtimeKVRequest(t, server, http.MethodDelete, "/message?urlencoded=true", token, namespaceID, nil, http.StatusNoContent)
	runtimeKVRequest(t, server, http.MethodGet, "/message?urlencoded=true", token, namespaceID, nil, http.StatusNotFound)
}

func TestRuntimeKVRejectsInvalidRequests(t *testing.T) {
	_, token, namespaceID, server := runtimeKVFixture(t)
	runtimeKVRequest(t, server, http.MethodGet, "/missing?urlencoded=true", "wrong", namespaceID, nil, http.StatusUnauthorized)
	runtimeKVRequest(t, server, http.MethodGet, "/.?urlencoded=true", token, namespaceID, nil, http.StatusBadRequest)
	runtimeKVRequest(t, server, http.MethodGet, "/"+strings.Repeat("a", maxKVKeySize+1)+"?urlencoded=true", token, namespaceID, nil, http.StatusBadRequest)
	runtimeKVRequest(t, server, http.MethodPut, "/large?urlencoded=true", token, namespaceID, make([]byte, maxKVValueSize+1), http.StatusRequestEntityTooLarge)
	runtimeKVRequest(t, server, http.MethodGet, "/?prefix=a", token, namespaceID, nil, http.StatusNotImplemented)
	runtimeKVRequest(t, server, http.MethodPost, "/bulk/get", token, namespaceID, []byte(`{"keys":["a"]}`), http.StatusNotImplemented)
	runtimeKVRequest(t, server, http.MethodPut, "/ttl?urlencoded=true&expiration_ttl=60", token, namespaceID, []byte("value"), http.StatusNotImplemented)
}

func TestRuntimeTokenSurvivesRedeployAndIsNotPublic(t *testing.T) {
	store := nanoflare.NewStore()
	service := nanoflare.NewService(store, discardWriter{})
	server := NewServer(service)
	app := createApp(t, server, "Stable Token", "stable.example.com")
	first := deploy(t, server, app.ID)
	token := runtimeTokens(t, store)[app.ID]
	second := deploy(t, server, app.ID)
	if token == "" || runtimeTokens(t, store)[app.ID] != token {
		t.Fatal("runtime token changed across deployments")
	}
	for _, deployment := range []nanoflare.Deployment{first, second} {
		body := httptest.NewRecorder()
		writeJSON(body, http.StatusOK, deployment)
		if strings.Contains(body.Body.String(), token) || strings.Contains(body.Body.String(), "capability") {
			t.Fatalf("public deployment leaked runtime token: %s", body.Body.String())
		}
	}
}

func runtimeKVFixture(t *testing.T) (*nanoflare.Store, string, string, http.Handler) {
	t.Helper()
	store := nanoflare.NewStore()
	service := nanoflare.NewService(store, discardWriter{})
	app, err := service.CreateApp(nanoflare.CreateAppInput{Name: "KV App", Hostname: "kv.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{Name: "kv-app"})
	if err != nil {
		t.Fatal(err)
	}
	return store, app.RuntimeToken, namespace.ID, NewRuntimeKVServer(service)
}

type discardWriter struct{}

func (discardWriter) Write([]nanoflare.ActiveDeployment) error {
	return nil
}
