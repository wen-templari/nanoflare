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
			writer := &managedRecordingWriter{}
			service, _ := managedService(t, repo, writer)
			must(service.SetBaseHostname("workers.example.test"))
			ns, err := service.CreateDispatchNamespace(org.ID, nanoflare.CreateDispatchNamespaceInput{Name: "sites"})
			must(err)
			_, err = service.CreateDispatchNamespace(foreign.ID, nanoflare.CreateDispatchNamespaceInput{Name: "foreign-sites"})
			must(err)
			// Ordinary script identity must not silently adopt a native display name.
			legacy, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: org.ID, Name: "dispatcher", Hostname: "dispatcher-platform.workers.example.test"})
			must(err)
			control, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: org.ID, Name: "control", Hostname: "control.example.com"})
			must(err)
			controlRevision, err := service.DeployForOrg(org.ID, control.ID, nanoflare.DeployInput{Format: "modules", CompatibilityDate: "2025-12-10", Entrypoint: "worker.js", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default {fetch(){return new Response("control")}}`}}})
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
				if key != "" {
					r.Header.Set("Idempotency-Key", key)
				}
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
			secretBody := []byte(`{"name":"CLI_TOKEN","type":"secret_text","text":"private-cli-value"}`)
			check(request("PUT", base+"/secrets", "application/json", secretBody, limited.Token, "cli-secret", nil), 403)
			check(request("PUT", base+"/secrets", "application/json", secretBody, session.Token, "cli-secret", nil), 200)
			check(request("PUT", base+"/secrets", "application/json", secretBody, limited.Token, "cli-secret", nil), 403)
			listedSecrets := check(request("GET", base+"/secrets", "", nil, session.Token, "", nil), 200)
			if bytes.Contains(listedSecrets, []byte("private-cli-value")) {
				t.Fatal("ordinary secret plaintext leaked")
			}
			check(request("GET", base+"/secrets/CLI_TOKEN", "", nil, session.Token, "", nil), 200)
			check(request("PUT", base+"/secrets", "application/json", []byte(`{"name":"DISPATCHER","type":"secret_text","text":"conflict"}`), session.Token, "cli-conflict", nil), 422)
			check(request("DELETE", base+"/secrets/CLI_TOKEN?url_encoded=false", "", nil, session.Token, "", nil), 422)
			check(request("DELETE", base+"/secrets/CLI_TOKEN?url_encoded=true", "", nil, session.Token, "cli-delete", nil), 200)
			check(request("GET", base+"/secrets/CLI_TOKEN", "", nil, session.Token, "", nil), 404)
			kv, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{OrgID: org.ID, Name: "z-owned"})
			must(err)
			_, err = service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{OrgID: foreign.ID, Name: "a-foreign"})
			must(err)
			kvPath := "/client/v4/accounts/" + org.ID + "/storage/kv/namespaces"
			check(request("GET", kvPath, "", nil, "", "", nil), 401)
			check(request("GET", kvPath, "", nil, limited.Token, "", nil), 403)
			check(request("GET", strings.Replace(kvPath, org.ID, foreign.ID, 1), "", nil, bound.Token, "", nil), 403)
			listedKV := check(request("GET", kvPath+"?page=1&per_page=1&order=title&direction=asc", "", nil, session.Token, "", nil), 200)
			if !bytes.Contains(listedKV, []byte(kv.ID)) || bytes.Contains(listedKV, []byte("a-foreign")) {
				t.Fatalf("KV listing scope: %s", listedKV)
			}
			if string(check(request("GET", kvPath+"?page=2&per_page=1", "", nil, session.Token, "", nil), 200)) != "[]" {
				t.Fatal("KV page did not terminate")
			}
			for _, query := range []string{"page=0", "per_page=0", "per_page=1001", "order=id", "direction=desc", "page=1&page=2", "unknown=true"} {
				check(request("GET", kvPath+"?"+query, "", nil, session.Token, "", nil), 422)
			}

			servicePath := "/client/v4/accounts/" + org.ID + "/workers/services/dispatcher"
			for _, path := range []string{servicePath, base + "/deployments", base + "/subdomain"} {
				check(request("GET", path, "", nil, "", "", nil), 401)
				check(request("GET", path, "", nil, limited.Token, "", nil), 403)
				check(request("GET", strings.Replace(path, org.ID, foreign.ID, 1), "", nil, bound.Token, "", nil), 403)
				check(request("GET", path, "", nil, session.Token, "", nil), 200)
				check(request("GET", path+"?unexpected=true", "", nil, session.Token, "", nil), 422)
			}
			check(request("POST", base+"/subdomain", "application/json", []byte(`{"enabled":false,"previews_enabled":false}`), session.Token, "", nil), 200)
			check(request("POST", base+"/subdomain", "application/json", []byte(`{"enabled":true}`), session.Token, "", nil), 422)
			check(request("POST", base+"/subdomain", "application/json", []byte(`{"enabled":false,"previews_enabled":true}`), session.Token, "", nil), 422)

			var receipt struct {
				ID       string `json:"id"`
				WorkerID string `json:"nanoflare_worker_id"`
			}
			must(json.Unmarshal(first, &receipt))
			app, err := service.OrdinaryScriptForOrg(org.ID, "dispatcher")
			must(err)
			if receipt.ID != "dispatcher" || receipt.WorkerID != app.ID || app.ID == legacy.ID || app.Platform != nil || app.Hostname == "" || app.Hostname == legacy.Hostname {
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
			// Version uploads are immutable latest snapshots, with no live traffic
			// change until a separately validated deployment request succeeds.
			stagedKind, stagedBody := cfMultipart(t, `{"main_module":"worker.js","compatibility_date":"2025-12-10","keep_bindings":["secret_text","dispatch_namespace","service"]}`, code+"\n// staged")
			check(request("POST", base+"/versions", stagedKind, stagedBody, limited.Token, "staged", nil), 403)
			check(request("POST", strings.Replace(base, org.ID, foreign.ID, 1)+"/versions", stagedKind, stagedBody, bound.Token, "staged", nil), 403)
			stagedResponse := request("POST", base+"/versions", stagedKind, stagedBody, session.Token, "staged", nil)
			var version struct {
				ID        string `json:"id"`
				Resources struct {
					Script struct {
						ETag string `json:"etag"`
					} `json:"script"`
				} `json:"resources"`
			}
			must(json.Unmarshal(check(stagedResponse, 200), &version))
			if version.ID == "" || version.ID == updated.DeploymentID || version.Resources.Script.ETag != version.ID {
				t.Fatal("incorrect staged version receipt")
			}
			assertTraffic := func(id string) {
				t.Helper()
				active, err := service.ActiveDeployments()
				must(err)
				found := false
				for _, item := range active {
					if item.App.ID == app.ID {
						found = true
						if item.Deployment.ID != id || item.TrafficPercent != 100 {
							t.Fatalf("unexpected live traffic: %#v", item)
						}
					}
				}
				if !found {
					t.Fatal("Worker lost live traffic")
				}
			}
			assertTraffic(updated.DeploymentID)
			staged, found, err := service.ManagementUploadForOrg(org.ID, app.ID, false)
			must(err)
			if !found || staged.DeploymentID != version.ID || !reflect.DeepEqual(staged.SecretRefs, updated.SecretRefs) {
				t.Fatal("staging lost immutable secret snapshot")
			}
			// Replays preserve the same staged receipt and do not activate it.
			replayed := request("POST", base+"/versions", stagedKind, stagedBody, session.Token, "staged", nil)
			check(replayed, 200)
			if replayed.Header().Get("ETag") != stagedResponse.Header().Get("ETag") {
				t.Fatal("staged retry created a revision")
			}
			assertTraffic(updated.DeploymentID)
			check(request("PUT", base, stagedKind, stagedBody, session.Token, "staged", nil), 409)
			deploymentBody := []byte(`{"strategy":"percentage","versions":[{"version_id":"` + version.ID + `","percentage":100}],"annotations":{}}`)
			check(request("POST", base+"/deployments", "application/json", deploymentBody, limited.Token, "", nil), 403)
			check(request("POST", strings.Replace(base, org.ID, foreign.ID, 1)+"/deployments", "application/json", deploymentBody, bound.Token, "", nil), 403)
			for _, invalid := range []string{
				`{"strategy":"other","versions":[]}`,
				`{"strategy":"percentage","versions":[]}`,
				`{"strategy":"percentage","versions":[{"version_id":"` + version.ID + `","percentage":50}]}`,
				`{"strategy":"percentage","versions":[{"version_id":"` + version.ID + `","percentage":50},{"version_id":"` + version.ID + `","percentage":50}]}`,
				`{"strategy":"percentage","versions":[{"version_id":"` + version.ID + `","percentage":99.5}]}`,
				`{"strategy":"percentage","versions":[{"version_id":"missing","percentage":100}]}`,
				`{"strategy":"percentage","versions":[{"version_id":"` + controlRevision.ID + `","percentage":100}]}`,
			} {
				response := request("POST", base+"/deployments", "application/json", []byte(invalid), session.Token, "", nil)
				if response.Code < 400 {
					t.Fatalf("invalid traffic accepted: %s", invalid)
				}
				assertTraffic(updated.DeploymentID)
			}
			writer.mu.Lock()
			writer.failNext = true
			writer.mu.Unlock()
			check(request("POST", base+"/deployments", "application/json", deploymentBody, session.Token, "", nil), 400)
			assertTraffic(updated.DeploymentID)
			weighted := []byte(`{"strategy":"percentage","versions":[{"version_id":"` + updated.DeploymentID + `","percentage":25},{"version_id":"` + version.ID + `","percentage":75}]}`)
			check(request("POST", base+"/deployments", "application/json", weighted, session.Token, "", nil), 200)
			projection := check(request("GET", base+"/deployments", "", nil, session.Token, "", nil), 200)
			if !bytes.Contains(projection, []byte(`"percentage":25`)) || !bytes.Contains(projection, []byte(`"percentage":75`)) {
				t.Fatal("deployment weights were not preserved")
			}
			check(request("POST", base+"/deployments", "application/json", deploymentBody, session.Token, "", nil), 200)
			assertTraffic(version.ID)
			// Non-versioned tag updates must not alter live traffic or accept
			// unsupported observability features.
			check(request("PATCH", base+"/script-settings", "application/json", []byte(`{"tags":["release"],"observability":{"enabled":false}}`), session.Token, "tags", nil), 200)
			assertTraffic(version.ID)
			for _, settings := range []string{`{"observability":{"enabled":true}}`, `{"logpush":true}`, `{"tail_consumers":[{"service":"x"}]}`, `{"unknown":true}`} {
				check(request("PATCH", base+"/script-settings", "application/json", []byte(settings), session.Token, "", nil), 422)
			}
			check(request("PATCH", base+"/script-settings", "application/json", []byte(`{"tags":[]}`), limited.Token, "", nil), 403)
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
			if restarted.ID != app.ID || restarted.RuntimeToken != app.RuntimeToken || restarted.Hostname != app.Hostname {
				t.Fatal("script identity changed after service recreation")
			}
			check(request("POST", base+"/versions", stagedKind, stagedBody, session.Token, "staged", nil), 200)
			assertTraffic(snapshot.DeploymentID)
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
			deletePath := strings.Replace(servicePath, "dispatcher", "concurrent", 1)
			readOnly, err := auth.CreatePersonalAccessToken(session.User.ID, nanoflare.CreatePersonalAccessTokenInput{Name: "read-only", ScopeType: "org", OrgID: org.ID, Scopes: []string{"workers:read"}})
			must(err)
			check(request("DELETE", deletePath+"?force=true", "", nil, readOnly.Token, "", nil), 403)
			check(request("DELETE", strings.Replace(deletePath, org.ID, foreign.ID, 1)+"?force=true", "", nil, bound.Token, "", nil), 403)
			check(request("DELETE", deletePath, "", nil, session.Token, "", nil), 422)
			check(request("DELETE", deletePath+"?force=true", "", nil, session.Token, "", map[string]string{"If-Match": "*"}), 422)
			check(request("DELETE", deletePath+"?force=true", "", nil, session.Token, "", nil), 200)
			if _, err := service.OrdinaryScriptForOrg(org.ID, "concurrent"); err != nanoflare.ErrAppNotFound {
				t.Fatalf("ordinary deletion did not remove identity: %v", err)
			}

		})
	}
}
