package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/nanoflare"
)

type hostnamePublisher struct {
	store *config.TraefikStore
	err   error
}

func (w *hostnamePublisher) Write(active []nanoflare.ActiveDeployment) error {
	if w.err != nil {
		return w.err
	}
	return w.store.WriteTraefik(active)
}

func TestOrgWorkerHostnamePatchAndReconciliation(t *testing.T) {
	store := nanoflare.NewStore()
	traefik := config.NewTraefikStore("http://nanoflared/internal/auth/verify", "127.0.0.1")
	publisher := &hostnamePublisher{store: traefik}
	service := nanoflare.NewServiceWithObjects(store, publisher, newAPIObjectStore())
	auth := nanoflare.NewControlAuthService(store, "test-secret")
	auth.SetTestHashCost(4)
	session, err := auth.Signup(nanoflare.SignupInput{Email: "owner@example.com", Password: "password", OrganizationName: "Sites"})
	if err != nil {
		t.Fatal(err)
	}
	org, err := auth.CreateOrganization(session.User.ID, nanoflare.CreateOrganizationInput{Name: "Sites"})
	if err != nil {
		t.Fatal(err)
	}
	session.ActiveOrgID = org.ID
	app, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: session.ActiveOrgID, Name: "Site", Hostname: "old.example.com", ExternalID: "site-id"})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := service.DeployForOrg(session.ActiveOrgID, app.ID, nanoflare.DeployInput{Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}, Assets: []nanoflare.AssetFile{{Path: "index.html", ContentType: "text/html", Data: []byte("<h1>Published site</h1>")}}, CompatibilityDate: "2025-12-10"})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServerWithControlAuth(service, traefik, "traefik-token", nil, auth)
	base := "/v1/organizations/" + session.ActiveOrgID + "/workers/" + app.ID
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	patch := func(body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := request(http.MethodPatch, base, body, session.Token)
		if w.Code != status {
			t.Fatalf("PATCH %s: status=%d body=%s", body, w.Code, w.Body.String())
		}
		return w
	}
	for _, body := range []string{`{"hostname":""}`, `{"hostname":null}`, `{"hostname":"a.example.com/path"}`, `{"hostname":"https://a.example.com"}`, `{"hostname":"a.example.com:443"}`, `{"hostname":"*.example.com"}`, `{"hostname":"ok.example.com","auth":{"protected_routes":["invalid"]}}`, `{"hostname":"ok.example.com","extra":true}`} {
		patch(body, http.StatusBadRequest)
	}
	w := patch(`{"hostname":" New.Example.COM. ","auth":{"protected_routes":["/admin/*"]}}`, http.StatusOK)
	var updated nanoflare.App
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Hostname != "new.example.com" || updated.ID != app.ID || updated.ExternalID != app.ExternalID || len(updated.Auth.ProtectedRoutes) != 1 {
		t.Fatalf("response=%#v", updated)
	}
	routing := string(traefik.TraefikConfig())
	if strings.Contains(routing, "old.example.com") || !strings.Contains(routing, "Host(`new.example.com`)") || !strings.Contains(routing, "PathPrefix(`/admin/`)") {
		t.Fatalf("routing=%s", routing)
	}
	// The published asset stays accessible through the same deployment and gateway.
	r := httptest.NewRequest(http.MethodGet, "/internal/http/workers/"+app.ID+"/", nil)
	r.Host = updated.Hostname
	asset := httptest.NewRecorder()
	server.ServeHTTP(asset, r)
	if asset.Code != http.StatusOK || asset.Body.String() != "<h1>Published site</h1>" {
		t.Fatalf("asset=%d %s", asset.Code, asset.Body.String())
	}
	patch(`{"auth":{"protected_routes":["/private"]}}`, http.StatusOK)
	patch(`{}`, http.StatusOK)
	if err := store.CreateOrganization(nanoflare.Organization{ID: "other-org", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	other, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "other-org", Name: "Other", Hostname: "taken.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	conflict := patch(`{"hostname":"taken.example.com"}`, http.StatusConflict)
	if !strings.Contains(conflict.Body.String(), "hostname is already assigned") {
		t.Fatal(conflict.Body.String())
	}
	forbidden := request(http.MethodPatch, "/v1/organizations/other-org/workers/"+other.ID, `{"hostname":"stolen.example.com"}`, session.Token)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("cross-org=%d %s", forbidden.Code, forbidden.Body.String())
	}
	missing := request(http.MethodPatch, "/v1/organizations/"+session.ActiveOrgID+"/workers/"+other.ID, `{"hostname":"stolen.example.com"}`, session.Token)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("foreign worker=%d %s", missing.Code, missing.Body.String())
	}
	unauthenticated := request(http.MethodPatch, base, `{"hostname":"stolen.example.com"}`, "")
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth=%d", unauthenticated.Code)
	}
	pat, err := auth.CreatePersonalAccessToken(session.User.ID, nanoflare.CreatePersonalAccessTokenInput{Name: "Read only", ScopeType: nanoflare.PATScopeTypeOrg, OrgID: session.ActiveOrgID, Scopes: []string{"workers:read"}})
	if err != nil {
		t.Fatal(err)
	}
	denied := request(http.MethodPatch, base, `{"hostname":"stolen.example.com"}`, pat.Token)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("missing scope=%d %s", denied.Code, denied.Body.String())
	}
	publisher.err = errors.New("publisher unavailable")
	failed := patch(`{"hostname":"final.example.com"}`, http.StatusInternalServerError)
	if !strings.Contains(failed.Body.String(), "saved but routing refresh failed") {
		t.Fatal(failed.Body.String())
	}
	get := request(http.MethodGet, base, "", session.Token)
	var detail nanoflare.WorkerDetail
	if get.Code != http.StatusOK {
		t.Fatalf("GET=%d %s", get.Code, get.Body.String())
	}
	if err := json.Unmarshal(get.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.App.Hostname != "final.example.com" || detail.Deployment == nil || detail.Deployment.ID != deployment.ID {
		t.Fatalf("detail=%#v", detail)
	}
	publisher.err = nil
	patch(`{"hostname":"final.example.com"}`, http.StatusOK)
	routing = string(traefik.TraefikConfig())
	if !strings.Contains(routing, "Host(`final.example.com`)") || strings.Contains(routing, "new.example.com") {
		t.Fatalf("retry routing=%s", routing)
	}
}

func TestOpenAPIWorkerHostnameUpdateContract(t *testing.T) {
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
	input := spec.Components.Schemas["UpdateAppInput"]
	var hostname struct{ Type string }
	if err := json.Unmarshal(input.Properties["hostname"], &hostname); err != nil {
		t.Fatal(err)
	}
	if hostname.Type != "string" {
		t.Fatalf("hostname schema=%s", input.Properties["hostname"])
	}
	for _, field := range input.Required {
		if field == "hostname" {
			t.Fatal("hostname must remain optional")
		}
	}
	operation := spec.Paths["/v1/organizations/{orgID}/workers/{workerID}"]["patch"]
	if len(operation.Responses["409"]) == 0 || !strings.Contains(operation.Description, "retry") {
		t.Fatalf("PATCH contract=%#v", operation)
	}
}
