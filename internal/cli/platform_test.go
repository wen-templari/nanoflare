package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestPlatformBundlePreservesCompleteProjectAndCannotOverwrite(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	source := `export default {fetch(){return new Response("local")}}`
	if err := os.WriteFile("worker.js", []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir("public", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("public/snapshot.txt", []byte("asset"), 0600); err != nil {
		t.Fatal(err)
	}
	project := Project{Name: "customer", WorkerID: "immutable-private-id", Main: "worker.js", Files: []string{"worker.js"}, Format: "modules", CompatibilityDate: "2025-12-10", Vars: map[string]json.RawMessage{"PLAN": json.RawMessage(`"test"`)}, Assets: ProjectAssets{Directory: "public"}, KVNamespaces: []nanoflare.KVBinding{{Binding: "STATE", ID: "kv-id"}}, DispatchNamespaces: []nanoflare.DispatchBinding{{Binding: "DISPATCHER", NamespaceID: "namespace-id"}}}
	data, _ := json.Marshal(project)
	if err := os.WriteFile(projectFilename, data, 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	runner := NewRunner(&stdout, &stderr)
	if err := runner.Run([]string{"platform", "bundle", "bundle.json"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("bundle.json")
	if err != nil {
		t.Fatal(err)
	}
	var bundle nanoflare.DeployInput
	if err = json.Unmarshal(data, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Files) != 1 || bundle.Files[0].Content != source || len(bundle.Assets) != 1 || string(bundle.Assets[0].Data) != "asset" || bundle.KVNamespaces[0].ID != "kv-id" || bundle.DispatchNamespaces[0].NamespaceID != "namespace-id" || bundle.IdempotencyKey == "" || string(bundle.Vars["PLAN"]) != `"test"` {
		t.Fatalf("incomplete bundle: %#v", bundle)
	}
	if strings.Contains(string(data), "immutable-private-id") {
		t.Fatal("bundle should contain deployment input, not mutable target selection")
	}
	if err = runner.Run([]string{"platform", "bundle", "bundle.json"}); err == nil {
		t.Fatal("existing deployment file was overwritten")
	}
	after, _ := os.ReadFile("bundle.json")
	if !bytes.Equal(data, after) {
		t.Fatal("existing bundle changed")
	}
	generated, err := generateWorkerTypes(project, "Env")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(generated), "DISPATCHER: NanoflareDispatchNamespace") || !strings.Contains(string(generated), "dispatchOptions?: Record<string, never>") {
		t.Fatalf("missing supported namespace types: %s", generated)
	}
}
func TestPrivateProjectTargetsExactWorkerWithoutPublicFallback(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	source := `export default {fetch(){return new Response("private")}}`
	if err := os.WriteFile("worker.js", []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	project := Project{Name: "ambiguous-name", WorkerID: "private-worker", Main: "worker.js", Files: []string{"worker.js"}, Format: "modules", CompatibilityDate: "2025-12-10"}
	data, _ := json.Marshal(project)
	if err := os.WriteFile(projectFilename, data, 0600); err != nil {
		t.Fatal(err)
	}
	published := false
	missing := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/workers/private-worker":
			if missing {
				http.Error(w, "not found", 404)
				return
			}
			if r.Method == "GET" {
				writeJSON(t, w, 200, nanoflare.WorkerDetail{App: nanoflare.App{ID: "private-worker", Name: "ambiguous-name", Platform: &nanoflare.WorkerPlatform{Exposure: "private", Execution: "dynamic"}}})
				return
			}
			if r.Method == "PATCH" {
				writeJSON(t, w, 200, map[string]any{})
				return
			}
		case "/v1/workers/private-worker/secrets":
			writeJSON(t, w, 200, []nanoflare.Secret{})
			return
		case "/v1/workers/private-worker/deployments":
			published = true
			var input nanoflare.DeployInput
			decodeRequest(t, r, &input)
			if len(input.Files) != 1 || input.Files[0].Content != source {
				t.Error("lost private project source")
			}
			writeJSON(t, w, 201, nanoflare.Deployment{ID: "revision", CompatibilityDate: "2025-12-10"})
			return
		case "/v1/workers":
			t.Error("an explicit private identity fell back to public name lookup or creation")
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	// Existing CLI tests use a temporary auth store. No real credentials are read.
	t.Setenv(authStorePathEnv, filepath.Join(t.TempDir(), "absent.json"))
	t.Setenv(authTokenEnv, "")
	t.Setenv(authOrgIDEnv, "")
	var stdout, stderr bytes.Buffer
	runner := NewRunner(&stdout, &stderr)
	if err := runner.Run([]string{"deploy", "--api-url", server.URL, "--provision=false"}); err != nil {
		t.Fatal(err)
	}
	if !published {
		t.Fatal("private Worker was not deployed")
	}
	missing = true
	published = false
	if err := runner.Run([]string{"deploy", "--api-url", server.URL, "--provision=false"}); err == nil {
		t.Fatal("missing explicit identity should fail")
	}
	if published {
		t.Fatal("missing identity was deployed")
	}
}
