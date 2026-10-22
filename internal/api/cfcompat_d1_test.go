package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestCloudflareD1DatabaseIDLifecycle(t *testing.T) {
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
			auth := nanoflare.NewControlAuthService(repo, "cf-d1")
			auth.SetTestHashCost(4)
			session, err := auth.Signup(nanoflare.SignupInput{Email: "d1@example.com", Password: "password"})
			must(err)
			org, err := auth.CreateOrganization(session.User.ID, nanoflare.CreateOrganizationInput{Name: "d1"})
			must(err)
			service, _ := managedService(t, repo, discardWriter{})
			ns, err := service.CreateDispatchNamespace(org.ID, nanoflare.CreateDispatchNamespaceInput{Name: "sites"})
			must(err)
			db, err := service.CreateDatabase(nanoflare.CreateDatabaseInput{OrgID: org.ID, Name: "site-db"})
			must(err)
			ungranted, err := service.CreateDatabase(nanoflare.CreateDatabaseInput{OrgID: org.ID, Name: "ungranted"})
			must(err)
			_, err = service.SetNamespaceGrants(org.ID, ns.ID, nanoflare.NamespaceGrantsInput{ExpectedRevision: 1, Grants: []nanoflare.NamespaceResourceGrant{{Kind: "db", ResourceID: db.ID}}})
			must(err)
			handler := NewServerWithControlAuth(service, nil, "", nil, auth)
			handler.SetCloudflareCompatibilityEnabled(true)
			base := "/client/v4/accounts/" + org.ID + "/workers/dispatch/namespaces/sites/scripts/site"
			call := func(method, suffix, kind string, body []byte, want int) json.RawMessage {
				t.Helper()
				request := httptest.NewRequest(method, base+suffix, bytes.NewReader(body))
				request.Header.Set("Authorization", "Bearer "+session.Token)
				request.Header.Set("Content-Type", kind)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != want {
					t.Fatalf("%s %s: %d %s", method, suffix, response.Code, response.Body.String())
				}
				var envelope struct {
					Result json.RawMessage `json:"result"`
				}
				must(json.Unmarshal(response.Body.Bytes(), &envelope))
				return envelope.Result
			}
			binding := func(field, id string) string { return `{"name":"DB","type":"d1","` + field + `":"` + id + `"}` }
			code := `export default {fetch(){return new Response("ok")}}`
			for _, field := range []string{"database_id", "id"} {
				kind, body := cfMultipart(t, `{"main_module":"worker.js","compatibility_date":"2025-12-10","bindings":[`+binding(field, db.ID)+`]}`, code)
				call("PUT", "", kind, body, 200)
				result := call("GET", "/bindings", "", nil, 200)
				if !bytes.Contains(result, []byte(`"database_id":"`+db.ID+`"`)) || bytes.Contains(result, []byte(`"id":`)) {
					t.Fatalf("D1 inspection did not use current field: %s", result)
				}
			}
			// An edit which omits bindings must preserve the selected database.
			kind, body := cfSettingsMultipart(t, `{"tags":["retained"]}`)
			call("PATCH", "/settings", kind, body, 200)
			kind, body = cfSettingsMultipart(t, `{"bindings":[`+binding("database_id", db.ID)+`]}`)
			call("PATCH", "/settings", kind, body, 200)
			app, err := service.NamespaceScript(org.ID, ns.ID, "site")
			must(err)
			before, found, err := service.ManagementUploadForOrg(org.ID, app.ID, false)
			must(err)
			if !found || len(before.Deployment.Databases) != 1 || before.Deployment.Databases[0].DatabaseID != db.ID {
				t.Fatalf("missing native database binding: %#v", before.Deployment.Databases)
			}
			// Native code updates must continue projecting current D1 metadata.
			_, err = service.DeployForOrg(org.ID, app.ID, before.Deployment)
			must(err)
			result := call("GET", "/bindings", "", nil, 200)
			if !strings.Contains(string(result), `"database_id":"`+db.ID+`"`) {
				t.Fatalf("native update lost D1 metadata: %s", result)
			}
			before, _, err = service.ManagementUploadForOrg(org.ID, app.ID, false)
			must(err)
			kind, body = cfSettingsMultipart(t, `{"bindings":[`+binding("database_id", ungranted.ID)+`]}`)
			call("PATCH", "/settings", kind, body, 403)
			kind, body = cfSettingsMultipart(t, `{"bindings":[{"name":"DB","type":"d1","database_id":"`+db.ID+`","id":"other"}]}`)
			call("PATCH", "/settings", kind, body, 422)
			after, _, err := service.ManagementUploadForOrg(org.ID, app.ID, false)
			must(err)
			if after.DeploymentID != before.DeploymentID || after.State.Revision != before.State.Revision {
				t.Fatal("rejected D1 edit changed uploaded state")
			}
		})
	}
}
