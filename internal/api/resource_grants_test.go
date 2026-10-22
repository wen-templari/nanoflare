package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type heldResourceResponse struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	finish  chan struct{}
}

func (w *heldResourceResponse) Write(data []byte) (int, error) {
	close(w.entered)
	<-w.finish
	return w.ResponseRecorder.Write(data)
}

func TestResourceGrantOperationRetainsSnapshotUntilResponseFinishes(t *testing.T) {
	store := nanoflare.NewStore()
	service := nanoflare.NewService(store, discardWriter{})
	app, err := service.CreateApp(nanoflare.CreateAppInput{Name: "grants", Hostname: "grants.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	kv, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{Name: "grants"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.KVPut("", kv.ID, "hello", []byte("retained response")); err != nil {
		t.Fatal(err)
	}
	registry := service.Executions()
	registry.RenewResourceSession("session", time.Now().Add(time.Minute))
	grant, _, err := service.PrepareExecutionGrant(nanoflare.ActiveDeployment{App: app, Deployment: nanoflare.Deployment{ID: "old", AppID: app.ID, KVNamespaces: []nanoflare.KVBinding{{Binding: "KV", ID: kv.ID}}}}, "", "session", "registered")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.PublishResourceGrant("session", grant); err != nil {
		t.Fatal(err)
	}
	invocation, err := registry.Admit(grant)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/hello?urlencoded=true", nil)
	request.Header.Set("Authorization", "Bearer "+grant)
	request.Header.Set("X-Nanoflare-KV-Namespace-ID", kv.ID)
	response := &heldResourceResponse{httptest.NewRecorder(), make(chan struct{}), make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); NewRuntimeKVServer(service).ServeHTTP(response, request) }()
	select {
	case <-response.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("resource response never started")
	}
	if err := registry.RetireResourceGrant("session", grant); err != nil {
		t.Fatal(err)
	}
	invocation.Release()
	if !registry.ReferencesDeployment(app.ID, "old") {
		t.Fatal("resource response lost snapshot")
	}
	if _, err := registry.AcquireResource(grant, nanoflare.ExecutionKV, kv.ID); err == nil {
		t.Fatal("accepted response admitted another operation")
	}
	close(response.finish)
	<-done
	if response.Code != 200 || response.Body.String() != "retained response" {
		t.Fatal(response.Code, response.Body.String())
	}
	if registry.ReferencesDeployment(app.ID, "old") {
		t.Fatal("completed response retained snapshot")
	}
	if request.Header.Get("Authorization") != "Bearer "+grant {
		t.Fatal("handler modified caller's authorization")
	}
}
