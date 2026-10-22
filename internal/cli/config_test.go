package cli

import (
	"bytes"
	"github.com/clas/nanoflare/internal/nanoflare"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWranglerConfigFormats(t *testing.T) {
	for _, name := range []string{"nanoflare.json", "nanoflare.jsonc", "nanoflare.toml", "wrangler.json", "wrangler.jsonc", "wrangler.toml"} {
		t.Run(name, func(t *testing.T) {
			withWorkingDirectory(t, t.TempDir())
			content := `{"name":"portable","main":"worker.ts","compatibility_date":"2026-08-01","vars":{"HELLO":{"nested":true}},"kv_namespaces":[{"binding":"KV","id":"cloudflare-kv"}],"d1_databases":[{"binding":"DB","database_id":"cloudflare-db","database_name":"db"}],"r2_buckets":[{"binding":"BUCKET","bucket_name":"bucket"}]}`
			if filepath.Ext(name) == ".jsonc" {
				content = "// comment\n" + strings.Replace(content, `"name":"portable",`, `"name":"portable", /* comment */`, 1)
				content = strings.TrimSuffix(content, "}") + ",}"
			}
			if filepath.Ext(name) == ".toml" {
				content = `name = "portable"
main = "worker.ts"
compatibility_date = "2026-08-01"
[vars]
HELLO = { nested = true }
[[kv_namespaces]]
binding = "KV"
id = "cloudflare-kv"
[[d1_databases]]
binding = "DB"
database_id = "cloudflare-db"
database_name = "db"
[[r2_buckets]]
binding = "BUCKET"
bucket_name = "bucket"
`
			}
			if err := os.WriteFile(name, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile("worker.ts", []byte(`import { message } from "./message"; export default { fetch(): Response { return new Response(message); } };`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile("message.ts", []byte(`export const message: string = "portable worker";`), 0600); err != nil {
				t.Fatal(err)
			}
			path, project, err := loadDeploymentProject()
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Base(path) != name || len(project.KVNamespaces) != 1 || project.KVNamespaces[0].ID != "" || len(project.Databases) != 1 || project.Databases[0].DatabaseID != "" || len(project.ObjectStorageBuckets) != 1 {
				t.Fatalf("project: %#v", project)
			}
			files, err := projectWorkerFiles(project)
			if err != nil || len(files) != 1 || !strings.Contains(files[0].Content, "portable worker") || strings.Contains(files[0].Content, ": Response") {
				t.Fatalf("bundle: %#v, %v", files, err)
			}
			runner := NewRunner(io.Discard, io.Discard)
			if err := runner.Run([]string{"check"}); err != nil {
				t.Fatal(err)
			}
			if err := runner.Run([]string{"types"}); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(name)
			if err != nil || !bytes.Equal(after, []byte(content)) {
				t.Fatal("config was modified")
			}
		})
	}
}

func TestWranglerRejectsUnsupportedSettings(t *testing.T) {
	_, err := parseProjectConfig("wrangler.json", []byte(`{"name":"test","main":"worker.js","compatibility_date":"2026-08-01","durable_objects":{"bindings":[{"name":"DO","class_name":"Counter"}]}}`), false)
	if err == nil || !strings.Contains(err.Error(), "durable_objects") {
		t.Fatalf("error: %v", err)
	}
}

func TestNativeConfigTakesPrecedence(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	writeProjectFile(t, Project{Name: "native", Main: "worker.js", CompatibilityDate: "2026-08-01"})
	if err := os.WriteFile("wrangler.json", []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, project, err := loadProject()
	if err != nil || project.Name != "native" {
		t.Fatalf("project: %#v, %v", project, err)
	}
}

func TestDeployWranglerProject(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	config := `{"name":"portable","main":"worker.ts","compatibility_date":"2026-08-01","vars":{"GREETING":"hello"},"assets":{"directory":"public","binding":"ASSETS"},"dispatch_namespaces":[{"binding":"DISPATCHER","namespace":"customers"}]}`
	for path, content := range map[string]string{"wrangler.jsonc": config, "worker.ts": `export default {fetch(): Response {return new Response("hello")}}`} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir("public", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("public/index.html", []byte("hello asset"), 0600); err != nil {
		t.Fatal(err)
	}
	var deployed nanoflare.DeployInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/workers":
			writeJSON(t, w, http.StatusOK, []nanoflare.App{{ID: "portable-id", Name: "portable"}})
		case "/v1/dispatch-namespaces":
			writeJSON(t, w, http.StatusOK, []nanoflare.DispatchNamespace{{ID: "ns-id", Name: "customers"}})
		case "/v1/workers/portable-id":
			w.WriteHeader(http.StatusOK)
		case "/v1/workers/portable-id/deployments":
			decodeRequest(t, r, &deployed)
			writeJSON(t, w, http.StatusCreated, nanoflare.Deployment{ID: "deployment", CompatibilityDate: "2026-08-01"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runner := NewRunner(io.Discard, io.Discard)
	if err := runner.Run([]string{"deploy", "--api-url", server.URL}); err != nil {
		t.Fatal(err)
	}
	if len(deployed.DispatchNamespaces) != 1 || deployed.DispatchNamespaces[0].NamespaceID != "ns-id" {
		t.Fatalf("dispatch payload: %#v", deployed.DispatchNamespaces)
	}
	if deployed.Entrypoint != "worker.mjs" || deployed.Format != "modules" || len(deployed.Files) != 1 || !strings.Contains(deployed.Files[0].Content, "hello") || len(deployed.Assets) != 1 || deployed.AssetConfig.Binding != "ASSETS" || string(deployed.Vars["GREETING"]) != `"hello"` {
		t.Fatalf("deployment: %#v", deployed)
	}
}

func TestWranglerRejectsNestedUnsupportedBehavior(t *testing.T) {
	for _, settings := range []string{`"services":[{"binding":"API","service":"api","entrypoint":"Special"}]`, `"assets":{"directory":"public","experimental_serve_directly":true}`} {
		_, err := parseProjectConfig("wrangler.json", []byte(`{"name":"test","main":"worker.js","compatibility_date":"2026-08-01",`+settings+`}`), false)
		if err == nil || !strings.Contains(err.Error(), "unsupported Wrangler setting") {
			t.Fatalf("error: %v", err)
		}
	}
}

func TestSharedConfigExtensionsAndDispatch(t *testing.T) {
	for _, filename := range []string{"nanoflare.json", "wrangler.json"} {
		t.Run(filename, func(t *testing.T) {
			withWorkingDirectory(t, t.TempDir())
			content := `{"name":"router","main":"worker.js","compatibility_date":"2026-08-01","d1_databases":[{"binding":"DB","database_id":"cf-db"}],"r2_buckets":[{"binding":"R2","bucket_name":"cf-bucket"}],"dispatch_namespaces":[{"binding":"DISPATCHER","namespace":"customers"}],"nanoflare":{"worker_id":"worker-id","dns":{"profile":"private"},"auth":{"protected_routes":["/admin/*"]},"secrets":{"required":["KEY"]},"resource_ids":{"DB":"nf-db","R2":"nf-bucket"}}}`
			if err := os.WriteFile(filename, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			_, project, err := loadProject()
			if err != nil {
				t.Fatal(err)
			}
			if project.WorkerID != "worker-id" || project.DNS.Profile != "private" || project.Databases[0].DatabaseID != "nf-db" || project.ObjectStorageBuckets[0].BucketID != "nf-bucket" || len(project.Secrets.Required) != 1 {
				t.Fatalf("project: %#v", project)
			}
			types, err := generateWorkerTypes(project, "Env")
			if err != nil || !strings.Contains(string(types), "DISPATCHER: NanoflareDispatchNamespace") {
				t.Fatalf("types: %s, %v", types, err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/dispatch-namespaces" {
					http.NotFound(w, r)
					return
				}
				writeJSON(t, w, http.StatusOK, []nanoflare.DispatchNamespace{{ID: "nf-namespace", Name: "customers"}})
			}))
			defer server.Close()
			runner := NewRunner(io.Discard, io.Discard)
			if err := runner.resolveProjectBindings(server.URL, &project, false); err != nil {
				t.Fatal(err)
			}
			if project.DispatchNamespaces[0].NamespaceID != "nf-namespace" {
				t.Fatalf("dispatch: %#v", project.DispatchNamespaces)
			}
			after, _ := os.ReadFile(filename)
			if string(after) != content {
				t.Fatal("source config modified")
			}
		})
	}
}

func TestSharedConfigRejectsConflicts(t *testing.T) {
	for _, fields := range []string{
		`"db":[],"d1_databases":[]`,
		`"object_storage_buckets":[],"r2_buckets":[]`,
		`"worker_id":"a","nanoflare":{"worker_id":"b"}`,
		`"dispatch_namespaces":[{"binding":"D","namespace":"name","namespace_id":"id"}]`,
		`"dispatch_namespaces":[{"binding":"D"}]`,
		`"dispatch_namespaces":[{"binding":"D","namespace":"name","outbound":{"service":"outbound"}}]`,
	} {
		_, _, err := decodeProjectConfig("nanoflare.json", []byte(`{"name":"test","main":"worker.js","compatibility_date":"2026-08-01",`+fields+`}`))
		if err == nil {
			t.Fatalf("accepted invalid fields: %s", fields)
		}
	}
}

func TestLegacyTOMLMigrationPreservesPortableMetadata(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	content := `name = "router"
entrypoint = "worker.js"
compatibility_date = "2026-08-01"
[[dispatch_namespaces]]
binding = "DISPATCHER"
namespace = "customers"
[nanoflare.resource_ids]
DISPATCHER = "ns-id"
`
	if err := os.WriteFile("nanoflare.toml", []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_, project, err := loadProject()
		if err != nil {
			t.Fatal(err)
		}
		if project.Main != "worker.js" || project.DispatchNamespaces[0].NamespaceID != "ns-id" {
			t.Fatalf("project: %#v", project)
		}
	}
}

func TestViteManifestRetainsSourceResourceResolution(t *testing.T) {
	withWorkingDirectory(t, t.TempDir())
	content := `{"name":"router","main":"worker.ts","compatibility_date":"2026-08-01","kv_namespaces":[{"binding":"KV","id":"cf-kv"}],"dispatch_namespaces":[{"binding":"DISPATCHER","namespace":"customers"}]}`
	if err := os.WriteFile("wrangler.json", []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("dist/router", 0700); err != nil {
		t.Fatal(err)
	}
	manifest := strings.TrimSuffix(content, "}") + `,"main":"dist/router/worker.mjs","files":["dist/router/worker.mjs"],"format":"modules"}`
	if err := os.WriteFile("dist/router/nanoflare.json", []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	_, project, err := loadDeploymentProject()
	if err != nil {
		t.Fatal(err)
	}
	if project.KVNamespaces[0].ID != "" || project.dispatchNamespaceNames["DISPATCHER"] != "customers" || project.Main != "dist/router/worker.mjs" {
		t.Fatalf("project: %#v", project)
	}
}
