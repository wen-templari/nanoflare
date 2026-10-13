package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

func TestWorkerdVarsUpdateEndToEnd(t *testing.T) {
	workerd, err := exec.LookPath("workerd")
	if err != nil {
		t.Skip("workerd is not installed")
	}
	store := nanoflare.NewStore()
	if err := store.CreateOrganization(nanoflare.Organization{ID: "vars-org", Name: "Vars"}); err != nil {
		t.Fatal(err)
	}
	var service *nanoflare.Service
	runtimeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/runtime/durations" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		api.NewRuntimeKVServer(service).ServeHTTP(w, r)
	}))
	defer runtimeServer.Close()
	dir := t.TempDir()
	writer := config.NewRuntimeWriter(filepath.Join(dir, "workerd.capnp"), discardTraefik{})
	writer.SetNanoflareRuntimeAddr(strings.TrimPrefix(runtimeServer.URL, "http://"))
	manager := runtime.NewLazyManager(writer, runtime.CommandLauncher{Executable: workerd}, dir, "127.0.0.1", availablePort(t), 5*time.Second, 5*time.Second, time.Minute)
	defer manager.Close()
	service = nanoflare.NewService(store, manager)
	codec, err := nanoflare.NewSecretCodec("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	service.SetSecretCodec(codec)
	app, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "vars-org", Name: "Vars", Hostname: "runtime-vars.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PutSecret(app.ID, "SECRET", "preserved"); err != nil {
		t.Fatal(err)
	}
	kv, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{OrgID: "vars-org", Name: "Vars KV"})
	if err != nil {
		t.Fatal(err)
	}
	code := `export default { async fetch(request, env) {
 await env.KV.put("key", "binding works");
 return Response.json({text:env.TEXT??null,json:env.JSON??null,old:env.OLD??null,secret:env.SECRET,kv:await env.KV.get("key")});
} };`
	base, err := service.DeployForOrg("vars-org", app.ID, nanoflare.DeployInput{Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: code}}, Entrypoint: "worker.js", Format: "modules", CompatibilityDate: "2025-12-10", Vars: map[string]json.RawMessage{"OLD": json.RawMessage(`"original"`)}, KVNamespaces: []nanoflare.KVBinding{{Binding: "KV", ID: kv.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	server := api.NewServerWithRuntime(service, nil, "", nil, nil, manager)
	request := func(want string) {
		t.Helper()
		w := httptest.NewRecorder()
		server.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/internal/http/workers/"+app.ID+"/", nil))
		if w.Code != http.StatusOK || w.Body.String() != want {
			t.Fatalf("runtime=%d %s want=%s", w.Code, w.Body.String(), want)
		}
	}
	original := `{"text":null,"json":null,"old":"original","secret":"preserved","kv":"binding works"}`
	request(original)
	result, err := service.UpdateVarsForOrg("vars-org", app.ID, nanoflare.UpdateVarsInput{Vars: map[string]json.RawMessage{"TEXT": json.RawMessage(`"updated"`), "JSON": json.RawMessage(`{"beta":true}`)}, ExpectedDeploymentID: base.ID, IdempotencyKey: "runtime-update"})
	if err != nil {
		t.Fatal(err)
	}
	request(`{"text":"updated","json":{"beta":true},"old":null,"secret":"preserved","kv":"binding works"}`)
	_, err = service.UpdateVarsForOrg("vars-org", app.ID, nanoflare.UpdateVarsInput{Vars: map[string]json.RawMessage{}, ExpectedDeploymentID: result.ID, IdempotencyKey: "runtime-clear"})
	if err != nil {
		t.Fatal(err)
	}
	request(`{"text":null,"json":null,"old":null,"secret":"preserved","kv":"binding works"}`)
	// Existing traffic API restores the old revision and its variable snapshot.
	if _, err := service.SetDeploymentTrafficForOrg("vars-org", app.ID, []nanoflare.DeploymentTraffic{{ID: base.ID, TrafficPercent: 100}}); err != nil {
		t.Fatal(err)
	}
	request(original)
}
