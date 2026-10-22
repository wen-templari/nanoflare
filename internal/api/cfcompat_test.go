package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"reflect"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func cfMultipart(t *testing.T, metadata, code string) (string, []byte) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, part := range []struct{ name, kind, body string }{{"metadata", "application/json", metadata}, {"worker.js", "application/javascript+module", code}} {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="`+part.name+`"; filename="ignored.js"`)
		header.Set("Content-Type", part.kind)
		out, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = out.Write([]byte(part.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return writer.FormDataContentType(), body.Bytes()
}

func TestCloudflareCompatibilityUploadHTTPContract(t *testing.T) {
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
			auth := nanoflare.NewControlAuthService(repo, "compat-http-test")
			auth.SetTestHashCost(4)
			session, err := auth.Signup(nanoflare.SignupInput{Email: "compat@example.com", Password: "password"})
			must(err)
			org, err := auth.CreateOrganization(session.User.ID, nanoflare.CreateOrganizationInput{Name: "compat"})
			must(err)
			objects := newAPIObjectStore()
			service := nanoflare.NewServiceWithObjects(repo, discardWriter{}, objects)
			service.SetPlatformDispatchEnabled(true)
			codec, err := nanoflare.NewSecretCodec("0123456789abcdef0123456789abcdef")
			must(err)
			service.SetSecretCodec(codec)
			handler := NewServerWithControlAuth(service, nil, "", nil, auth)
			server := httptest.NewServer(handler)
			defer server.Close()
			base := "/client/v4/accounts/" + org.ID + "/workers/dispatch/namespaces"
			call := func(method, path, kind string, body []byte, token, key string, profile bool, want int) json.RawMessage {
				t.Helper()
				request, err := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
				must(err)
				if kind != "" {
					request.Header.Set("Content-Type", kind)
				}
				if token != "" {
					request.Header.Set("Authorization", "Bearer "+token)
				}
				if key != "" {
					request.Header.Set("Idempotency-Key", key)
				}
				if profile {
					request.Header.Set("X-Nanoflare-Execution-Profile", "trusted_shared_workerd")
				}
				response, err := server.Client().Do(request)
				must(err)
				defer response.Body.Close()
				data, err := io.ReadAll(response.Body)
				must(err)
				if response.StatusCode != want {
					t.Fatalf("%s %s: %d %s", method, path, response.StatusCode, data)
				}
				var envelope struct {
					Success bool            `json:"success"`
					Errors  []cfAPIError    `json:"errors"`
					Result  json.RawMessage `json:"result"`
				}
				must(json.Unmarshal(data, &envelope))
				if envelope.Success != (want < 300) || ((want >= 300) != (len(envelope.Errors) > 0)) {
					t.Fatalf("incorrect envelope %s", data)
				}
				if bytes.Contains(data, []byte("compat-secret")) {
					t.Fatal("compatibility response leaked secret")
				}
				return envelope.Result
			}
			call("GET", base, "", nil, session.Token, "", false, 404)
			handler.SetCloudflareCompatibilityEnabled(true)
			call("GET", base, "", nil, "", "", false, 401)
			call("POST", base, "application/json", []byte(`{"name":"staging","trusted_workers":true}`), session.Token, "", true, 422)
			created := call("POST", base, "application/json", []byte(`{"name":"staging"}`), session.Token, "", false, 200)
			var namespace struct {
				ID   string `json:"namespace_id"`
				Name string `json:"namespace_name"`
			}
			must(json.Unmarshal(created, &namespace))
			if namespace.ID == "" || namespace.Name != "staging" {
				t.Fatal("namespace response shape")
			}
			call("GET", base+"?unexpected=true", "", nil, session.Token, "", false, 422)
			kv, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{OrgID: org.ID, Name: "state"})
			must(err)
			_, err = service.SetNamespaceGrants(org.ID, namespace.ID, nanoflare.NamespaceGrantsInput{ExpectedRevision: 1, Grants: []nanoflare.NamespaceResourceGrant{{Kind: "kv", ResourceID: kv.ID}}})
			must(err)
			code := `export default {async fetch(request,env){return Response.json({message:env.MESSAGE,config:env.CONFIG,secret:env.TOKEN});}}`
			metadataBytes, err := json.Marshal(map[string]any{"main_module": "worker.js", "compatibility_date": "2025-12-10", "bindings": []any{map[string]string{"type": "plain_text", "name": "MESSAGE", "text": "hello"}, map[string]any{"type": "json", "name": "CONFIG", "json": map[string]any{"answer": 42}}, map[string]string{"type": "kv_namespace", "name": "STATE", "namespace_id": kv.ID}, map[string]string{"type": "secret_text", "name": "TOKEN", "text": "compat-secret"}}, "tags": []string{"customer-a"}})
			must(err)
			kind, body := cfMultipart(t, string(metadataBytes), code)
			path := base + "/staging/scripts/alpha"
			conditional := httptest.NewRequest("PUT", path, bytes.NewReader(body))
			conditional.Header.Set("Authorization", "Bearer "+session.Token)
			conditional.Header.Set("Content-Type", kind)
			conditional.Header.Set("If-Match", `"must-not-be-ignored"`)
			conditionalResponse := httptest.NewRecorder()
			handler.ServeHTTP(conditionalResponse, conditional)
			if conditionalResponse.Code != 422 {
				t.Fatal("full upload silently ignored a precondition")
			}
			call("PUT", path+"?excludeScript=false", kind, body, session.Token, "bad-query", true, 422)
			call("PUT", path+"?excludeScript=true&excludeScript=true", kind, body, session.Token, "duplicate-query", true, 422)
			result := call("PUT", path+"?excludeScript=true", kind, body, session.Token, "first", true, 200)
			var script struct {
				ID       string `json:"id"`
				Revision string `json:"etag"`
			}
			must(json.Unmarshal(result, &script))
			if script.ID != "alpha" || script.Revision == "" {
				t.Fatal("script response shape")
			}
			firstID := script.Revision
			// A regenerated multipart boundary does not change request identity.
			kind, body = cfMultipart(t, string(metadataBytes), code)
			retry := call("PUT", path, kind, body, session.Token, "first", true, 200)
			must(json.Unmarshal(retry, &script))
			if script.Revision != firstID {
				t.Fatal("HTTP retry created another revision")
			}
			settings := call("GET", path+"/settings", "", nil, session.Token, "", false, 200)
			var settingsValue struct {
				Bindings []map[string]json.RawMessage `json:"bindings"`
			}
			must(json.Unmarshal(settings, &settingsValue))
			for _, binding := range settingsValue.Bindings {
				if string(binding["type"]) == `"secret_text"` && binding["text"] != nil {
					t.Fatal("settings exposed secret value")
				}
			}
			call("GET", path+"/bindings", "", nil, session.Token, "", false, 200)
			call("GET", path+"/secrets", "", nil, session.Token, "", false, 200)
			detail := call("GET", path, "", nil, session.Token, "", false, 200)
			var detailValue struct {
				Namespace string `json:"dispatch_namespace"`
				Script    struct {
					ID string `json:"id"`
				} `json:"script"`
			}
			must(json.Unmarshal(detail, &detailValue))
			if detailValue.Namespace != "staging" || detailValue.Script.ID != "alpha" {
				t.Fatal("script detail wrapper missing")
			}
			app, err := service.NamespaceScript(org.ID, namespace.ID, "alpha")
			must(err)
			// Equivalent native HTTP requests publish the same effective snapshot.
			nativeApp, err := service.CreateNamespaceScript(org.ID, namespace.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "native", Execution: "dynamic"})
			must(err)
			nativeRequest := func(method, suffix string, input any, want int) []byte {
				t.Helper()
				payload, err := json.Marshal(input)
				must(err)
				request, err := http.NewRequest(method, server.URL+"/v1/organizations/"+org.ID+"/workers/"+nativeApp.ID+suffix, bytes.NewReader(payload))
				must(err)
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Authorization", "Bearer "+session.Token)
				response, err := server.Client().Do(request)
				must(err)
				defer response.Body.Close()
				data, err := io.ReadAll(response.Body)
				must(err)
				if response.StatusCode != want {
					t.Fatalf("native %s %s: %d %s", method, suffix, response.StatusCode, data)
				}
				return data
			}
			nativeRequest("PUT", "/secrets/TOKEN", nanoflare.PutSecretInput{Value: "compat-secret"}, 204)
			nativeBody := nativeRequest("POST", "/deployments", nanoflare.DeployInput{Format: "modules", CompatibilityDate: "2025-12-10", Entrypoint: "worker.js", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: code}}, Vars: map[string]json.RawMessage{"MESSAGE": json.RawMessage(`"hello"`), "CONFIG": json.RawMessage(`{"answer":42}`)}, KVNamespaces: []nanoflare.KVBinding{{Binding: "STATE", ID: kv.ID}}}, 201)
			var native nanoflare.Deployment
			must(json.Unmarshal(nativeBody, &native))
			cfArtifact, err := repo.GetDeploymentArtifact(app.ID, firstID)
			must(err)
			nativeArtifact, err := repo.GetDeploymentArtifact(nativeApp.ID, native.ID)
			must(err)
			if !reflect.DeepEqual(cfArtifact.Deployment.Files, nativeArtifact.Deployment.Files) || !reflect.DeepEqual(cfArtifact.Deployment.Vars, nativeArtifact.Deployment.Vars) || !reflect.DeepEqual(cfArtifact.Deployment.KVNamespaces, nativeArtifact.Deployment.KVNamespaces) || cfArtifact.Deployment.Format != nativeArtifact.Deployment.Format {
				t.Fatal("native and compatibility snapshots differ")
			}
			value, err := codec.Decrypt(cfArtifact.Secrets[0].Nonce, cfArtifact.Secrets[0].Ciphertext)
			must(err)
			if value != "compat-secret" {
				t.Fatal("compatibility secret was not pinned")
			}
			// Retention loads verified asset bytes only when requested. A full
			// replacement can repair a Worker by dropping unavailable old assets.
			nativeRequest("POST", "/deployments", nanoflare.DeployInput{IdempotencyKey: "native-assets", Format: "modules", CompatibilityDate: "2025-12-10", Entrypoint: "worker.js", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: code}}, Assets: []nanoflare.AssetFile{{Path: "hello.txt", Data: []byte("retained asset"), ContentType: "text/plain"}}}, 201)
			keep := `{"main_module":"worker.js","compatibility_date":"2025-12-10","keep_assets":true,"bindings":[{"type":"inherit","name":"TOKEN"},{"type":"assets","name":"ASSETS"}]}`
			kind, body = cfMultipart(t, keep, code)
			kept := call("PUT", base+"/staging/scripts/native", kind, body, session.Token, "keep-assets", true, 200)
			var keptScript struct {
				Revision string `json:"etag"`
			}
			must(json.Unmarshal(kept, &keptScript))
			loaded, _, err := service.ManagementUploadForOrg(org.ID, nativeApp.ID, true)
			must(err)
			if len(loaded.Deployment.Assets) != 1 || string(loaded.Deployment.Assets[0].Data) != "retained asset" || loaded.Deployment.Assets[0].ObjectKey != "" {
				t.Fatal("retained assets were not loaded safely")
			}
			keptArtifact, err := repo.GetDeploymentArtifact(nativeApp.ID, keptScript.Revision)
			must(err)
			_, err = objects.Put(nativeApp.ID, keptArtifact.Deployment.Assets[0].ObjectKey, "text/plain", []byte("corrupted"))
			must(err)
			call("GET", base+"/staging/scripts/native/settings", "", nil, session.Token, "", false, 200)
			kind, body = cfMultipart(t, `{"main_module":"worker.js","compatibility_date":"2025-12-10","bindings":[{"type":"inherit","name":"TOKEN"}]}`, code)
			call("PUT", base+"/staging/scripts/native", kind, body, session.Token, "drop-broken-assets", true, 200)
			loaded, _, err = service.ManagementUploadForOrg(org.ID, nativeApp.ID, true)
			must(err)
			if len(loaded.Deployment.Assets) != 0 {
				t.Fatal("full replacement retained broken old assets")
			}
			// Renaming inheritance removes the source name. Retry must replay before
			// attempting strict inheritance against the newly uploaded snapshot.
			metadata := `{"main_module":"worker.js","compatibility_date":"2025-12-10","bindings":[{"name":"RENAMED","type":"inherit","old_name":"TOKEN"}],"tags":["renamed"]}`
			kind, body = cfMultipart(t, metadata, code)
			result = call("PUT", path+"?bindings_inherit=strict", kind, body, session.Token, "renamed", true, 200)
			must(json.Unmarshal(result, &script))
			secondID := script.Revision
			_, err = service.SetDeploymentTrafficForOrg(org.ID, app.ID, []nanoflare.DeploymentTraffic{{ID: firstID, TrafficPercent: 100}})
			must(err)
			kind, body = cfMultipart(t, metadata, code)
			result = call("PUT", path+"?bindings_inherit=strict", kind, body, session.Token, "renamed", true, 200)
			must(json.Unmarshal(result, &script))
			if script.Revision != secondID {
				t.Fatal("renamed inheritance retry changed revision")
			}
			active, err := repo.ActiveDeploymentsForApp(app.ID)
			must(err)
			if len(active) != 1 || active[0].Deployment.ID != firstID {
				t.Fatal("HTTP retry undid native rollback")
			}
			kind, body = cfMultipart(t, strings.Replace(metadata, "renamed", "changed-tag", 1), code)
			call("PUT", path+"?bindings_inherit=strict", kind, body, session.Token, "renamed", true, 409)
			pub, _, err := repo.GetPublication(app.ID, "renamed")
			must(err)
			encoded, err := json.Marshal(pub)
			must(err)
			if bytes.Contains(encoded, []byte("compat-secret")) || pub.RequestIdentity == "" {
				t.Fatal("unsafe or missing persistent request identity")
			}
			readOnly, err := auth.CreatePersonalAccessToken(session.User.ID, nanoflare.CreatePersonalAccessTokenInput{Name: "reader", ScopeType: "org", OrgID: org.ID, Scopes: []string{"workers:read", "namespaces:read"}})
			must(err)
			call("GET", path+"/settings", "", nil, readOnly.Token, "", false, 200)
			call("PUT", path, kind, body, readOnly.Token, "forbidden", true, 403)
			call("DELETE", path, "", nil, readOnly.Token, "", true, 403)
			call("DELETE", base+"/staging", "", nil, session.Token, "", true, 409)
			call("DELETE", path, "", nil, session.Token, "", true, 200)
			call("GET", path, "", nil, session.Token, "", false, 404)
		})
	}
}

func TestCloudflareCompatibilityAuthorizationAndUnsupportedInputs(t *testing.T) {
	repo := nanoflare.NewStore()
	auth := nanoflare.NewControlAuthService(repo, "compat-auth")
	auth.SetTestHashCost(4)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	session, err := auth.Signup(nanoflare.SignupInput{Email: "one@example.com", Password: "password"})
	must(err)
	org, err := auth.CreateOrganization(session.User.ID, nanoflare.CreateOrganizationInput{Name: "one"})
	must(err)
	other, err := auth.Signup(nanoflare.SignupInput{Email: "two@example.com", Password: "password"})
	must(err)
	foreign, err := auth.CreateOrganization(other.User.ID, nanoflare.CreateOrganizationInput{Name: "two"})
	must(err)
	service := nanoflare.NewService(repo, discardWriter{})
	service.SetPlatformDispatchEnabled(true)
	handler := NewServerWithControlAuth(service, nil, "", nil, auth)
	handler.SetCloudflareCompatibilityEnabled(true)
	base := "/client/v4/accounts/" + org.ID + "/workers/dispatch/namespaces"
	request := func(method, path, kind string, body []byte, token string, want int) {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", kind)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		var envelope cfEnvelope
		must(json.Unmarshal(w.Body.Bytes(), &envelope))
		if (want >= 300) != (!envelope.Success) {
			t.Fatal("invalid error envelope")
		}
	}
	request("POST", base, "application/json", []byte(`{"name":"staging","name":"duplicate"}`), session.Token, 422)
	request("POST", base, "application/json", []byte(`{"NAME":"staging"}`), session.Token, 422)
	request("POST", base, "application/json", []byte(`{"name":"staging"}`), session.Token, 200)
	request("GET", "/client/v4/accounts/"+foreign.ID+"/workers/dispatch/namespaces", "", nil, session.Token, 403)
	request("GET", base, "", nil, "nfc1.not-a-control-token", 401)
	request("GET", base+"?bad=%zz", "", nil, session.Token, 422)
	limited, err := auth.CreatePersonalAccessToken(session.User.ID, nanoflare.CreatePersonalAccessTokenInput{Name: "no-reader", ScopeType: "org", OrgID: org.ID, Scopes: []string{"secrets:write"}})
	must(err)
	bound, err := auth.CreatePersonalAccessToken(session.User.ID, nanoflare.CreatePersonalAccessTokenInput{Name: "account-bound", ScopeType: "org", OrgID: org.ID})
	must(err)
	foreignBase := "/client/v4/accounts/" + foreign.ID + "/workers/dispatch/namespaces"
	request("GET", foreignBase, "", nil, bound.Token, 403)
	request("POST", foreignBase, "application/json", []byte(`{"name":"must-not-create"}`), bound.Token, 403)
	request("PUT", foreignBase+"/staging/scripts/must-not-create", "multipart/form-data", nil, bound.Token, 403)
	request("GET", foreignBase+"/staging/scripts/alpha/settings", "", nil, bound.Token, 403)
	request("GET", base+"/staging/scripts/missing", "", nil, limited.Token, 403)
	request("GET", base+"/nonexistent/scripts/missing", "", nil, limited.Token, 403)
	kind, body := cfMultipart(t, `{"main_module":"worker.js","limits":{"cpu_ms":10}}`, `export default {}`)
	request("PUT", base+"/staging/scripts/invalid", kind, body, session.Token, 422)
	kind, body = cfMultipart(t, `{"main_module":"worker.js","compatibility_date":"2099-01-01"}`, `export default {}`)
	request("PUT", base+"/staging/scripts/invalid-date", kind, body, session.Token, 422)
	items, err := service.DispatchNamespacesForOrg(org.ID)
	must(err)
	apps, err := service.NamespaceScripts(org.ID, items[0].ID)
	must(err)
	if len(apps) != 0 {
		t.Fatal("unsupported upload created a Worker")
	}
	unauthenticated := NewServer(service)
	unauthenticated.SetCloudflareCompatibilityEnabled(true)
	w := httptest.NewRecorder()
	unauthenticated.ServeHTTP(w, httptest.NewRequest("GET", base, nil))
	if w.Code != 503 {
		t.Fatal("compatibility adapter used legacy unauthenticated mode")
	}
}
