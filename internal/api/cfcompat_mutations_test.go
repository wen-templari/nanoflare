package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func cfSettingsMultipart(t *testing.T, data string) (string, []byte) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="settings"`)
	header.Set("Content-Type", "application/json")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	return writer.FormDataContentType(), body.Bytes()
}

func TestCloudflareSettingsSecretsAndTagsMutations(t *testing.T) {
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
			auth := nanoflare.NewControlAuthService(repo, "cf-mutations")
			auth.SetTestHashCost(4)
			session, err := auth.Signup(nanoflare.SignupInput{Email: "mutations@example.com", Password: "password"})
			must(err)
			org, err := auth.CreateOrganization(session.User.ID, nanoflare.CreateOrganizationInput{Name: "mutations"})
			must(err)
			service, codec := managedService(t, repo, discardWriter{})
			ns, err := service.CreateDispatchNamespace(org.ID, nanoflare.CreateDispatchNamespaceInput{Name: "staging"})
			must(err)
			app, err := service.CreateNamespaceScript(org.ID, ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", Execution: "dynamic"})
			must(err)
			input := nanoflare.ManagedDeployInput{Deployment: nanoflare.DeployInput{IdempotencyKey: "first", Format: "modules", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {version:'one'}"}}, Vars: map[string]json.RawMessage{"MESSAGE": json.RawMessage(`"one"`)}}, Metadata: nanoflare.ScriptManagementMetadata{Bindings: []json.RawMessage{json.RawMessage(`{"name":"MESSAGE","type":"plain_text","text":"one"}`), json.RawMessage(`{"name":"TOKEN","type":"secret_text"}`)}, Tags: []string{"first"}}, Secrets: map[string]string{"TOKEN": "original-secret-value"}}
			first, err := service.ManagedDeployForOrg(org.ID, app.ID, input)
			must(err)
			input.ExpectedRevision = 1
			input.BaseDeploymentID = first.ID
			input.Deployment.IdempotencyKey = "second"
			input.Deployment.Files[0].Content = "export default {version:'two'}"
			input.Deployment.Vars = map[string]json.RawMessage{"MESSAGE": json.RawMessage(`"two"`)}
			input.Metadata.Bindings[0] = json.RawMessage(`{"name":"MESSAGE","type":"plain_text","text":"two"}`)
			_, err = service.ManagedDeployForOrg(org.ID, app.ID, input)
			must(err)
			_, err = service.SetDeploymentTrafficForOrg(org.ID, app.ID, []nanoflare.DeploymentTraffic{{ID: first.ID, TrafficPercent: 100}})
			must(err)
			handler := NewServerWithControlAuth(service, nil, "", nil, auth)
			handler.SetCloudflareCompatibilityEnabled(true)
			base := "/client/v4/accounts/" + org.ID + "/workers/dispatch/namespaces/staging/scripts/alpha"
			call := func(method, suffix, kind string, body []byte, key, match, token string, want int) (json.RawMessage, string) {
				t.Helper()
				request := httptest.NewRequest(method, base+suffix, bytes.NewReader(body))
				request.Header.Set("Authorization", "Bearer "+token)
				if kind != "" {
					request.Header.Set("Content-Type", kind)
				}
				if key != "" {
					request.Header.Set("Idempotency-Key", key)
				}
				if match != "" {
					request.Header.Set("If-Match", match)
				}
				out := httptest.NewRecorder()
				handler.ServeHTTP(out, request)
				if out.Code != want {
					t.Fatalf("%s %s: %d %s", method, suffix, out.Code, out.Body.String())
				}
				var envelope struct {
					Success bool            `json:"success"`
					Result  json.RawMessage `json:"result"`
				}
				must(json.Unmarshal(out.Body.Bytes(), &envelope))
				if envelope.Success != (want < 300) {
					t.Fatal("wrong mutation envelope")
				}
				if strings.Contains(out.Body.String(), "secret-value") {
					t.Fatal("mutation response leaked secret")
				}
				return envelope.Result, out.Header().Get("ETag")
			}
			assertActive := func(want string) {
				t.Helper()
				active, err := repo.ActiveDeploymentsForApp(app.ID)
				must(err)
				if len(active) != 1 || active[0].Deployment.ID != want {
					t.Fatal("metadata operation changed traffic")
				}
			}
			workerWriter, err := auth.CreatePersonalAccessToken(session.User.ID, nanoflare.CreatePersonalAccessTokenInput{Name: "tags", ScopeType: "org", OrgID: org.ID, Scopes: []string{"workers:write"}})
			must(err)
			secretWriter, err := auth.CreatePersonalAccessToken(session.User.ID, nanoflare.CreatePersonalAccessTokenInput{Name: "secrets", ScopeType: "org", OrgID: org.ID, Scopes: []string{"secrets:write"}})
			must(err)
			call("PUT", "/tags", "application/json", []byte(`["group/a","retain"]`), "tags", "", workerWriter.Token, 200)
			assertActive(first.ID)
			call("DELETE", "/tags/group%2Fa", "", nil, "delete-tag", "", workerWriter.Token, 200)
			assertActive(first.ID)
			tags, _ := call("GET", "/tags", "", nil, "", "", session.Token, 200)
			if string(tags) != `["retain"]` {
				t.Fatal("encoded tag deletion failed", string(tags))
			}
			call("PUT", "/tags/group%2Fa", "", nil, "add-tag", "", workerWriter.Token, 200)
			call("PUT", "/tags/group%2Fa", "", nil, "add-tag-again", "", workerWriter.Token, 200)
			assertActive(first.ID)
			tags, _ = call("GET", "/tags", "", nil, "", "", session.Token, 200)
			if string(tags) != `["retain","group/a"]` {
				t.Fatal("single tag addition lost or duplicated tags", string(tags))
			}
			call("PUT", "/tags", "application/json", []byte(`["1","2","3","4","5","6","7","8","9"]`), "tag-limit", "", workerWriter.Token, 422)
			for _, tc := range []struct {
				query         string
				status, count int
			}{
				{"?tags=group%2Fa%3Ayes", 200, 1},
				{"?tags=group%2Fa%3Ano", 200, 0},
				{"?tags=absent%3Ano", 200, 1},
				{"?tags=absent%3Ayes", 200, 0},
				{"?tags=retain:maybe", 422, 0},
				{"?tags=retain:yes&tags=first:yes", 422, 0},
				{"?tags=retain:yes,first:yes", 422, 0},
				{"?page=1", 422, 0},
			} {
				request := httptest.NewRequest("GET", strings.TrimSuffix(base, "/alpha")+tc.query, nil)
				request.Header.Set("Authorization", "Bearer "+session.Token)
				out := httptest.NewRecorder()
				handler.ServeHTTP(out, request)
				if out.Code != tc.status {
					t.Fatalf("list %s: %d %s", tc.query, out.Code, out.Body.String())
				}
				if tc.status == 200 {
					var envelope struct {
						Result []struct {
							ID string `json:"id"`
						} `json:"result"`
					}
					must(json.Unmarshal(out.Body.Bytes(), &envelope))
					if len(envelope.Result) != tc.count || (tc.count == 1 && envelope.Result[0].ID != "alpha") {
						t.Fatalf("filter %s returned %s", tc.query, out.Body.String())
					}
				}
			}
			secretBody := []byte(`{"name":"TOKEN","type":"secret_text","text":"updated-secret-value"}`)
			call("PUT", "/secrets", "application/json", secretBody, "denied", "", secretWriter.Token, 403)
			_, secretETag := call("PUT", "/secrets", "application/json", secretBody, "secret", "", session.Token, 200)
			state, _, err := service.ScriptManagementForOrg(org.ID, app.ID)
			must(err)
			artifact, err := repo.GetDeploymentArtifact(app.ID, state.DeploymentID)
			must(err)
			if !strings.Contains(artifact.Deployment.Files[0].Content, "'two'") || string(artifact.Deployment.Vars["MESSAGE"]) != `"two"` {
				t.Fatal("secret update used serving rollback rather than latest upload")
			}
			value, err := codec.Decrypt(artifact.Secrets[0].Nonce, artifact.Secrets[0].Ciphertext)
			must(err)
			if value != "updated-secret-value" {
				t.Fatal("secret mutation not captured")
			}
			call("GET", "/secrets/TOKEN", "", nil, "", "", session.Token, 200)
			call("GET", "/secrets/missing", "", nil, "", "", session.Token, 404)
			kind, body := cfSettingsMultipart(t, `{"compatibility_flags":[]}`)
			call("PATCH", "/settings", kind, body, "stale", `"not-the-latest"`, session.Token, 412)
			_, settingsETag := call("PATCH", "/settings", kind, body, "settings", secretETag, session.Token, 200)
			state, _, err = service.ScriptManagementForOrg(org.ID, app.ID)
			must(err)
			after, err := repo.GetDeploymentArtifact(app.ID, state.DeploymentID)
			must(err)
			if len(after.Secrets) != 1 || string(after.Deployment.Vars["MESSAGE"]) != `"two"` {
				t.Fatal("omitted settings fields were cleared")
			}
			_, err = service.SetDeploymentTrafficForOrg(org.ID, app.ID, []nanoflare.DeploymentTraffic{{ID: first.ID, TrafficPercent: 100}})
			must(err)
			kind, body = cfSettingsMultipart(t, `{"compatibility_flags":[]}`)
			_, retryETag := call("PATCH", "/settings", kind, body, "settings", secretETag, session.Token, 200)
			if retryETag != settingsETag {
				t.Fatal("mutation retry created another revision")
			}
			assertActive(first.ID)
			call("PUT", "/secrets", "application/json", []byte(`{"name":"TOKEN","type":"secret_text","text":"changed-secret-value"}`), "secret", "", session.Token, 409)
			call("DELETE", "/secrets/TOKEN", "", nil, "remove-secret", "", session.Token, 200)
			call("DELETE", "/secrets/TOKEN", "", nil, "remove-secret", "", session.Token, 200)
			call("DELETE", "/secrets/missing", "", nil, "missing-secret", "", session.Token, 404)
			kind, body = cfSettingsMultipart(t, `{"bindings":[{"name":"CONFIG","type":"json","json":{"answer":9007199254740993}}],"tags":[]}`)
			call("PATCH", "/settings", kind, body, "replace", "", session.Token, 200)
			state, _, err = service.ScriptManagementForOrg(org.ID, app.ID)
			must(err)
			after, err = repo.GetDeploymentArtifact(app.ID, state.DeploymentID)
			must(err)
			var config struct {
				Answer json.Number `json:"answer"`
			}
			must(json.Unmarshal(after.Deployment.Vars["CONFIG"], &config))
			if len(after.Secrets) != 0 || len(after.Deployment.Vars) != 1 || config.Answer.String() != "9007199254740993" || len(state.Metadata.Tags) != 0 {
				t.Fatal("explicit settings replacement changed JSON or retention semantics")
			}
			for _, invalid := range []string{`{"bindings":null}`, `{"compatibility_date":"2099-01-01"}`, `{"limits":{"cpu_ms":1}}`, `{"bindings":[{"name":"CONFIG","NAME":"alias","type":"json","json":1}]}`} {
				kind, body = cfSettingsMultipart(t, invalid)
				call("PATCH", "/settings", kind, body, "invalid-"+invalid, "", session.Token, 422)
			}
			call("PUT", "/tags", "application/json", []byte(`["duplicate","duplicate"]`), "bad-tags", "", session.Token, 422)
		})
	}
}
