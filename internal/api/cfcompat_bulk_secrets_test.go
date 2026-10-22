package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestCloudflareBulkSecretsAtomicLatestSnapshotAndRetry(t *testing.T) {
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
			auth := nanoflare.NewControlAuthService(repo, "bulk-secrets")
			auth.SetTestHashCost(4)
			session, err := auth.Signup(nanoflare.SignupInput{Email: "bulk@example.com", Password: "password"})
			must(err)
			org, err := auth.CreateOrganization(session.User.ID, nanoflare.CreateOrganizationInput{Name: "bulk"})
			must(err)
			writer := &managedRecordingWriter{}
			service, codec := managedService(t, repo, writer)
			ns, err := service.CreateDispatchNamespace(org.ID, nanoflare.CreateDispatchNamespaceInput{Name: "staging"})
			must(err)
			app, err := service.CreateNamespaceScript(org.ID, ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "alpha", Execution: "dynamic"})
			must(err)
			input := nanoflare.ManagedDeployInput{Deployment: nanoflare.DeployInput{IdempotencyKey: "initial", Format: "modules", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {version:'one'}"}}, Vars: map[string]json.RawMessage{"MESSAGE": json.RawMessage(`"retain"`)}}, Metadata: nanoflare.ScriptManagementMetadata{Bindings: []json.RawMessage{json.RawMessage(`{"name":"MESSAGE","type":"plain_text","text":"retain"}`), json.RawMessage(`{"name":"KEEP","type":"secret_text"}`), json.RawMessage(`{"name":"DROP","type":"secret_text"}`), json.RawMessage(`{"name":"UPDATE","type":"secret_text"}`)}, Tags: []string{"retain"}}, Secrets: map[string]string{"KEEP": "old-keep", "DROP": "old-drop", "UPDATE": "old-update"}}
			first, err := service.ManagedDeployForOrg(org.ID, app.ID, input)
			must(err)
			input.ExpectedRevision = 1
			input.BaseDeploymentID = first.ID
			input.Deployment.IdempotencyKey = "second"
			input.Deployment.Files[0].Content = "export default {version:'two'}"
			input.Secrets["KEEP"] = "new-keep"
			second, err := service.ManagedDeployForOrg(org.ID, app.ID, input)
			must(err)
			_, err = service.SetDeploymentTrafficForOrg(org.ID, app.ID, []nanoflare.DeploymentTraffic{{ID: first.ID, TrafficPercent: 100}})
			must(err)
			handler := NewServerWithControlAuth(service, nil, "", nil, auth)
			handler.SetCloudflareCompatibilityEnabled(true)
			base := "/client/v4/accounts/" + org.ID + "/workers/dispatch/namespaces/staging/scripts/alpha/secrets-bulk"
			call := func(body, key, match, token string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("PATCH", base, strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+token)
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("X-Nanoflare-Execution-Profile", "trusted_shared_workerd")
				r.Header.Set("Idempotency-Key", key)
				if match != "" {
					r.Header.Set("If-Match", match)
				}
				out := httptest.NewRecorder()
				handler.ServeHTTP(out, r)
				return out
			}
			patch := `{"secrets":{"UPDATE":{"name":"UPDATE","type":"secret_text","text":"bulk-value"},"ADD":{"name":"ADD","type":"secret_text","text":""},"DROP":null,"missing":null}}`
			secretOnly, err := auth.CreatePersonalAccessToken(session.User.ID, nanoflare.CreatePersonalAccessTokenInput{Name: "secret-only", ScopeType: "org", OrgID: org.ID, Scopes: []string{"secrets:write"}})
			must(err)
			if out := call(patch, "denied", "", secretOnly.Token); out.Code != 403 {
				t.Fatal("bulk edits bypassed deployment scope", out.Code)
			}
			beforeWrites := len(writer.writes)
			out := call(patch, "bulk", second.ID, session.Token)
			if out.Code != 200 {
				t.Fatalf("bulk patch: %d %s", out.Code, out.Body.String())
			}
			if strings.Contains(out.Body.String(), "bulk-value") || strings.Contains(out.Body.String(), "new-keep") {
				t.Fatal("bulk response leaked values")
			}
			var response struct {
				Result map[string]map[string]string `json:"result"`
			}
			must(json.Unmarshal(out.Body.Bytes(), &response))
			if len(response.Result) != 3 || response.Result["ADD"]["name"] != "ADD" || response.Result["DROP"] != nil {
				t.Fatal("bulk response does not describe resulting secrets")
			}
			state, _, err := service.ScriptManagementForOrg(org.ID, app.ID)
			must(err)
			bulkID := state.DeploymentID
			artifact, err := repo.GetDeploymentArtifact(app.ID, bulkID)
			must(err)
			values := map[string]string{}
			for _, secret := range artifact.Secrets {
				value, err := codec.Decrypt(secret.Nonce, secret.Ciphertext)
				must(err)
				values[secret.Name] = value
			}
			if len(values) != 3 || values["KEEP"] != "new-keep" || values["UPDATE"] != "bulk-value" || values["ADD"] != "" || !strings.Contains(artifact.Deployment.Files[0].Content, "'two'") {
				t.Fatal("bulk patch did not publish a complete latest snapshot")
			}
			records, err := repo.ListDeployments()
			must(err)
			if len(records) != 3 || len(writer.writes) != beforeWrites+1 || state.Revision != 3 {
				t.Fatal("bulk patch published intermediate revisions")
			}
			_, err = service.SetDeploymentTrafficForOrg(org.ID, app.ID, []nanoflare.DeploymentTraffic{{ID: first.ID, TrafficPercent: 100}})
			must(err)
			if out = call(patch, "bulk", second.ID, session.Token); out.Code != 200 {
				t.Fatalf("retry after rollback: %d %s", out.Code, out.Body.String())
			}
			active, err := repo.ActiveDeploymentsForApp(app.ID)
			must(err)
			if len(active) != 1 || active[0].Deployment.ID != first.ID {
				t.Fatal("bulk retry undid rollback")
			}
			if out = call(strings.Replace(patch, "bulk-value", "changed", 1), "bulk", second.ID, session.Token); out.Code != 409 {
				t.Fatal("changed bulk request did not conflict")
			}
			if out = call(patch, "stale", second.ID, session.Token); out.Code != 412 {
				t.Fatal("stale bulk precondition accepted")
			}
			for _, invalid := range []string{`{"secrets":null}`, `{"secrets":{"A":{"name":"B","type":"secret_text","text":"value"}}}`, `{"secrets":{"A":{"name":"A","type":"secret_key","text":"value"}}}`, `{"secrets":{"A":{"name":"A","type":"secret_text","text":null}}}`, `{"secrets":{"A":null,"A":null}}`, `{"secrets":{"A":{"name":"A","type":"secret_text","TEXT":"value"}}}`, `{"secrets":{"MESSAGE":{"name":"MESSAGE","type":"secret_text","text":"value"}}}`, `{"version_tags":{"tag":"unsupported"}}`} {
				if out = call(invalid, "invalid-"+invalid, "", session.Token); out.Code != 422 {
					t.Fatalf("invalid bulk patch: %d %s", out.Code, out.Body.String())
				}
			}
			unchanged, _, err := service.ScriptManagementForOrg(org.ID, app.ID)
			must(err)
			if unchanged.DeploymentID != bulkID {
				t.Fatal("invalid patch changed latest upload")
			}
			// Simultaneous requests sharing a precondition can publish only one
			// complete pair, whether the loser observes stale state or loses CAS.
			var wg sync.WaitGroup
			responses := make([]*httptest.ResponseRecorder, 2)
			for index := range 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					value := []string{"left", "right"}[index]
					body := `{"secrets":{"UPDATE":{"name":"UPDATE","type":"secret_text","text":"` + value + `"},"PAIR":{"name":"PAIR","type":"secret_text","text":"` + value + `"}}}`
					responses[index] = call(body, "concurrent-"+value, bulkID, session.Token)
				}()
			}
			wg.Wait()
			success := 0
			for _, response := range responses {
				if response.Code == 200 {
					success++
				} else if response.Code != 409 && response.Code != 412 {
					t.Fatalf("unexpected concurrent status %d %s", response.Code, response.Body.String())
				}
			}
			if success != 1 {
				t.Fatal("concurrent bulk requests lost precondition isolation")
			}
			state, _, err = service.ScriptManagementForOrg(org.ID, app.ID)
			must(err)
			artifact, err = repo.GetDeploymentArtifact(app.ID, state.DeploymentID)
			must(err)
			values = map[string]string{}
			for _, secret := range artifact.Secrets {
				value, err := codec.Decrypt(secret.Nonce, secret.Ciphertext)
				must(err)
				values[secret.Name] = value
			}
			if values["PAIR"] != values["UPDATE"] {
				t.Fatal("concurrent patches mixed secret values")
			}
			winner := state.DeploymentID
			failedPatch := `{"secrets":{"UPDATE":{"name":"UPDATE","type":"secret_text","text":"failed-value"},"PAIR":{"name":"PAIR","type":"secret_text","text":"failed-value"}}}`
			writer.failNext = true
			if out = call(failedPatch, "failed-publication", "", session.Token); out.Code != 400 {
				t.Fatalf("publication failure status %d %s", out.Code, out.Body.String())
			}
			active, err = repo.ActiveDeploymentsForApp(app.ID)
			must(err)
			if len(active) != 1 || active[0].Deployment.ID != winner {
				t.Fatal("failed bulk publication changed serving secrets")
			}
			if out = call(failedPatch, "failed-publication", "", session.Token); out.Code != 400 {
				t.Fatal("failed bulk retry activated partial publication")
			}
			if out = call(failedPatch, "new-publication", "", session.Token); out.Code != 200 {
				t.Fatalf("new bulk request did not recover: %d %s", out.Code, out.Body.String())
			}
			encoded, err := json.Marshal(artifact.Manifest())
			must(err)
			if bytes.Contains(encoded, []byte("bulk-value")) {
				t.Fatal("public manifest leaked secret")
			}
		})
	}
}
