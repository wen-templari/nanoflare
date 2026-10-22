package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestPlatformModelAndAuthorization(t *testing.T) {
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
			for _, id := range []string{"org", "other"} {
				must(repo.CreateOrganization(nanoflare.Organization{ID: id, Name: id, UsageLevel: nanoflare.UsageLevelPaid}))
			}
			service := nanoflare.NewService(repo, discardWriter{})
			ns, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "production", CustomerMode: "platform"})
			must(err)
			_, err = service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "production"})
			if !errors.Is(err, nanoflare.ErrDispatchNamespaceExists) {
				t.Fatalf("duplicate namespace: %v", err)
			}
			_, err = service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "unsafe", Trust: "untrusted"})
			if !errors.Is(err, nanoflare.ErrUnsupportedExecutionProfile) {
				t.Fatal(err)
			}
			_, err = service.DispatchNamespaceForOrg("other", ns.ID)
			if !errors.Is(err, nanoflare.ErrDispatchNamespaceNotFound) {
				t.Fatal(err)
			}
			_, err = service.CreateDispatchNamespace("other", nanoflare.CreateDispatchNamespaceInput{Name: "production"})
			must(err)
			second, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "staging", CustomerMode: "platform"})
			must(err)
			a, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", CustomerID: "customer-a"})
			must(err)
			b, err := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "beta", CustomerID: "customer-b"})
			must(err)
			_, err = service.CreateNamespaceScript("org", second.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", CustomerID: "customer-a"})
			must(err)
			if !a.IsPrivate() || a.Hostname != "" || b.Hostname != "" || a.ID == b.ID {
				t.Fatalf("private identities: %#v %#v", a, b)
			}
			_, err = service.CreateNamespaceScript("other", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "stolen", CustomerID: "customer-a"})
			if !errors.Is(err, nanoflare.ErrDispatchNamespaceNotFound) {
				t.Fatal(err)
			}
			_, err = service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "no-customer"})
			if err == nil {
				t.Fatal("missing customer accepted")
			}
			_, err = service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", CustomerID: "customer-b"})
			if !errors.Is(err, nanoflare.ErrDispatchNamespaceExists) {
				t.Fatal(err)
			}
			if err := service.DeleteDispatchNamespace("org", ns.ID); !errors.Is(err, nanoflare.ErrDispatchNamespaceInUse) {
				t.Fatal(err)
			}
			host := "public.example.com"
			_, err = service.UpdateApp(a.ID, nanoflare.UpdateAppInput{Hostname: &host})
			if !errors.Is(err, nanoflare.ErrInvalidAppUpdate) {
				t.Fatal(err)
			}
			// Editing a returned object cannot mutate stored trust/ownership.
			stored, err := repo.GetApp(a.ID)
			must(err)
			stored.Platform.Exposure = "public"
			again, err := repo.GetApp(a.ID)
			must(err)
			if !again.IsPrivate() {
				t.Fatal("mutable platform pointer")
			}
			kv, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{OrgID: "org", Name: "private"})
			must(err)
			foreign, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{OrgID: "other", Name: "private"})
			must(err)
			_, err = service.SetNamespaceGrants("org", ns.ID, nanoflare.NamespaceGrantsInput{ExpectedRevision: 1, Grants: []nanoflare.NamespaceResourceGrant{{Kind: "kv", ResourceID: foreign.ID, CustomerID: "customer-a"}}})
			if !errors.Is(err, nanoflare.ErrNamespaceGrantDenied) {
				t.Fatal(err)
			}
			db, err := service.CreateDatabase(nanoflare.CreateDatabaseInput{OrgID: "org", Name: "private"})
			must(err)
			bucket, err := service.CreateObjectStorageBucket(nanoflare.CreateObjectStorageBucketInput{OrgID: "org", Name: "private"})
			must(err)
			ns, err = service.SetNamespaceGrants("org", ns.ID, nanoflare.NamespaceGrantsInput{ExpectedRevision: 1, Grants: []nanoflare.NamespaceResourceGrant{{Kind: "kv", ResourceID: kv.ID, CustomerID: "customer-a"}, {Kind: "db", ResourceID: db.ID, CustomerID: "customer-a"}, {Kind: "objects", ResourceID: bucket.ID, CustomerID: "customer-a"}}})
			must(err)
			input := nanoflare.DeployInput{Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default {fetch(){return new Response("ok")}}`}}, Entrypoint: "worker.js", Format: "modules", CompatibilityDate: "2025-12-10", KVNamespaces: []nanoflare.KVBinding{{Binding: "KV", ID: kv.ID}}}
			input.Databases = []nanoflare.DatabaseBinding{{Binding: "DB", DatabaseID: db.ID}}
			input.ObjectStorageBuckets = []nanoflare.ObjectStorageBucketBinding{{Binding: "OBJECTS", BucketID: bucket.ID}}
			dep, err := service.DeployForOrg("org", a.ID, input)
			must(err)
			_, err = service.DeployForOrg("org", b.ID, input)
			if !errors.Is(err, nanoflare.ErrNamespaceGrantDenied) {
				t.Fatalf("cross customer deploy: %v", err)
			}
			grant := nanoflare.RuntimeCapability(nanoflare.ActiveDeployment{App: a, Deployment: dep}, "kv", kv.ID)
			_, err = service.AuthorizeRuntime(context.Background(), grant, "kv", kv.ID)
			must(err)
			for _, scope := range []struct{ kind, id string }{{"db", db.ID}, {"objects", bucket.ID}} {
				_, e := service.AuthorizeRuntime(context.Background(), nanoflare.RuntimeCapability(nanoflare.ActiveDeployment{App: a, Deployment: dep}, scope.kind, scope.id), scope.kind, scope.id)
				must(e)
			}
			active, err := repo.ActiveDeployments()
			must(err)
			if len(active) != 1 || !active[0].App.IsPrivate() {
				t.Fatalf("active private metadata: %#v", active)
			}
			generated := config.Traefik(active, "http://control/internal/auth", "auth.example.com", "runtime")
			if strings.Contains(generated, "Host(``)") || strings.Contains(generated, "/internal/http/workers/"+a.ID) {
				t.Fatal("private public route generated")
			}
			server := NewServer(service)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, httptest.NewRequest("GET", "/internal/http/workers/"+a.ID+"/", nil))
			if w.Code != 404 {
				t.Fatalf("private gateway: %d %s", w.Code, w.Body.String())
			}
			// Explicit ID service calls are limited to the same customer and namespace.
			plain := input
			plain.KVNamespaces = nil
			plain.Databases = nil
			plain.ObjectStorageBuckets = nil
			_, err = service.Deploy(b.ID, plain)
			must(err)
			plain.Services = []nanoflare.ServiceBinding{{Binding: "OTHER", WorkerID: b.ID}}
			_, err = service.Deploy(a.ID, plain)
			if !errors.Is(err, nanoflare.ErrNamespaceGrantDenied) {
				t.Fatalf("cross-customer service: %v", err)
			}
			// Policy CAS: exactly one concurrent writer wins and history remains immutable.
			var wg sync.WaitGroup
			results := make(chan error, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, e := service.SetNamespaceGrants("org", ns.ID, nanoflare.NamespaceGrantsInput{ExpectedRevision: ns.PolicyRevision, Grants: []nanoflare.NamespaceResourceGrant{}})
					results <- e
				}()
			}
			wg.Wait()
			close(results)
			wins := 0
			for e := range results {
				if e == nil {
					wins++
				} else if !errors.Is(e, nanoflare.ErrNamespacePolicyConflict) {
					t.Fatal(e)
				}
			}
			if wins != 1 {
				t.Fatalf("policy winners %d", wins)
			}
			_, err = service.AuthorizeRuntime(context.Background(), grant, "kv", kv.ID)
			if !errors.Is(err, nanoflare.ErrRuntimeGrantDenied) {
				t.Fatalf("revoked runtime grant: %v", err)
			}
			history, err := service.NamespacePoliciesForOrg("org", ns.ID)
			must(err)
			if len(history) != 3 || len(history[1].Grants) != 3 || len(history[2].Grants) != 0 {
				t.Fatalf("history: %#v", history)
			}
			renamed, err := service.RenameDispatchNamespace("org", ns.ID, "renamed")
			must(err)
			if renamed.ID != ns.ID {
				t.Fatal("namespace identity changed")
			}
			// Worker names remain stable immutable IDs; creation race admits one script.
			var count int
			var mu sync.Mutex
			results = make(chan error, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, e := service.CreateNamespaceScript("org", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "raced", CustomerID: "customer-a"})
					if e == nil {
						mu.Lock()
						count++
						mu.Unlock()
					}
					results <- e
				}()
			}
			wg.Wait()
			close(results)
			for e := range results {
				if e != nil && !errors.Is(e, nanoflare.ErrDispatchNamespaceExists) {
					t.Fatal(e)
				}
			}
			if count != 1 {
				t.Fatal("duplicate script admitted")
			}
			apps, err := service.NamespaceScripts("org", ns.ID)
			must(err)
			for _, app := range apps {
				must(service.DeleteAppForOrg("org", app.ID))
			}
			must(service.DeleteDispatchNamespace("org", ns.ID))
			empty, err := service.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "delete-race"})
			must(err)
			start := make(chan struct{})
			created := make(chan error, 1)
			deleted := make(chan error, 1)
			go func() {
				<-start
				_, e := service.CreateNamespaceScript("org", empty.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "racing"})
				created <- e
			}()
			go func() { <-start; deleted <- service.DeleteDispatchNamespace("org", empty.ID) }()
			close(start)
			createErr, deleteErr := <-created, <-deleted
			if createErr == nil {
				if !errors.Is(deleteErr, nanoflare.ErrDispatchNamespaceInUse) {
					t.Fatalf("live member lost: %v", deleteErr)
				}
			} else if !errors.Is(createErr, nanoflare.ErrDispatchNamespaceNotFound) || deleteErr != nil {
				t.Fatalf("create/delete race: %v %v", createErr, deleteErr)
			}

		})
	}
}

func TestNamespaceAPIScopesAndCRUD(t *testing.T) {
	store := nanoflare.NewStore()
	store.CreateOrganization(nanoflare.Organization{ID: "org", Name: "org"})
	service := nanoflare.NewService(store, discardWriter{})
	server := NewServer(service)
	// Authenticated context fixture exercises real route scope guards; authentication
	// token verification itself is covered by the control-auth integration suite.
	server.controlAuth = nanoflare.NewControlAuthService(store, "fixture-secret")
	request := func(method, path, body, scope string, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, "/v1/organizations/org/dispatch-namespaces"+path, strings.NewReader(body))
		ctx := context.WithValue(r.Context(), controlOrgKey, "org")
		ctx = context.WithValue(ctx, controlScopesKey, strings.Fields(scope))
		w := httptest.NewRecorder()
		server.mux.ServeHTTP(w, r.WithContext(ctx))
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	request("POST", "", `{"name":"production"}`, "workers:write", 403)
	raw := request("POST", "", `{"name":"production"}`, "namespaces:write", 201)
	var ns nanoflare.DispatchNamespace
	json.Unmarshal(raw, &ns)
	request("PATCH", "/"+ns.ID, `{"trust":"untrusted"}`, "namespaces:write", 400)
	request("PUT", "/"+ns.ID+"/grants", `{"expected_revision":1,"grants":[]}`, "deployments:write namespaces:write", 403)
	request("PUT", "/"+ns.ID+"/grants", `{"expected_revision":1,"grants":[]}`, "namespaces:dispatch", 200)
	request("POST", "/"+ns.ID+"/scripts", `{"script_name":"customer"}`, "workers:write", 201)
	request("POST", "/"+ns.ID+"/scripts", `{"script_name":"platform","kind":"platform"}`, "workers:write", 403)
	request("GET", "/"+ns.ID+"/scripts/customer", "", "workers:read", 200)
	request("DELETE", "/"+ns.ID, "", "namespaces:write", 409)
	request("DELETE", "/"+ns.ID+"/scripts/customer", "", "workers:write", 204)
	request("DELETE", "/"+ns.ID, "", "namespaces:write", 204)
	if nanoflare.HasScope(nanoflare.RoleScopes(nanoflare.RoleMember), "namespaces:dispatch") {
		t.Fatal("ordinary deployment role acquired dispatch")
	}
}

func TestLegacyServiceAmbiguityFailsClosed(t *testing.T) {
	store := nanoflare.NewStore()
	service := nanoflare.NewService(store, discardWriter{})
	for i := 0; i < 2; i++ {
		app := nanoflare.App{ID: fmt.Sprint(i), Name: "duplicate", Hostname: fmt.Sprintf("%d.example.com", i), CreatedAt: time.Now().UTC()}
		if err := store.CreateApp(app); err != nil {
			t.Fatal(err)
		}
		if err := store.Activate(nanoflare.Deployment{ID: fmt.Sprint("dep", i), AppID: app.ID}); err != nil {
			t.Fatal(err)
		}
	}
	app, err := service.CreateApp(nanoflare.CreateAppInput{Name: "caller", Hostname: "caller.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Deploy(app.ID, nanoflare.DeployInput{Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}, Services: []nanoflare.ServiceBinding{{Binding: "TARGET", Service: "duplicate"}}})
	if !errors.Is(err, nanoflare.ErrAmbiguousService) {
		t.Fatalf("ambiguous name: %v", err)
	}
}
