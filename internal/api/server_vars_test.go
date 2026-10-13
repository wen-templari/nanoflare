package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestOrgWorkerVarsEndpoint(t *testing.T) {
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
	path := "/v1/organizations/" + org.ID + "/workers/" + app.ID + "/vars"
	input := nanoflare.UpdateVarsInput{Vars: map[string]json.RawMessage{"TEXT": json.RawMessage(`"hello"`), "JSON": json.RawMessage(`{"beta":true}`)}, ExpectedDeploymentID: base.ID, IdempotencyKey: "api-request", CommitMessage: "publication marker"}
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
	writer, err := auth.CreatePersonalAccessToken(session.User.ID, nanoflare.CreatePersonalAccessTokenInput{Name: "Deploy", ScopeType: nanoflare.PATScopeTypeOrg, OrgID: org.ID, Scopes: []string{"deployments:write"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{}`, `{"vars":null}`, `{"vars":{},"expected_deployment_id":"base"}`, `{"vars":{},"files":[]}`, `{"vars":{}}`} {
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
	// A valid token in one organization cannot address another organization's worker.
	if err := store.CreateOrganization(nanoflare.Organization{ID: "other-vars-org", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	foreign, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "other-vars-org", Name: "Other", Hostname: "other-vars.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	request("/v1/organizations/"+org.ID+"/workers/"+foreign.ID+"/vars", writer.Token, body, http.StatusNotFound)
	request("/v1/organizations/other-vars-org/workers/"+foreign.ID+"/vars", session.Token, body, http.StatusForbidden)
	// Static assets remain accessible without re-uploading them.
	asset := httptest.NewRecorder()
	server.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/internal/http/workers/"+app.ID+"/", nil))
	if asset.Code != http.StatusOK || asset.Body.String() != "original asset" {
		t.Fatalf("asset=%d %s", asset.Code, asset.Body.String())
	}
	deployments, err := service.WorkerDeploymentsForOrg(org.ID, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deployments) != 2 {
		t.Fatalf("deployments=%#v", deployments)
	}
	if !strings.Contains(w.Body.String(), "publication marker") {
		t.Fatal("missing caller marker")
	}
}

func TestOpenAPIWorkerVarsContract(t *testing.T) {
	data, err := OpenAPIJSON()
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage
				Required   []string
			}
		}
		Paths map[string]map[string]struct {
			Description string
			Responses   map[string]json.RawMessage
		}
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	input := spec.Components.Schemas["UpdateVarsInput"]
	for _, name := range []string{"vars", "expected_deployment_id", "idempotency_key"} {
		found := false
		for _, required := range input.Required {
			if required == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s is not required", name)
		}
	}
	if _, found := input.Properties["files"]; found {
		t.Fatal("vars endpoint accepts files")
	}
	op := spec.Paths["/v1/organizations/{orgID}/workers/{workerID}/vars"]["put"]
	if op.Responses["409"] == nil || !strings.Contains(op.Description, "deployments:write") || !strings.Contains(op.Description, "without reactivation") {
		t.Fatalf("operation=%#v", op)
	}
}
