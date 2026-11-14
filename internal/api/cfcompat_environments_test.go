package api

import (
	"bytes"
	"github.com/clas/nanoflare/internal/nanoflare"
	"net/http/httptest"
	"testing"
)

func TestCloudflareServiceEnvironmentIsolation(t *testing.T) {
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
			auth := nanoflare.NewControlAuthService(repo, "environment-isolation")
			auth.SetTestHashCost(4)
			session, err := auth.Signup(nanoflare.SignupInput{Email: "environments@example.com", Password: "password"})
			must(err)
			org, err := auth.CreateOrganization(session.User.ID, nanoflare.CreateOrganizationInput{Name: "platform"})
			must(err)
			service, _ := managedService(t, repo, &managedRecordingWriter{})
			handler := NewServerWithControlAuth(service, nil, "", nil, auth)
			handler.SetCloudflareCompatibilityEnabled(true)
			base := "/client/v4/accounts/" + org.ID + "/workers/services/gateway/environments/"
			request := func(method, path, kind string, body []byte) *httptest.ResponseRecorder {
				t.Helper()
				r := httptest.NewRequest(method, path, bytes.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+session.Token)
				r.Header.Set("Content-Type", kind)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			for _, env := range []string{"production", "staging"} {
				kind, body := cfMultipart(t, `{"main_module":"worker.js","compatibility_date":"2025-12-10","bindings":[{"name":"TOKEN","type":"secret_text","text":"`+env+`"}]}`, `export default {fetch(){return new Response("`+env+`")}}`)
				w := request("PUT", base+env, kind, body)
				if w.Code != 200 {
					t.Fatalf("%s: %d %s", env, w.Code, w.Body.String())
				}
			}
			prod, err := service.OrdinaryServiceEnvironmentForOrg(org.ID, "gateway", "production")
			must(err)
			stage, err := service.OrdinaryServiceEnvironmentForOrg(org.ID, "gateway", "staging")
			must(err)
			if prod.ID == stage.ID {
				t.Fatal("environment identity collision")
			}
			native := request("GET", "/v1/organizations/"+org.ID+"/workers/"+prod.ID+"/environments", "", nil)
			if native.Code != 200 {
				t.Fatalf("native environments: %d %s", native.Code, native.Body.String())
			}
			active, err := service.ActiveDeployments()
			must(err)
			seen := map[string]bool{}
			for _, item := range active {
				if item.App.ID == prod.ID || item.App.ID == stage.ID {
					want := "production"
					if item.App.ID == stage.ID {
						want = "staging"
					}
					if item.App.SecretValues["TOKEN"] != want {
						t.Fatal("environment secret isolation failed")
					}
					seen[item.App.ID] = true
				}
			}
			if len(seen) != 2 {
				t.Fatal("environment traffic not independently active")
			}

			for _, pair := range []struct {
				app    nanoflare.App
				secret string
			}{{prod, "production"}, {stage, "staging"}} {
				snapshot, found, err := service.ManagementUploadForOrg(org.ID, pair.app.ID, true)
				must(err)
				if !found || snapshot.Deployment.Files[0].Content != `export default {fetch(){return new Response("`+pair.secret+`")}}` {
					t.Fatalf("environment code mixed: %#v", snapshot)
				}
			}
			if _, err := service.OrdinaryServiceEnvironmentForOrg("foreign", "gateway", "staging"); err == nil {
				t.Fatal("cross org environment visible")
			}
			old := stage.ID
			must(service.DeleteAppForOrg(org.ID, stage.ID))
			stage, err = service.EnsureOrdinaryServiceEnvironmentForOrg(org.ID, "gateway", "staging", "")
			must(err)
			if old == stage.ID {
				t.Fatal("deleted environment ID reused")
			}
			if _, err := repo.GetApp(old); err == nil {
				t.Fatal("deleted identity still resolves")
			}
			must(service.DeleteAppForOrg(org.ID, prod.ID))
			if _, err := repo.GetApp(stage.ID); err == nil {
				t.Fatal("service delete retained environment worker")
			}
			recreated, err := service.EnsureOrdinaryScriptForOrg(org.ID, "gateway", "")
			must(err)
			if recreated.ID == prod.ID {
				t.Fatal("production ID reused after deletion")
			}
			if _, err := service.ServiceEnvironmentForWorkerForOrg(org.ID, prod.ID); err == nil {
				t.Fatal("deleted production authority rebound")
			}
			if _, err := service.OrdinaryServiceEnvironmentForOrg(org.ID, "gateway", "staging"); err == nil {
				t.Fatal("recreated parent adopted old environment")
			}

		})
	}
}
