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
	"github.com/clas/nanoflare/internal/runtime"
)

func TestWorkerRequestAccessFlow(t *testing.T) {
	store := nanoflare.NewStore()
	service := nanoflare.NewService(store, nil)
	auth := nanoflare.NewControlAuthService(store, "worker-access-secret")
	auth.SetTestHashCost(4)
	session, err := auth.Signup(nanoflare.SignupInput{Email: "invoke@example.com", Password: "password", OrganizationName: "Invoke"})
	if err != nil {
		t.Fatal(err)
	}
	org, err := auth.CreateOrganization(session.User.ID, nanoflare.CreateOrganizationInput{Name: "Invoke"})
	if err != nil {
		t.Fatal(err)
	}
	app, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: org.ID, Name: "app", Hostname: "invoke.example.com", Auth: nanoflare.AuthConfig{ProtectedRoutes: []string{"/api/*"}}})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServerWithControlAuth(service, nil, "", nil, auth)
	endpoint := "/v1/organizations/" + org.ID + "/workers/" + app.ID + "/access-token"
	issue := func(token, endpoint string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", endpoint, strings.NewReader(`{"method":"POST","path":"/api/check?q=1"}`))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("issue status=%d want=%d body=%s", w.Code, status, w.Body.String())
		}
		return w
	}
	issue("", endpoint, 401)
	readonly, err := auth.CreatePersonalAccessToken(session.User.ID, nanoflare.CreatePersonalAccessTokenInput{Name: "Read", ScopeType: nanoflare.PATScopeTypeOrg, OrgID: org.ID, Scopes: []string{"workers:read"}})
	if err != nil {
		t.Fatal(err)
	}
	issue(readonly.Token, endpoint, 403)
	invoker, err := auth.CreatePersonalAccessToken(session.User.ID, nanoflare.CreatePersonalAccessTokenInput{Name: "Invoke", ScopeType: nanoflare.PATScopeTypeOrg, OrgID: org.ID, Scopes: []string{"workers:invoke"}})
	if err != nil {
		t.Fatal(err)
	}
	issue(invoker.Token, strings.Replace(endpoint, app.ID, "missing", 1), 404)
	issue(invoker.Token, strings.Replace(endpoint, org.ID, "other", 1), 403)
	w := issue(invoker.Token, endpoint, 200)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("token response can be cached")
	}
	var access nanoflare.WorkerAccessToken
	if err := json.Unmarshal(w.Body.Bytes(), &access); err != nil {
		t.Fatal(err)
	}
	verify := func(token, host, method, path string, status int) {
		t.Helper()
		r := httptest.NewRequest("GET", "/internal/auth/verify", nil)
		if token != "" {
			r.Header.Set(nanoflare.WorkerAccessHeader, token)
		}
		r.Header.Set("X-Forwarded-Host", host)
		r.Header.Set("X-Forwarded-Method", method)
		r.Header.Set("X-Forwarded-Uri", path)
		result := httptest.NewRecorder()
		server.ServeHTTP(result, r)
		if result.Code != status {
			t.Fatalf("verify status=%d want=%d body=%s", result.Code, status, result.Body.String())
		}
		if status == 200 && (result.Header().Get("X-Nanoflare-Principal-Type") != "agent" || result.Header().Get("X-Nanoflare-Actor") == "" || result.Header().Get("X-Nanoflare-User-JWT") != "") {
			t.Fatalf("identity headers=%v", result.Header())
		}
	}
	verify("", app.Hostname, "POST", "/api/check?q=1", 401)
	verify(access.Token, app.Hostname, "POST", "/api/check?q=1", 200)
	verify(access.Token, "other.example.com", "POST", "/api/check?q=1", 401)
	verify(access.Token, app.Hostname, "GET", "/api/check?q=1", 401)
	verify(access.Token, app.Hostname, "POST", "/api/other?q=1", 401)
	verify(access.Token, app.Hostname, "POST", "/api/check?q=2", 401)
	// Even an application bearer accepted by the user authenticator must not turn
	// this machine credential into an end-user assertion.
	server.auth = fakeAuthenticator{userInfoResult: AuthResult{Valid: true, Email: "end-user@example.com"}}
	// The worker receives a verified machine principal, no access secret or user assertion.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(nanoflare.WorkerAccessHeader) != "" || r.Header.Get("X-Nanoflare-User-JWT") != "" || r.Header.Get("X-Nanoflare-User-Email") != "" {
			t.Error("credential or end-user identity leaked to worker")
		}
		if r.Header.Get("X-Nanoflare-Principal-Type") != "agent" || r.Header.Get("X-Nanoflare-Actor") != "invoke@example.com" {
			t.Errorf("invalid machine identity: %v", r.Header)
		}
		if r.Header.Get("Authorization") != "Bearer application-token" {
			t.Error("application authentication was changed")
		}
		var body bytes.Buffer
		body.ReadFrom(r.Body)
		if body.String() != "payload" || r.URL.RequestURI() != "/api/check?q=1" {
			t.Error("worker request changed")
		}
		w.WriteHeader(204)
	}))
	defer upstream.Close()
	r := httptest.NewRequest("POST", "https://"+app.Hostname+"/api/check?q=1", strings.NewReader("payload"))
	r.Header.Set(nanoflare.WorkerAccessHeader, access.Token)
	r.Header.Set("Authorization", "Bearer application-token")
	r.Header.Set("X-Nanoflare-User-JWT", "forged")
	r.Header.Set("X-Nanoflare-Actor", "forged")
	response, err := server.workerResponse(r, runtime.EnsuredWorker{URL: upstream.URL}, "/api/check", "", app.Auth)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatalf("worker status=%d", response.StatusCode)
	}
	app.Hostname = "renamed.example.com"
	if err := store.UpdateApp(app); err != nil {
		t.Fatal(err)
	}
	verify(access.Token, "invoke.example.com", "POST", "/api/check?q=1", 401)
}

