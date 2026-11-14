package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/clas/nanoflare/internal/database"
	"github.com/lib/pq"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

// Exercise environment selection through compatible metadata and the actual
// production dispatch path. An environment name is resolved only at upload;
// retained revisions must carry the immutable child Worker ID.
func TestOutboundEnvironmentEndToEnd(t *testing.T) {
	if os.Getenv("NANOFLARE_TEST_WORKERD") == "" {
		t.Skip("pinned workerd required")
	}
	for _, dynamic := range []bool{false, true} {
		for _, remote := range []bool{false, true} {
			t.Run(fmt.Sprintf("dynamic=%t/remote=%t", dynamic, remote), func(t *testing.T) {
				privateDynamicDispatchWithFixture(t, remote, dynamic, func(server *Server, s *nanoflare.Service, org nanoflare.Organization, token string) {
					must := func(err error) {
						t.Helper()
						if err != nil {
							t.Fatal(err)
						}
					}
					server.SetCloudflareCompatibilityEnabled(true)
					base := "/client/v4/accounts/" + org.ID + "/workers/"
					request := func(method, path, kind string, body []byte) *httptest.ResponseRecorder {
						t.Helper()
						r := httptest.NewRequest(method, path, bytes.NewReader(body))
						r.Header.Set("Authorization", "Bearer "+token)
						r.Header.Set("Content-Type", kind)
						w := httptest.NewRecorder()
						server.ServeHTTP(w, r)
						return w
					}
					upload := func(path, metadata, code string) {
						t.Helper()
						kind, body := cfMultipart(t, metadata, code)
						w := request("PUT", path, kind, body)
						if w.Code != 200 {
							t.Fatalf("upload %s: %d %s", path, w.Code, w.Body.String())
						}
					}
					envBase := base + "services/policy/environments/"
					for _, env := range []string{"production", "staging"} {
						upload(envBase+env, `{"main_module":"worker.js","compatibility_date":"2026-09-11","bindings":[{"name":"TOKEN","type":"secret_text","text":"`+env+`-secret"}]}`, `export default {fetch(request,env){return new Response('`+env+`:'+env.TOKEN)}}`)
					}
					prod, err := s.OrdinaryServiceEnvironmentForOrg(org.ID, "policy", "production")
					must(err)
					stage, err := s.OrdinaryServiceEnvironmentForOrg(org.ID, "policy", "staging")
					must(err)
					if prod.ID == stage.ID {
						t.Fatal("environment identity collision")
					}
					ns, err := s.CreateDispatchNamespace(org.ID, nanoflare.CreateDispatchNamespaceInput{Name: "customers"})
					must(err)
					customer, err := s.CreateNamespaceScript(org.ID, ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", Execution: "dynamic"})
					must(err)
					_, err = s.Deploy(customer.ID, nanoflare.DeployInput{Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2026-09-11", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default {fetch(){return fetch('http://8.8.8.8/ok')}}`}}})
					must(err)
					hostCode := `export default {fetch(request,env){return env.CUSTOMERS.get('alpha').fetch(request)}}`
					metadata := func(env string) string {
						return `{"main_module":"worker.js","compatibility_date":"2026-09-11","bindings":[{"name":"CUSTOMERS","type":"dispatch_namespace","namespace":"customers","outbound":{"worker":{"service":"policy","environment":"` + env + `"}}}]}`
					}
					hostPath := base + "scripts/environment-host"
					upload(hostPath, metadata("production"), hostCode)
					host, err := s.OrdinaryScriptForOrg(org.ID, "environment-host")
					must(err)
					active, _, found, err := s.WorkerRuntimeDeployment(host.ID, "/")
					must(err)
					if !found {
						t.Fatal("host missing")
					}
					productionRevision := active.Deployment.ID
					public := httptest.NewServer(server)
					defer public.Close()
					invoke := func(want string) {
						t.Helper()
						r, e := public.Client().Get(public.URL + "/internal/http/workers/" + host.ID + "/")
						must(e)
						b, e := io.ReadAll(r.Body)
						r.Body.Close()
						must(e)
						if r.StatusCode != 200 || string(b) != want {
							t.Fatalf("invoke: %d %s; want %s", r.StatusCode, b, want)
						}
					}
					invoke("production:production-secret")
					upload(hostPath, metadata("staging"), hostCode)
					active, _, _, err = s.WorkerRuntimeDeployment(host.ID, "/")
					must(err)
					stagingRevision := active.Deployment.ID
					if active.Deployment.DispatchNamespaces[0].Outbound.WorkerID != stage.ID {
						t.Fatal("metadata failed to pin child identity")
					}
					invoke("staging:staging-secret")
					w := request("GET", hostPath+"/settings", "", nil)
					if w.Code != 200 || !strings.Contains(w.Body.String(), `"environment":"staging"`) {
						t.Fatalf("selector readback: %d %s", w.Code, w.Body.String())
					}
					// keep_bindings must preserve the child identity even when the upload contains
					// no new outbound metadata.
					upload(hostPath, `{"main_module":"worker.js","compatibility_date":"2026-09-11","keep_bindings":["dispatch_namespace"]}`, hostCode)
					active, _, _, err = s.WorkerRuntimeDeployment(host.ID, "/")
					must(err)
					if len(active.Deployment.DispatchNamespaces) != 1 || active.Deployment.DispatchNamespaces[0].Outbound.WorkerID != stage.ID {
						t.Fatal("keep_bindings lost child identity")
					}
					invoke("staging:staging-secret")
					upload(hostPath, `{"main_module":"worker.js","compatibility_date":"2026-09-11","bindings":[{"name":"CUSTOMERS","type":"inherit"}]}`, hostCode)
					active, _, _, err = s.WorkerRuntimeDeployment(host.ID, "/")
					must(err)
					if len(active.Deployment.DispatchNamespaces) != 1 || active.Deployment.DispatchNamespaces[0].Outbound.WorkerID != stage.ID {
						t.Fatal("inherit lost child identity")
					}
					invoke("staging:staging-secret")
					_, err = s.SetDeploymentTrafficForOrg(org.ID, host.ID, []nanoflare.DeploymentTraffic{{ID: productionRevision, TrafficPercent: 100}})
					must(err)
					invoke("production:production-secret")
					_, err = s.SetDeploymentTrafficForOrg(org.ID, host.ID, []nanoflare.DeploymentTraffic{{ID: stagingRevision, TrafficPercent: 100}})
					must(err)
					invoke("staging:staging-secret")
					// Secret updates are environment scoped and freeze with that environment's
					// next deployment rather than contaminating its production sibling.
					w = request("PUT", envBase+"staging/secrets", "application/json", []byte(`{"name":"TOKEN","type":"secret_text","text":"rotated"}`))
					if w.Code != 200 {
						t.Fatalf("secret update: %d %s", w.Code, w.Body.String())
					}
					upload(envBase+"staging", `{"main_module":"worker.js","compatibility_date":"2026-09-11","keep_bindings":["secret_text"]}`, `export default {fetch(r,e){return new Response('staging:'+e.TOKEN)}}`)
					invoke("staging:rotated")
					must(s.DeleteAppForOrg(org.ID, stage.ID))
					upload(envBase+"staging", `{"main_module":"worker.js","compatibility_date":"2026-09-11"}`, `export default {fetch(){return new Response('replacement')}}`)
					replacement, err := s.OrdinaryServiceEnvironmentForOrg(org.ID, "policy", "staging")
					must(err)
					if replacement.ID == stage.ID {
						t.Fatal("environment reused deleted identity")
					}
					r, err := http.Get(public.URL + "/internal/http/workers/" + host.ID + "/")
					must(err)
					b, err := io.ReadAll(r.Body)
					r.Body.Close()
					must(err)
					if r.StatusCode == 200 || strings.Contains(string(b), "replacement") {
						t.Fatalf("historical binding rebound to recreated environment: %d %s", r.StatusCode, b)
					}
					_, err = s.SetDeploymentTrafficForOrg(org.ID, host.ID, []nanoflare.DeploymentTraffic{{ID: productionRevision, TrafficPercent: 100}})
					must(err)
					invoke("production:production-secret")
					// Compatible settings must never serialize secret values.
					w = request("GET", envBase+"production/settings", "", nil)
					var envelope map[string]any
					must(json.Unmarshal(w.Body.Bytes(), &envelope))
					if w.Code != 200 || strings.Contains(w.Body.String(), "production-secret") {
						t.Fatalf("secret readback: %d %s", w.Code, w.Body.String())
					}
				})
			})
		}
	}
}

func TestOutboundEnvironmentPostgresEndToEnd(t *testing.T) {
	if os.Getenv("NANOFLARE_TEST_POSTGRES_URL") == "" {
		t.Skip("disposable PostgreSQL required")
	}
	t.Setenv("NANOFLARE_TEST_RUNTIME_BACKEND", "postgres")
	TestOutboundEnvironmentEndToEnd(t)
}

// Reopen the actual PostgreSQL pool (or recreate the memory-backed service),
// then verify both name resolution and stored outbound authority remain stable.
func TestOutboundEnvironmentPersistence(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			var repo nanoflare.Repository = nanoflare.NewStore()
			reopen := func() nanoflare.Repository { return repo }
			if backend == "postgres" {
				url := os.Getenv("NANOFLARE_TEST_POSTGRES_URL")
				if url == "" {
					t.Skip("disposable PostgreSQL required")
				}
				admin, err := sql.Open("postgres", url)
				must(err)
				schema := fmt.Sprintf("outbound_environment_%d", time.Now().UnixNano())
				_, err = admin.Exec("CREATE SCHEMA " + pq.QuoteIdentifier(schema))
				must(err)
				dsn, err := pq.ParseURL(url)
				must(err)
				dsn += " search_path=" + schema
				pg, err := database.Open(context.Background(), dsn)
				must(err)
				repo = pg
				reopen = func() nanoflare.Repository {
					must(pg.Close())
					var err error
					pg, err = database.Open(context.Background(), dsn)
					must(err)
					return pg
				}
				t.Cleanup(func() {
					pg.Close()
					admin.Exec("DROP SCHEMA " + pq.QuoteIdentifier(schema) + " CASCADE")
					admin.Close()
				})
			}
			must(repo.CreateOrganization(nanoflare.Organization{ID: "org", Name: "org", UsageLevel: nanoflare.UsageLevelPaid}))
			s, _ := managedService(t, repo, discardWriter{})
			_, err := s.EnsureOrdinaryServiceEnvironmentForOrg("org", "policy", "production", "")
			must(err)
			stage, err := s.EnsureOrdinaryServiceEnvironmentForOrg("org", "policy", "staging", "")
			must(err)
			must(s.PutSecret(stage.ID, "TOKEN", "stage-secret"))
			code := nanoflare.DeployInput{Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2026-09-11", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default {fetch(r,e){return new Response(e.TOKEN)}}`}}}
			_, err = s.Deploy(stage.ID, code)
			must(err)
			ns, err := s.CreateDispatchNamespace("org", nanoflare.CreateDispatchNamespaceInput{Name: "customers"})
			must(err)
			host, err := s.CreateApp(nanoflare.CreateAppInput{OrgID: "org", Name: "host", Hostname: "host.test"})
			must(err)
			code.DispatchNamespaces = []nanoflare.DispatchBinding{{Binding: "CUSTOMERS", NamespaceID: ns.ID, Outbound: &nanoflare.DispatchOutbound{WorkerID: stage.ID}}}
			revision, err := s.Deploy(host.ID, code)
			must(err)
			repo = reopen()
			s, _ = managedService(t, repo, discardWriter{})
			selected, err := s.OrdinaryServiceEnvironmentForOrg("org", "policy", "staging")
			must(err)
			if selected.ID != stage.ID {
				t.Fatal("environment selector changed across reopen")
			}
			active, _, found, err := s.WorkerRuntimeDeployment(host.ID, "/")
			must(err)
			if !found || active.Deployment.ID != revision.ID || active.Deployment.DispatchNamespaces[0].Outbound.WorkerID != stage.ID {
				t.Fatal("outbound child authority lost across reopen")
			}
			if _, err = s.OrdinaryServiceEnvironmentForOrg("foreign", "policy", "staging"); err == nil {
				t.Fatal("cross-org selector resolved")
			}
			must(s.DeleteAppForOrg("org", stage.ID))
			replacement, err := s.EnsureOrdinaryServiceEnvironmentForOrg("org", "policy", "staging", "")
			must(err)
			if replacement.ID == stage.ID {
				t.Fatal("recreated child reused authority")
			}
			repo = reopen()
			s, _ = managedService(t, repo, discardWriter{})
			selected, err = s.OrdinaryServiceEnvironmentForOrg("org", "policy", "staging")
			must(err)
			if selected.ID != replacement.ID {
				t.Fatal("replacement selector not durable")
			}
			artifact, err := repo.GetDeploymentArtifact(host.ID, revision.ID)
			must(err)
			if artifact.Deployment.DispatchNamespaces[0].Outbound.WorkerID != stage.ID {
				t.Fatal("retained revision rebound after reopen")
			}
			if _, err = repo.GetApp(stage.ID); err == nil {
				t.Fatal("deleted authority revived after reopen")
			}
		})
	}
}
