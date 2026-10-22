package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestCloudflareContentPreservesSnapshot(t *testing.T) {
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
			auth := nanoflare.NewControlAuthService(repo, "content-test")
			auth.SetTestHashCost(4)
			session, err := auth.Signup(nanoflare.SignupInput{Email: "content@example.com", Password: "password"})
			must(err)
			org, err := auth.CreateOrganization(session.User.ID, nanoflare.CreateOrganizationInput{Name: "content"})
			must(err)
			service, codec := managedService(t, repo, discardWriter{})
			ns, err := service.CreateDispatchNamespace(org.ID, nanoflare.CreateDispatchNamespaceInput{Name: "staging"})
			must(err)
			app, err := service.CreateNamespaceScript(org.ID, ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", Execution: "dynamic"})
			must(err)
			first, err := service.ManagedDeployForOrg(org.ID, app.ID, nanoflare.ManagedDeployInput{
				Deployment: nanoflare.DeployInput{IdempotencyKey: "initial", Format: "modules", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}, Vars: map[string]json.RawMessage{"MESSAGE": json.RawMessage(`"retain"`)}},
				Metadata:   nanoflare.ScriptManagementMetadata{Bindings: []json.RawMessage{json.RawMessage(`{"name":"MESSAGE","type":"plain_text","text":"retain"}`), json.RawMessage(`{"name":"TOKEN","type":"secret_text"}`)}, Tags: []string{"keep"}}, Secrets: map[string]string{"TOKEN": "never-return-secret"},
			})
			must(err)
			before, _, err := service.ManagementUploadForOrg(org.ID, app.ID, false)
			must(err)
			handler := NewServerWithControlAuth(service, nil, "", nil, auth)
			handler.SetCloudflareCompatibilityEnabled(true)
			base := "/client/v4/accounts/" + org.ID + "/workers/dispatch/namespaces/staging/scripts/alpha/content"
			call := func(method, token, metadata, code, key, main string, want int) *httptest.ResponseRecorder {
				t.Helper()
				var body []byte
				var kind string
				if method == "PUT" {
					kind, body = cfMultipart(t, metadata, code)
				}
				r := httptest.NewRequest(method, base, bytes.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+token)
				r.Header.Set("Content-Type", kind)
				r.Header.Set("Idempotency-Key", key)
				if main != "" {
					r.Header.Set("CF-WORKER-MAIN-MODULE-PART", main)
				}
				out := httptest.NewRecorder()
				handler.ServeHTTP(out, r)
				if out.Code != want {
					t.Fatalf("%s: %d %s", method, out.Code, out.Body.String())
				}
				if strings.Contains(out.Body.String(), "never-return-secret") {
					t.Fatal("content leaked secret")
				}
				return out
			}
			metadata := `{"main_module":"worker.js"}`
			code := `export default {fetch(){return new Response("updated")}}`
			writer, err := auth.CreatePersonalAccessToken(session.User.ID, nanoflare.CreatePersonalAccessTokenInput{Name: "deploy", ScopeType: "org", OrgID: org.ID, Scopes: []string{"deployments:write"}})
			must(err)
			out := call("PUT", writer.Token, metadata, code, "content", "worker.js", 200)
			latest, _, err := service.ManagementUploadForOrg(org.ID, app.ID, false)
			must(err)
			if !reflect.DeepEqual(latest.State.Metadata, before.State.Metadata) || !reflect.DeepEqual(latest.Deployment.Vars, before.Deployment.Vars) || latest.Deployment.CompatibilityDate != before.Deployment.CompatibilityDate {
				t.Fatal("content changed configuration")
			}
			artifact, err := repo.GetDeploymentArtifact(app.ID, latest.DeploymentID)
			must(err)
			if len(artifact.Secrets) != 1 {
				t.Fatal("content dropped secret")
			}
			value, err := codec.Decrypt(artifact.Secrets[0].Nonce, artifact.Secrets[0].Ciphertext)
			must(err)
			if value != "never-return-secret" {
				t.Fatal("content changed secret")
			}
			if latest.Deployment.Files[0].Content != code || out.Header().Get("ETag") != `"`+latest.DeploymentID+`"` {
				t.Fatal("content not published")
			}
			_, err = service.SetDeploymentTrafficForOrg(org.ID, app.ID, []nanoflare.DeploymentTraffic{{ID: first.ID, TrafficPercent: 100}})
			must(err)
			call("PUT", writer.Token, metadata, code, "content", "", 200)
			active, err := repo.ActiveDeploymentsForApp(app.ID)
			must(err)
			if len(active) != 1 || active[0].Deployment.ID != first.ID {
				t.Fatal("retry rewound rollback")
			}
			out = call("GET", session.Token, "", "", "", "", 200)
			if out.Header().Get("CF-WORKER-MAIN-MODULE-PART") != "worker.js" || out.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing content headers")
			}
			_, params, err := mime.ParseMediaType(out.Header().Get("Content-Type"))
			must(err)
			reader := multipart.NewReader(out.Body, params["boundary"])
			part, err := reader.NextPart()
			must(err)
			data, err := io.ReadAll(part)
			must(err)
			if part.FormName() != "worker.js" || string(data) != code {
				t.Fatal("content did not use latest upload")
			}
			if _, err = reader.NextPart(); err != io.EOF {
				t.Fatal("unexpected metadata or secret part")
			}
			call("GET", writer.Token, "", "", "", "", 403)
			call("PUT", writer.Token, metadata, code, "conflict", "other.js", 422)
			call("PUT", writer.Token, `{"main_module":"worker.js","bindings":[]}`, code, "invalid", "", 422)
			call("PUT", writer.Token, metadata, "export default {}", "content", "", 409)
		})
	}
}
