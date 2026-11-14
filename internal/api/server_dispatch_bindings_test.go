package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestOrgWorkerDispatchBindingsEndpoint(t *testing.T) {
	store := nanoflare.NewStore()
	service := nanoflare.NewServiceWithObjects(store, &hostnamePublisher{store: config.NewTraefikStore("", "127.0.0.1")}, newAPIObjectStore())
	auth := nanoflare.NewControlAuthService(store, "test-secret")
	auth.SetTestHashCost(4)
	session, err := auth.Signup(nanoflare.SignupInput{Email: "vars-owner@example.com", Password: "password", OrganizationName: "Vars"})
	if err != nil {
		t.Fatal(err)
	}
	org, err := auth.CreateOrganization(session.User.ID, nanoflare.CreateOrganizationInput{Name: "Vars"})
	if err != nil {
		t.Fatal(err)
	}
	app, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: org.ID, Name: "Vars", Hostname: "vars-api.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	base, err := service.DeployForOrg(org.ID, app.ID, nanoflare.DeployInput{Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}, Format: "modules", CompatibilityDate: "2025-12-10", Assets: []nanoflare.AssetFile{{Path: "index.html", Data: []byte("original asset")}}})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServerWithControlAuth(service, nil, "", nil, auth)
	path := "/v1/organizations/" + org.ID + "/workers/" + app.ID + "/dispatch-bindings"
	input := nanoflare.UpdateDispatchBindingsInput{DispatchNamespaces: []nanoflare.DispatchBinding{}, ExpectedDeploymentID: base.ID, IdempotencyKey: "api-request", CommitMessage: "publication marker"}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := func(path, token string, body []byte, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("PUT status=%d want=%d body=%s", w.Code, status, w.Body.String())
		}
		return w
	}
	request(path, "", body, http.StatusUnauthorized)
	readOnly, err := auth.CreatePersonalAccessToken(session.User.ID, nanoflare.CreatePersonalAccessTokenInput{Name: "Read", ScopeType: nanoflare.PATScopeTypeOrg, OrgID: org.ID, Scopes: []string{"workers:read"}})
	if err != nil {
		t.Fatal(err)
	}
	request(path, readOnly.Token, body, http.StatusForbidden)
	writer, err := auth.CreatePersonalAccessToken(session.User.ID, nanoflare.CreatePersonalAccessTokenInput{Name: "Deploy", ScopeType: nanoflare.PATScopeTypeOrg, OrgID: org.ID, Scopes: []string{"deployments:write", "namespaces:dispatch"}})
	if err != nil {
		t.Fatal(err)
	}
	deploymentOnly, err := auth.CreatePersonalAccessToken(session.User.ID, nanoflare.CreatePersonalAccessTokenInput{Name: "Deploy only", ScopeType: nanoflare.PATScopeTypeOrg, OrgID: org.ID, Scopes: []string{"deployments:write"}})
	if err != nil {
		t.Fatal(err)
	}
	request(path, deploymentOnly.Token, body, http.StatusForbidden)
	dispatchOnly, err := auth.CreatePersonalAccessToken(session.User.ID, nanoflare.CreatePersonalAccessTokenInput{Name: "Dispatch only", ScopeType: nanoflare.PATScopeTypeOrg, OrgID: org.ID, Scopes: []string{"namespaces:dispatch"}})
	if err != nil {
		t.Fatal(err)
	}
	request(path, dispatchOnly.Token, body, http.StatusForbidden)
	for _, bad := range []string{`{}`, `{"dispatch_namespaces":null}`, `{"dispatch_namespaces":[],"expected_deployment_id":"base"}`, `{"dispatch_namespaces":[],"files":[]}`, `{"dispatch_namespaces":{}}`} {
		request(path, writer.Token, []byte(bad), http.StatusBadRequest)
	}
	w := request(path, writer.Token, body, http.StatusOK)
	var result nanoflare.VarsUpdateResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ID == base.ID || result.BaseDeploymentID != base.ID || result.CommitMessage != input.CommitMessage || result.CreatedBy == "" {
		t.Fatalf("result=%#v", result)
	}
	replay := request(path, writer.Token, body, http.StatusOK)
	if replay.Body.String() != w.Body.String() {
		t.Fatalf("retry changed response: %s", replay.Body.String())
	}
	input.IdempotencyKey = "stale"
	stale, _ := json.Marshal(input)
	request(path, writer.Token, stale, http.StatusConflict)
	input.IdempotencyKey = "api-request"
	input.CommitMessage = "other"
	different, _ := json.Marshal(input)
	request(path, writer.Token, different, http.StatusConflict)

}
