package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestCloudflareOrdinaryDispatcherUpload(t *testing.T) {
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
			auth := nanoflare.NewControlAuthService(repo, "ordinary-upload")
			auth.SetTestHashCost(4)
			session, err := auth.Signup(nanoflare.SignupInput{Email: "ordinary@example.com", Password: "password"})
			must(err)
			org, err := auth.CreateOrganization(session.User.ID, nanoflare.CreateOrganizationInput{Name: "platform"})
			must(err)
			foreign, err := auth.CreateOrganization(session.User.ID, nanoflare.CreateOrganizationInput{Name: "foreign"})
			must(err)
			service, _ := managedService(t, repo, discardWriter{})
			ns, err := service.CreateDispatchNamespace(org.ID, nanoflare.CreateDispatchNamespaceInput{Name: "sites"})
			must(err)
			_, err = service.CreateDispatchNamespace(foreign.ID, nanoflare.CreateDispatchNamespaceInput{Name: "foreign-sites"})
			must(err)
			// Ordinary script identity must not silently adopt a native display name.
			legacy, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: org.ID, Name: "dispatcher", Hostname: "legacy.example.com"})
			must(err)
			control, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: org.ID, Name: "control", Hostname: "control.example.com"})
			must(err)
			_, err = service.DeployForOrg(org.ID, control.ID, nanoflare.DeployInput{Format: "modules", CompatibilityDate: "2025-12-10", Entrypoint: "worker.js", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default {fetch(){return new Response("control")}}`}}})
			must(err)
			handler := NewServerWithControlAuth(service, nil, "", nil, auth)
			base := "/client/v4/accounts/" + org.ID + "/workers/scripts/dispatcher"
			metadata := `{"main_module":"worker.js","compatibility_date":"2025-12-10","bindings":[{"name":"DISPATCHER","type":"dispatch_namespace","namespace":"sites"},{"name":"CONTROL","type":"service","service":"control"},{"name":"TOKEN","type":"secret_text","text":"dispatcher-secret"}]}`
			code := `export default {async fetch(request,env,ctx){return env.DISPATCHER.get("site",{props:{}}).fetch(request)}}`
			kind, body := cfMultipart(t, metadata, code)
			request := func(method, path, contentType string, body []byte, token, key string, headers map[string]string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, bytes.NewReader(body))
				if token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				r.Header.Set("Content-Type", contentType)
				r.Header.Set("Idempotency-Key", key)
				for k, v := range headers {
					r.Header.Set(k, v)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			check := func(w *httptest.ResponseRecorder, want int) json.RawMessage {
				t.Helper()
				if w.Code != want {
					t.Fatalf("status %d, want %d: %s", w.Code, want, w.Body.String())
				}
				if strings.Contains(w.Body.String(), "dispatcher-secret") {
					t.Fatal("secret leaked")
				}
				var envelope struct {
					Success bool            `json:"success"`
					Result  json.RawMessage `json:"result"`
				}
				must(json.Unmarshal(w.Body.Bytes(), &envelope))
				if envelope.Success != (want < 300) {
					t.Fatal("incorrect response envelope")
				}
				return envelope.Result
			}
			check(request("PUT", base, kind, body, session.Token, "first", nil), 404)
			handler.SetCloudflareCompatibilityEnabled(true)
			check(request("PUT", base, kind, body, "", "first", nil), 401)
			check(request("PUT", base, kind, body, session.Token, "first", map[string]string{"X-Nanoflare-Customer-ID": "site"}), 422)
			limited, err := auth.CreatePersonalAccessToken(session.User.ID, nanoflare.CreatePersonalAccessTokenInput{Name: "upload-only", ScopeType: "org", OrgID: org.ID, Scopes: []string{"workers:write", "deployments:write", "secrets:write"}})
			must(err)
			check(request("PUT", base, kind, body, limited.Token, "first", nil), 403)
			bound, err := auth.CreatePersonalAccessToken(session.User.ID, nanoflare.CreatePersonalAccessTokenInput{Name: "bound", ScopeType: "org", OrgID: org.ID})
			must(err)
			check(request("PUT", strings.Replace(base, org.ID, foreign.ID, 1), kind, body, bound.Token, "first", nil), 403)
			badKind, badBody := cfMultipart(t, strings.Replace(metadata, `"namespace":"sites"`, `"namespace":"foreign-sites"`, 1), code)
			check(request("PUT", base, badKind, badBody, session.Token, "foreign", nil), 422)
			if _, err = service.OrdinaryScriptForOrg(org.ID, "dispatcher"); err != nanoflare.ErrAppNotFound {
				t.Fatal("rejected request created a Worker")
			}
			firstResponse := request("PUT", base+"?excludeScript=true", kind, body, session.Token, "first", nil)
			first := check(firstResponse, 200)
			var receipt struct {
				ID       string `json:"id"`
				WorkerID string `json:"nanoflare_worker_id"`
			}
			must(json.Unmarshal(first, &receipt))
			app, err := service.OrdinaryScriptForOrg(org.ID, "dispatcher")
			must(err)
			if receipt.ID != "dispatcher" || receipt.WorkerID != app.ID || app.ID == legacy.ID || app.Platform != nil || app.Hostname != "" {
				t.Fatalf("incorrect ordinary identity: %#v %s", app, first)
			}
			snapshot, found, err := service.ManagementUploadForOrg(org.ID, app.ID, false)
			must(err)
			if !found || !reflect.DeepEqual(snapshot.Deployment.DispatchNamespaces, []nanoflare.DispatchBinding{{Binding: "DISPATCHER", NamespaceID: ns.ID}}) || snapshot.Deployment.Services[0].WorkerID != control.ID {
				t.Fatalf("incorrect deployed bindings: %#v", snapshot.Deployment)
			}
			settings := check(request("GET", base+"/settings", "", nil, session.Token, "", nil), 200)
			if !bytes.Contains(settings, []byte(`"namespace":"sites"`)) {
				t.Fatalf("missing namespace settings: %s", settings)
			}
			// The same publication key recovers the original receipt.
			retry := request("PUT", base, kind, body, session.Token, "first", nil)
			check(retry, 200)
			if retry.Header().Get("ETag") != firstResponse.Header().Get("ETag") {
				t.Fatal("retry created a revision")
			}
			changedKind, changedBody := cfMultipart(t, metadata, code+"\n// changed")
			check(request("PUT", base, changedKind, changedBody, session.Token, "first", nil), 409)
			// Namespace scripts cannot acquire dispatcher authority through this schema.
			customerPath := "/client/v4/accounts/" + org.ID + "/workers/dispatch/namespaces/sites/scripts/site"
			check(request("PUT", customerPath, kind, body, session.Token, "customer", nil), 422)
			if _, err := service.NamespaceScript(org.ID, ns.ID, "site"); err != nanoflare.ErrAppNotFound {
				t.Fatal("customer upload created an identity before rejection")
			}
			// A full update retains secrets/dispatch via explicit type preservation.
			updateKind, updateBody := cfMultipart(t, `{"main_module":"worker.js","compatibility_date":"2025-12-10","keep_bindings":["secret_text","dispatch_namespace","service"]}`, code+"\n// update")
			check(request("PUT", base, updateKind, updateBody, session.Token, "second", nil), 200)
			updated, _, err := service.ManagementUploadForOrg(org.ID, app.ID, false)
			must(err)
			if !reflect.DeepEqual(updated.SecretRefs, snapshot.SecretRefs) || !reflect.DeepEqual(updated.Deployment.DispatchNamespaces, snapshot.Deployment.DispatchNamespaces) {
				t.Fatal("update lost secret or namespace")
			}
			_, err = service.SetDeploymentTrafficForOrg(org.ID, app.ID, []nanoflare.DeploymentTraffic{{ID: snapshot.DeploymentID, TrafficPercent: 100}})
			must(err)
			check(request("PUT", base, updateKind, updateBody, session.Token, "second", nil), 200)
			active, err := service.ActiveDeployments()
			must(err)
			for _, a := range active {
				if a.App.ID == app.ID && a.Deployment.ID != snapshot.DeploymentID {
					t.Fatal("retry undid rollback")
				}
			}
			// Recreating service state preserves ordinary script and upload identity.
			reopened, _ := managedService(t, repo, discardWriter{})
			handler = NewServerWithControlAuth(reopened, nil, "", nil, auth)
			handler.SetCloudflareCompatibilityEnabled(true)
			restarted, err := reopened.OrdinaryScriptForOrg(org.ID, "dispatcher")
			must(err)
			if restarted.ID != app.ID || restarted.RuntimeToken != app.RuntimeToken {
				t.Fatal("script identity changed after service recreation")
			}
			check(request("PUT", base, updateKind, updateBody, session.Token, "second", nil), 200)
			// Native administration preserves inspectable dispatch metadata and cannot
			// replace a namespace binding with a colliding secret.
			must(reopened.PutSecretForOrg(org.ID, app.ID, "EXTRA", "native-value"))
			nativeSnapshot, _, err := reopened.ManagementUploadForOrg(org.ID, app.ID, false)
			must(err)
			_, err = reopened.DeployForOrg(org.ID, app.ID, nativeSnapshot.Deployment)
			must(err)
			if err := reopened.PutSecretForOrg(org.ID, app.ID, "DISPATCHER", "must-not-replace"); err == nil {
				t.Fatal("native secret replaced a namespace binding")
			}
			nativeSettings := check(request("GET", base+"/settings", "", nil, session.Token, "", nil), 200)
			if !bytes.Contains(nativeSettings, []byte(`"namespace":"sites"`)) || bytes.Contains(nativeSettings, []byte("native-value")) {
				t.Fatalf("incorrect native namespace/secret projection: %s", nativeSettings)
			}
			check(request("PUT", base, updateKind, updateBody, session.Token, "after-native", nil), 200)
			// Free the unrelated legacy fixture before testing the three-Worker quota.
			must(reopened.DeleteAppForOrg(org.ID, legacy.ID))
			// Concurrent first uploads converge on one identity and keyed revision.
			var group sync.WaitGroup
			responses := make(chan *httptest.ResponseRecorder, 2)
			for i := 0; i < 2; i++ {
				group.Add(1)
				go func() {
					defer group.Done()
					responses <- request("PUT", strings.TrimSuffix(base, "dispatcher")+"concurrent", kind, body, session.Token, "concurrent", nil)
				}()
			}
			group.Wait()
			close(responses)
			etag := ""
			for response := range responses {
				check(response, 200)
				if etag != "" && etag != response.Header().Get("ETag") {
					t.Fatal("concurrent replay created separate revisions")
				}
				etag = response.Header().Get("ETag")
			}
			workers, err := reopened.ListAppsForOrg(org.ID)
			must(err)
			if len(workers) != 3 {
				t.Fatalf("concurrent upload created duplicate Workers: %d", len(workers))
			}
		})
	}
}
