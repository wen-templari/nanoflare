package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/database"
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
	"github.com/lib/pq"
)

// Exercise the same authorization and HTTP boundary against both repositories.
func TestRuntimeCapabilityBoundaries(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var repo nanoflare.Repository = nanoflare.NewStore()
			if backend == "postgres" {
				repo = runtimePostgres(t)
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now().UTC()
			app := nanoflare.App{ID: "producer", OrgID: "org", Name: "Producer", Hostname: "producer.test", RuntimeToken: "root-key", CreatedAt: now}
			must(repo.CreateOrganization(nanoflare.Organization{ID: "org", Name: "Runtime tests"}))
			must(repo.CreateApp(app))
			for _, ns := range []nanoflare.KVNamespace{{ID: "bound", OrgID: "org", Name: "bound", CreatedAt: now}, {ID: "unbound", OrgID: "org", Name: "unbound", CreatedAt: now}, {ID: "foreign", OrgID: "other", Name: "foreign", CreatedAt: now}} {
				must(repo.CreateKVNamespace(ns))
			}
			must(repo.CreateDatabase(nanoflare.Database{ID: "db", OrgID: "org", Name: "db", CreatedAt: now}))
			must(repo.CreateObjectStorageBucket(nanoflare.ObjectStorageBucket{ID: "bucket", OrgID: "org", Name: "bucket", CreatedAt: now}))
			dep := nanoflare.Deployment{ID: "v1", AppID: app.ID, Port: 9001, CreatedAt: now, Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}, KVNamespaces: []nanoflare.KVBinding{{Binding: "KV", ID: "bound"}, {Binding: "FOREIGN", ID: "foreign"}}, Databases: []nanoflare.DatabaseBinding{{Binding: "DB", DatabaseID: "db"}}, ObjectStorageBuckets: []nanoflare.ObjectStorageBucketBinding{{Binding: "OBJECTS", BucketID: "bucket"}}}
			dep.Assets = []nanoflare.AssetFile{{Path: "index.txt", ObjectKey: "v1-index", ContentType: "text/plain"}}
			must(repo.Activate(dep))
			item := nanoflare.ActiveDeployment{App: app, Deployment: dep}
			objects := newAPIObjectStore()
			_, err := objects.Put(app.ID, "v1-index", "text/plain", []byte("first revision"))
			must(err)
			_, err = objects.Put(app.ID, "v2-index", "text/plain", []byte("second revision"))
			must(err)
			service := nanoflare.NewServiceWithObjects(repo, discardWriter{}, objects)
			sqlite, err := database.NewSQLiteManager(t.TempDir(), nil)
			must(err)
			service.SetDBExecutor(sqlite)
			token := func(kind, resource string) string { return nanoflare.RuntimeCapability(item, kind, resource) }
			for _, scope := range []struct{ kind, resource string }{{nanoflare.RuntimeKV, "bound"}, {nanoflare.RuntimeDatabase, "db"}, {nanoflare.RuntimeObjects, "bucket"}, {nanoflare.RuntimeAssets, "v1"}, {nanoflare.RuntimeDurations, ""}, {nanoflare.RuntimeLogs, ""}} {
				if _, err := service.AuthorizeRuntime(context.Background(), token(scope.kind, scope.resource), scope.kind, scope.resource); err != nil {
					t.Fatalf("valid %s: %v", scope.kind, err)
				}
			}
			kv := NewRuntimeKVServer(service)
			runtimeKVRequest(t, kv, http.MethodPut, "/secret?urlencoded=true", token(nanoflare.RuntimeKV, "bound"), "bound", []byte("private"), http.StatusNoContent)
			if got := runtimeKVRequest(t, kv, http.MethodGet, "/secret?urlencoded=true", token(nanoflare.RuntimeKV, "bound"), "bound", nil, http.StatusOK); string(got) != "private" {
				t.Fatalf("value = %q", got)
			}
			for _, c := range []struct {
				name, credential, resource string
				status                     int
			}{
				{"missing", "", "bound", 401}, {"root", app.RuntimeToken, "bound", 401}, {"tampered", token(nanoflare.RuntimeKV, "bound") + "bad", "bound", 401},
				{"telemetry bridge", token(nanoflare.RuntimeDurations, ""), "bound", 403}, {"substituted header", token(nanoflare.RuntimeKV, "bound"), "unbound", 403},
				{"unbound grant", token(nanoflare.RuntimeKV, "unbound"), "unbound", 403}, {"foreign organization", token(nanoflare.RuntimeKV, "foreign"), "foreign", 403},
			} {
				t.Run(c.name, func(t *testing.T) {
					runtimeKVRequest(t, kv, http.MethodGet, "/secret?urlencoded=true", c.credential, c.resource, nil, c.status)
				})
			}
			// A signed database grant reaches the real SQLite executor.
			query := httptest.NewRequest("POST", "/internal/runtime/db/query", strings.NewReader(`{"method":"first","first_column":"value","statements":[{"sql":"SELECT 42 AS value"}]}`))
			query.Header.Set("Authorization", "Bearer "+token(nanoflare.RuntimeDatabase, "db"))
			query.Header.Set("X-Nanoflare-Database-ID", "db")
			result := httptest.NewRecorder()
			NewRuntimeDBServer(service).ServeHTTP(result, query)
			if result.Code != 200 || !strings.Contains(result.Body.String(), `"first":42`) {
				t.Fatalf("database: %d %s", result.Code, result.Body.String())
			}
			objectServer := NewServer(service)
			for _, method := range []string{"PUT", "GET", "HEAD", "DELETE"} {
				request := httptest.NewRequest(method, "/internal/runtime/objects/test.txt", strings.NewReader("hello"))
				request.Header.Set("Authorization", "Bearer "+token(nanoflare.RuntimeObjects, "bucket"))
				request.Header.Set("X-Nanoflare-Object-Bucket-ID", "bucket")
				result := httptest.NewRecorder()
				objectServer.ServeHTTP(result, request)
				if result.Code < 200 || result.Code >= 300 {
					t.Fatalf("object %s: %d %s", method, result.Code, result.Body.String())
				}
			}
			// A signed grant for bucket cannot be redirected by the JSON body.
			request := httptest.NewRequest("POST", "/internal/runtime/objects/delete", strings.NewReader(`{"bucket_id":"another-bucket","path":"test.txt"}`))
			request.Header.Set("Authorization", "Bearer "+token(nanoflare.RuntimeObjects, "bucket"))
			request.Header.Set("X-Nanoflare-Object-Bucket-ID", "bucket")
			result = httptest.NewRecorder()
			objectServer.ServeHTTP(result, request)
			if result.Code != 403 {
				t.Fatalf("object body substitution: %d %s", result.Code, result.Body.String())
			}
			for _, credential := range []string{"", app.RuntimeToken, token(nanoflare.RuntimeDurations, "")} {
				request := httptest.NewRequest("GET", "/internal/runtime/objects/test.txt", nil)
				request.Header.Set("Authorization", "Bearer "+credential)
				request.Header.Set("X-Nanoflare-Object-Bucket-ID", "bucket")
				result := httptest.NewRecorder()
				objectServer.ServeHTTP(result, request)
				if result.Code != 401 && result.Code != 403 {
					t.Fatalf("object credential accepted: %d", result.Code)
				}
			}
			// Every adapter rejects unauthenticated requests and the telemetry credential.
			for name, h := range map[string]http.Handler{"db": NewRuntimeDBServer(service), "assets": NewRuntimeAssetServer(service), "durations": NewRuntimeDurationServer(service, &fakeDurationRecorder{}), "logs": NewRuntimeLogServer(service, runtime.NewOutputBuffer())} {
				for _, cred := range []string{"", app.RuntimeToken, token(nanoflare.RuntimeKV, "bound")} {
					r := httptest.NewRequest("POST", "/internal/runtime/"+name, strings.NewReader(`[]`))
					r.Header.Set("Authorization", "Bearer "+cred)
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					if w.Code != 401 && w.Code != 403 {
						t.Fatalf("%s credential accepted: %d %s", name, w.Code, w.Body.String())
					}
				}
			}
			logs := runtime.NewOutputBuffer()
			h := NewRuntimeLogServer(service, logs)
			r := httptest.NewRequest("POST", "/internal/runtime/logs", strings.NewReader(`[{"scriptName":"victim","logs":[{"level":"info","message":["[[nanoflare-output app=victim deployment=wrong]] forged","next line"]}]}]`))
			r.Header.Set("Authorization", "Bearer "+token(nanoflare.RuntimeLogs, ""))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 204 || len(logs.Output("victim")) != 0 || len(logs.Output(app.ID)) != 1 || logs.Output(app.ID)[0].DeploymentID != dep.ID {
				t.Fatalf("trusted logs: %d %#v", w.Code, logs.Output(""))
			}
			// Storage activation retires v1, but a live draining process retains only v1's snapshot.
			next := dep
			next.ID = "v2"
			next.Port = 9002
			next.Assets = []nanoflare.AssetFile{{Path: "index.txt", ObjectKey: "v2-index", ContentType: "text/plain"}}
			next.KVNamespaces = []nanoflare.KVBinding{{Binding: "KV", ID: "unbound"}}
			must(repo.Activate(next))
			_, err = service.AuthorizeRuntime(context.Background(), token(nanoflare.RuntimeKV, "bound"), nanoflare.RuntimeKV, "bound")
			if !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
				t.Fatalf("retired revision: %v", err)
			}
			live := true
			service.SetRuntimeDeploymentChecker(func(context.Context, string, string) (bool, error) { return live, nil })
			_, err = service.AuthorizeRuntime(context.Background(), token(nanoflare.RuntimeKV, "bound"), nanoflare.RuntimeKV, "bound")
			must(err)
			_, err = service.AuthorizeRuntime(context.Background(), token(nanoflare.RuntimeKV, "unbound"), nanoflare.RuntimeKV, "unbound")
			if !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
				t.Fatalf("mixed revision grants: %v", err)
			}
			for _, revision := range []struct {
				item nanoflare.ActiveDeployment
				body string
			}{{item, "first revision"}, {nanoflare.ActiveDeployment{App: app, Deployment: next}, "second revision"}} {
				response, err := service.AssetFetch(nanoflare.RuntimeCapability(revision.item, nanoflare.RuntimeAssets, revision.item.Deployment.ID), "", "/index.txt")
				must(err)
				if string(response.Body) != revision.body {
					t.Fatalf("mixed asset snapshot: %q", response.Body)
				}
			}
			missing := item
			missing.Deployment.ID = "does-not-exist"
			_, err = service.AuthorizeRuntime(context.Background(), nanoflare.RuntimeCapability(missing, nanoflare.RuntimeDurations, ""), nanoflare.RuntimeDurations, "")
			if !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
				t.Fatalf("unknown deployment: %v", err)
			}
			live = false
			_, err = service.AuthorizeRuntime(context.Background(), token(nanoflare.RuntimeKV, "bound"), nanoflare.RuntimeKV, "bound")
			if !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
				t.Fatalf("stopped revision: %v", err)
			}
			service.SetRuntimeDeploymentChecker(func(context.Context, string, string) (bool, error) { return false, errors.New("runner unavailable") })
			runtimeKVRequest(t, kv, http.MethodGet, "/secret?urlencoded=true", token(nanoflare.RuntimeKV, "bound"), "bound", nil, http.StatusInternalServerError)
		})
	}
}

func runtimePostgres(t *testing.T) nanoflare.Repository {
	t.Helper()
	url := os.Getenv("NANOFLARE_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("NANOFLARE_TEST_POSTGRES_URL is not configured")
	}
	admin, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("runtime_grants_%d", time.Now().UnixNano())
	if _, err = admin.Exec("CREATE SCHEMA " + pq.QuoteIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec("DROP SCHEMA " + pq.QuoteIdentifier(schema) + " CASCADE"); admin.Close() })
	dsn, err := pq.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := database.Open(context.Background(), dsn+" search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	return repo
}