func TestWorkerAccessGatewayScopesAssets(t *testing.T) {
	store := nanoflare.NewStore()
	service := nanoflare.NewServiceWithObjects(store, &hostnamePublisher{store: config.NewTraefikStore("", "127.0.0.1")}, newAPIObjectStore())
	auth := nanoflare.NewControlAuthService(store, "gateway-secret")
	if err := store.CreateOrganization(nanoflare.Organization{ID: "org", Name: "Gateway"}); err != nil {
		t.Fatal(err)
	}
	app, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "org", Name: "gateway", Hostname: "gateway.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.DeployForOrg(app.OrgID, app.ID, nanoflare.DeployInput{Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}, Format: "modules", CompatibilityDate: "2025-12-10", Assets: []nanoflare.AssetFile{{Path: "private.json", Data: []byte(`{"ok":true}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	access, err := auth.IssueWorkerAccessToken(app, "agent", nanoflare.WorkerAccessRequest{Method: "GET", Path: "/private.json?x=1"})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServerWithControlAuth(service, nil, "", nil, auth)
	for _, tc := range []struct {
		name, token, host, method, path string
		status                          int
	}{
		{"authorized", access.Token, app.Hostname, "GET", "/private.json?x=1", 200},
		{"empty", "", app.Hostname, "GET", "/private.json?x=1", 401},
		{"forged", "nfwa_forged.forged", app.Hostname, "GET", "/private.json?x=1", 401},
		{"wrong-host", access.Token, "other.example.com", "GET", "/private.json?x=1", 401},
		{"wrong-method", access.Token, app.Hostname, "HEAD", "/private.json?x=1", 401},
		{"wrong-query", access.Token, app.Hostname, "GET", "/private.json?x=2", 401},
		{"wrong-path", access.Token, app.Hostname, "GET", "/other.json?x=1", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "https://"+tc.host+"/internal/http/workers/"+app.ID+tc.path, nil)
			r.Header.Set(nanoflare.WorkerAccessHeader, tc.token)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
			}
			if tc.status == 200 && w.Body.String() != `{"ok":true}` {
				t.Fatalf("asset=%s", w.Body.String())
			}
		})
	}
}
