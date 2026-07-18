package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestUpdateAppPersistsProtectedRoutes(t *testing.T) {
	service := nanoflare.NewService(nanoflare.NewStore(), config.NewWriter(
		filepath.Join(t.TempDir(), "workerd.capnp"),
		filepath.Join(t.TempDir(), "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	))
	server := NewServer(service)
	app := createApp(t, server, "Secure", "secure.example.com")

	body := bytes.NewBufferString(`{"auth":{"protected_routes":["/admin/*","/reports"]}}`)
	request := httptest.NewRequest(http.MethodPatch, "/v1/apps/"+app.ID, body)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %q", recorder.Code, recorder.Body.String())
	}

	var updated nanoflare.App
	if err := json.Unmarshal(recorder.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if len(updated.Auth.ProtectedRoutes) != 2 || updated.Auth.ProtectedRoutes[0] != "/admin/*" {
		t.Fatalf("auth = %#v", updated.Auth)
	}
}

func TestVerifyAuthForwardsJWTAndEmail(t *testing.T) {
	service := nanoflare.NewService(nanoflare.NewStore(), config.NewWriter(
		filepath.Join(t.TempDir(), "workerd.capnp"),
		filepath.Join(t.TempDir(), "traefik.yml"),
		"http://nanoflared/internal/auth/verify",
		"127.0.0.1",
	))
	server := NewServerWithAuth(service, nil, "", fakeAuthenticator{
		userInfoResult: AuthResult{
			Valid:     true,
			Subject:   "user-123",
			Email:     "worker@example.com",
			ExpiresAt: timePointer(time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)),
		},
	})

	request := httptest.NewRequest(http.MethodGet, "/internal/auth/verify", nil)
	request.Header.Set("Authorization", "Bearer jwt-token")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Nanoflare-User-JWT"); got != "jwt-token" {
		t.Fatalf("jwt header = %q", got)
	}
	if got := recorder.Header().Get("X-Nanoflare-User-Email"); got != "worker@example.com" {
		t.Fatalf("email header = %q", got)
	}
}

func TestVerifyAuthRedirectsBrowserWithoutBearerToken(t *testing.T) {
	server := NewServerWithAuth(nanoflare.NewService(nanoflare.NewStore(), &noopWriter{}), nil, "", fakeAuthenticator{
		beginAuthURL: "https://issuer.example.com/oauth2/authorize",
	})
	request := httptest.NewRequest(http.MethodGet, "/internal/auth/verify", nil)
	request.Header.Set("Accept", "text/html")
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Forwarded-Host", "worker.example.com")
	request.Header.Set("X-Forwarded-Uri", "/preview/logo.svg")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d body = %q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Location"); got != "https://issuer.example.com/oauth2/authorize" {
		t.Fatalf("location = %q", got)
	}
}

func TestVerifyAuthUsesBrowserSession(t *testing.T) {
	server := NewServerWithAuth(nanoflare.NewService(nanoflare.NewStore(), &noopWriter{}), nil, "", fakeAuthenticator{
		sessionResult: AuthResult{Valid: true, Subject: "user-123", Email: "browser@example.com"},
		sessionToken:  "session-token",
		sessionOK:     true,
	})
	request := httptest.NewRequest(http.MethodGet, "/internal/auth/verify", nil)
	request.Header.Set("Accept", "text/html")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Nanoflare-User-JWT"); got != "session-token" {
		t.Fatalf("jwt header = %q", got)
	}
	if got := recorder.Header().Get("X-Nanoflare-User-Email"); got != "browser@example.com" {
		t.Fatalf("email header = %q", got)
	}
}

func TestAuthCallbackDelegatesToBrowserAuthenticator(t *testing.T) {
	server := NewServerWithAuth(nanoflare.NewService(nanoflare.NewStore(), &noopWriter{}), nil, "", fakeAuthenticator{
		callbackRedirectURL: "https://worker.example.com/preview/logo.svg",
	})
	request := httptest.NewRequest(http.MethodGet, "/internal/auth/callback?state=abc&code=xyz", nil)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d body = %q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Location"); got != "https://worker.example.com/preview/logo.svg" {
		t.Fatalf("location = %q", got)
	}
}

func TestAuthAPIsReturnNormalizedResponses(t *testing.T) {
	expiresAt := timePointer(time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC))
	server := NewServerWithAuth(nanoflare.NewService(nanoflare.NewStore(), &noopWriter{}), nil, "", fakeAuthenticator{
		validateResult: AuthResult{Valid: true, Subject: "user-123", Email: "person@example.com", ExpiresAt: expiresAt, Claims: map[string]any{"scope": "openid"}},
		userInfoResult: AuthResult{Valid: true, Subject: "user-123", Email: "person@example.com", ExpiresAt: expiresAt, Claims: map[string]any{"scope": "openid"}},
		userInfoRaw:    map[string]any{"sub": "user-123", "email": "person@example.com"},
	})

	for _, test := range []struct {
		path string
	}{
		{path: "/v1/auth/validate"},
		{path: "/v1/auth/userinfo"},
	} {
		request := httptest.NewRequest(http.MethodPost, test.path, bytes.NewBufferString(`{"token":"jwt-token"}`))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d body = %q", test.path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestControlAuthProtectsOrgScopedAPIs(t *testing.T) {
	store := nanoflare.NewStore()
	service := nanoflare.NewService(store, &noopWriter{})
	if err := service.SetBaseHostname("workers.example.test"); err != nil {
		t.Fatal(err)
	}
	controlAuth := nanoflare.NewControlAuthService(store, "test-control-secret")
	server := NewServerWithControlAuth(service, nil, "", nil, controlAuth)

	signupBody := bytes.NewBufferString(`{"email":"admin@example.com","password":"secret","organization_name":"Acme"}`)
	signupRequest := httptest.NewRequest(http.MethodPost, "/v1/setup/signup", signupBody)
	signupRequest.Header.Set("Content-Type", "application/json")
	signupRecorder := httptest.NewRecorder()
	server.ServeHTTP(signupRecorder, signupRequest)
	if signupRecorder.Code != http.StatusCreated {
		t.Fatalf("signup status = %d body = %q", signupRecorder.Code, signupRecorder.Body.String())
	}
	var session nanoflare.AuthSession
	if err := json.Unmarshal(signupRecorder.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session.Token == "" || session.ActiveOrgID == "" {
		t.Fatalf("session = %#v", session)
	}

	createBody := bytes.NewBufferString(`{"name":"Control App","hostname":"control.example.com"}`)
	createRequest := httptest.NewRequest(http.MethodPost, "/v1/apps", createBody)
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.Header.Set("Authorization", "Bearer "+session.Token)
	createRecorder := httptest.NewRecorder()
	server.ServeHTTP(createRecorder, createRequest)
	if createRecorder.Code != http.StatusBadRequest {
		t.Fatalf("missing org status = %d body = %q", createRecorder.Code, createRecorder.Body.String())
	}

	createBody = bytes.NewBufferString(`{"name":"Control App","hostname":"control.example.com"}`)
	createRequest = httptest.NewRequest(http.MethodPost, "/v1/apps", createBody)
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.Header.Set("Authorization", "Bearer "+session.Token)
	createRequest.Header.Set("X-Nanoflare-Org-ID", session.ActiveOrgID)
	createRecorder = httptest.NewRecorder()
	server.ServeHTTP(createRecorder, createRequest)
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %q", createRecorder.Code, createRecorder.Body.String())
	}
	var app nanoflare.App
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &app); err != nil {
		t.Fatal(err)
	}
	if app.OrgID != session.ActiveOrgID {
		t.Fatalf("app org = %q, want %q", app.OrgID, session.ActiveOrgID)
	}

	for _, body := range []string{
		`{"name":"Generated App","hostname":"generated.example.com"}`,
		`{"name":"Third App","hostname":"third.example.com"}`,
	} {
		createRequest = httptest.NewRequest(http.MethodPost, "/v1/apps", bytes.NewBufferString(body))
		createRequest.Header.Set("Content-Type", "application/json")
		createRequest.Header.Set("Authorization", "Bearer "+session.Token)
		createRequest.Header.Set("X-Nanoflare-Org-ID", session.ActiveOrgID)
		createRecorder = httptest.NewRecorder()
		server.ServeHTTP(createRecorder, createRequest)
		if createRecorder.Code != http.StatusCreated {
			t.Fatalf("create within default org limit status = %d body = %q", createRecorder.Code, createRecorder.Body.String())
		}
	}

	createBody = bytes.NewBufferString(`{"name":"Fourth App","hostname":"fourth.example.com"}`)
	createRequest = httptest.NewRequest(http.MethodPost, "/v1/apps", createBody)
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.Header.Set("Authorization", "Bearer "+session.Token)
	createRequest.Header.Set("X-Nanoflare-Org-ID", session.ActiveOrgID)
	createRecorder = httptest.NewRecorder()
	server.ServeHTTP(createRecorder, createRequest)
	if createRecorder.Code != http.StatusPaymentRequired {
		t.Fatalf("default org limit status = %d body = %q", createRecorder.Code, createRecorder.Body.String())
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	listRequest.Header.Set("Authorization", "Bearer "+session.Token)
	listRequest.Header.Set("X-Nanoflare-Org-ID", session.ActiveOrgID)
	listRecorder := httptest.NewRecorder()
	server.ServeHTTP(listRecorder, listRequest)
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %q", listRecorder.Code, listRecorder.Body.String())
	}
	var apps []nanoflare.App
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &apps); err != nil {
		t.Fatal(err)
	}
	if len(apps) != 3 || !containsAppID(apps, app.ID) {
		t.Fatalf("apps = %#v", apps)
	}
}

func TestOpenSignupAndCreateOrganization(t *testing.T) {
	store := nanoflare.NewStore()
	service := nanoflare.NewService(store, &noopWriter{})
	controlAuth := nanoflare.NewControlAuthService(store, "test-control-secret")
	server := NewServerWithControlAuth(service, nil, "", nil, controlAuth)

	first := signupControlUser(t, server)
	signupBody := bytes.NewBufferString(`{"email":"second@example.com","password":"secret"}`)
	signupRequest := httptest.NewRequest(http.MethodPost, "/v1/auth/signup", signupBody)
	signupRequest.Header.Set("Content-Type", "application/json")
	signupRecorder := httptest.NewRecorder()
	server.ServeHTTP(signupRecorder, signupRequest)
	if signupRecorder.Code != http.StatusCreated {
		t.Fatalf("open signup status = %d body = %q", signupRecorder.Code, signupRecorder.Body.String())
	}
	var second nanoflare.AuthSession
	if err := json.Unmarshal(signupRecorder.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Organizations) != 0 || second.ActiveOrgID != "" {
		t.Fatalf("signup session = %#v", second)
	}

	createBody := bytes.NewBufferString(`{"name":"Second Org"}`)
	createRequest := httptest.NewRequest(http.MethodPost, "/v1/orgs", createBody)
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.Header.Set("Authorization", "Bearer "+second.Token)
	createRecorder := httptest.NewRecorder()
	server.ServeHTTP(createRecorder, createRequest)
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("create org status = %d body = %q", createRecorder.Code, createRecorder.Body.String())
	}
	var org nanoflare.Organization
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &org); err != nil {
		t.Fatal(err)
	}
	if org.Role != nanoflare.RoleOwner || !nanoflare.HasScope(org.Scopes, "members:owner") {
		t.Fatalf("created org = %#v", org)
	}

	secondCreateRequest := httptest.NewRequest(http.MethodPost, "/v1/orgs", bytes.NewBufferString(`{"name":"Overflow Org"}`))
	secondCreateRequest.Header.Set("Content-Type", "application/json")
	secondCreateRequest.Header.Set("Authorization", "Bearer "+second.Token)
	secondCreateRecorder := httptest.NewRecorder()
	server.ServeHTTP(secondCreateRecorder, secondCreateRequest)
	if secondCreateRecorder.Code != http.StatusPaymentRequired {
		t.Fatalf("second owned org status = %d body = %q", secondCreateRecorder.Code, secondCreateRecorder.Body.String())
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	listRequest.Header.Set("Authorization", "Bearer "+first.Token)
	listRequest.Header.Set("X-Nanoflare-Org-ID", org.ID)
	listRecorder := httptest.NewRecorder()
	server.ServeHTTP(listRecorder, listRequest)
	if listRecorder.Code != http.StatusForbidden {
		t.Fatalf("cross-org list status = %d body = %q", listRecorder.Code, listRecorder.Body.String())
	}
}

func TestInvitesGrantScopedOrgAccess(t *testing.T) {
	store := nanoflare.NewStore()
	service := nanoflare.NewService(store, &noopWriter{})
	controlAuth := nanoflare.NewControlAuthService(store, "test-control-secret")
	server := NewServerWithControlAuth(service, nil, "", nil, controlAuth)

	owner := signupControlUser(t, server)
	inviteBody := bytes.NewBufferString(`{"email":"viewer@example.com","role":"viewer"}`)
	inviteRequest := httptest.NewRequest(http.MethodPost, "/v1/orgs/"+owner.ActiveOrgID+"/invites", inviteBody)
	inviteRequest.Header.Set("Content-Type", "application/json")
	inviteRequest.Header.Set("Authorization", "Bearer "+owner.Token)
	inviteRequest.Header.Set("X-Nanoflare-Org-ID", owner.ActiveOrgID)
	inviteRecorder := httptest.NewRecorder()
	server.ServeHTTP(inviteRecorder, inviteRequest)
	if inviteRecorder.Code != http.StatusCreated {
		t.Fatalf("invite status = %d body = %q", inviteRecorder.Code, inviteRecorder.Body.String())
	}
	var invite nanoflare.InviteCreated
	if err := json.Unmarshal(inviteRecorder.Body.Bytes(), &invite); err != nil {
		t.Fatal(err)
	}

	signupBody := bytes.NewBufferString(`{"email":"viewer@example.com","password":"secret"}`)
	signupRequest := httptest.NewRequest(http.MethodPost, "/v1/auth/signup", signupBody)
	signupRequest.Header.Set("Content-Type", "application/json")
	signupRecorder := httptest.NewRecorder()
	server.ServeHTTP(signupRecorder, signupRequest)
	if signupRecorder.Code != http.StatusCreated {
		t.Fatalf("viewer signup status = %d body = %q", signupRecorder.Code, signupRecorder.Body.String())
	}
	var viewer nanoflare.AuthSession
	if err := json.Unmarshal(signupRecorder.Body.Bytes(), &viewer); err != nil {
		t.Fatal(err)
	}

	viewerOrgRequest := httptest.NewRequest(http.MethodPost, "/v1/orgs", bytes.NewBufferString(`{"name":"Viewer Org"}`))
	viewerOrgRequest.Header.Set("Content-Type", "application/json")
	viewerOrgRequest.Header.Set("Authorization", "Bearer "+viewer.Token)
	viewerOrgRecorder := httptest.NewRecorder()
	server.ServeHTTP(viewerOrgRecorder, viewerOrgRequest)
	if viewerOrgRecorder.Code != http.StatusCreated {
		t.Fatalf("viewer own org status = %d body = %q", viewerOrgRecorder.Code, viewerOrgRecorder.Body.String())
	}

	acceptRequest := httptest.NewRequest(http.MethodPost, "/v1/invites/"+invite.Token+"/accept", bytes.NewBufferString(`{}`))
	acceptRequest.Header.Set("Content-Type", "application/json")
	acceptRequest.Header.Set("Authorization", "Bearer "+viewer.Token)
	acceptRecorder := httptest.NewRecorder()
	server.ServeHTTP(acceptRecorder, acceptRequest)
	if acceptRecorder.Code != http.StatusOK {
		t.Fatalf("accept status = %d body = %q", acceptRecorder.Code, acceptRecorder.Body.String())
	}

	createBody := bytes.NewBufferString(`{"name":"Viewer App","hostname":"viewer.example.com"}`)
	createRequest := httptest.NewRequest(http.MethodPost, "/v1/apps", createBody)
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.Header.Set("Authorization", "Bearer "+viewer.Token)
	createRequest.Header.Set("X-Nanoflare-Org-ID", owner.ActiveOrgID)
	createRecorder := httptest.NewRecorder()
	server.ServeHTTP(createRecorder, createRequest)
	if createRecorder.Code != http.StatusForbidden {
		t.Fatalf("viewer create status = %d body = %q", createRecorder.Code, createRecorder.Body.String())
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	listRequest.Header.Set("Authorization", "Bearer "+viewer.Token)
	listRequest.Header.Set("X-Nanoflare-Org-ID", owner.ActiveOrgID)
	listRecorder := httptest.NewRecorder()
	server.ServeHTTP(listRecorder, listRequest)
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("viewer list status = %d body = %q", listRecorder.Code, listRecorder.Body.String())
	}
}

func TestPendingInviteCanBeRevoked(t *testing.T) {
	store := nanoflare.NewStore()
	service := nanoflare.NewService(store, &noopWriter{})
	controlAuth := nanoflare.NewControlAuthService(store, "test-control-secret")
	server := NewServerWithControlAuth(service, nil, "", nil, controlAuth)

	owner := signupControlUser(t, server)
	inviteBody := bytes.NewBufferString(`{"email":"pending@example.com","role":"member"}`)
	inviteRequest := httptest.NewRequest(http.MethodPost, "/v1/orgs/"+owner.ActiveOrgID+"/invites", inviteBody)
	inviteRequest.Header.Set("Content-Type", "application/json")
	inviteRequest.Header.Set("Authorization", "Bearer "+owner.Token)
	inviteRequest.Header.Set("X-Nanoflare-Org-ID", owner.ActiveOrgID)
	inviteRecorder := httptest.NewRecorder()
	server.ServeHTTP(inviteRecorder, inviteRequest)
	if inviteRecorder.Code != http.StatusCreated {
		t.Fatalf("invite status = %d body = %q", inviteRecorder.Code, inviteRecorder.Body.String())
	}
	var invite nanoflare.InviteCreated
	if err := json.Unmarshal(inviteRecorder.Body.Bytes(), &invite); err != nil {
		t.Fatal(err)
	}

	revokeRequest := httptest.NewRequest(http.MethodDelete, "/v1/orgs/"+owner.ActiveOrgID+"/invites/"+invite.ID, nil)
	revokeRequest.Header.Set("Authorization", "Bearer "+owner.Token)
	revokeRequest.Header.Set("X-Nanoflare-Org-ID", owner.ActiveOrgID)
	revokeRecorder := httptest.NewRecorder()
	server.ServeHTTP(revokeRecorder, revokeRequest)
	if revokeRecorder.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d body = %q", revokeRecorder.Code, revokeRecorder.Body.String())
	}

	acceptBody := bytes.NewBufferString(`{"email":"pending@example.com","password":"secret"}`)
	acceptRequest := httptest.NewRequest(http.MethodPost, "/v1/invites/"+invite.Token+"/accept", acceptBody)
	acceptRequest.Header.Set("Content-Type", "application/json")
	acceptRecorder := httptest.NewRecorder()
	server.ServeHTTP(acceptRecorder, acceptRequest)
	if acceptRecorder.Code != http.StatusBadRequest {
		t.Fatalf("accept revoked status = %d body = %q", acceptRecorder.Code, acceptRecorder.Body.String())
	}
}

func TestOAuthAppCanManageApprovedOrgResources(t *testing.T) {
	store := nanoflare.NewStore()
	service := nanoflare.NewService(store, &noopWriter{})
	controlAuth := nanoflare.NewControlAuthService(store, "test-control-secret")
	oauth := nanoflare.NewOAuthService(store)
	server := NewServerWithRuntimeAndOAuth(service, nil, "", nil, controlAuth, oauth, nil)

	session := signupControlUser(t, server)
	paidOrgID := addPaidOrgForSession(t, store, session, "org-paid-oauth-resources")
	client := createOAuthClient(t, server, session.Token, paidOrgID, []string{"apps:read", "apps:write", "kv:write"})
	assertOAuthClientInfo(t, server, client.ClientID)
	token := authorizeOAuthClient(t, server, session.Token, paidOrgID, client, []string{"apps:write", "kv:write"})

	createBody := bytes.NewBufferString(`{"name":"External App","hostname":"external.example.com","external_id":"ext-app-1"}`)
	createRequest := httptest.NewRequest(http.MethodPost, "/v1/apps", createBody)
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.Header.Set("Authorization", "Bearer "+token.AccessToken)
	createRequest.Header.Set("X-Nanoflare-Org-ID", "ignored-org")
	createRecorder := httptest.NewRecorder()
	server.ServeHTTP(createRecorder, createRequest)
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("oauth create app status = %d body = %q", createRecorder.Code, createRecorder.Body.String())
	}
	var app nanoflare.App
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &app); err != nil {
		t.Fatal(err)
	}
	if app.OrgID != paidOrgID || app.OAuthClientID != client.ClientID || app.ExternalID != "ext-app-1" {
		t.Fatalf("oauth app metadata = %#v, session org = %q client = %q", app, paidOrgID, client.ClientID)
	}

	readRequest := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	readRequest.Header.Set("Authorization", "Bearer "+token.AccessToken)
	readRecorder := httptest.NewRecorder()
	server.ServeHTTP(readRecorder, readRequest)
	if readRecorder.Code != http.StatusForbidden {
		t.Fatalf("missing read scope status = %d body = %q", readRecorder.Code, readRecorder.Body.String())
	}

	namespaceBody := bytes.NewBufferString(`{"name":"external-cache","external_id":"ext-kv-1"}`)
	namespaceRequest := httptest.NewRequest(http.MethodPost, "/v1/kv/namespaces", namespaceBody)
	namespaceRequest.Header.Set("Content-Type", "application/json")
	namespaceRequest.Header.Set("Authorization", "Bearer "+token.AccessToken)
	namespaceRecorder := httptest.NewRecorder()
	server.ServeHTTP(namespaceRecorder, namespaceRequest)
	if namespaceRecorder.Code != http.StatusCreated {
		t.Fatalf("oauth create namespace status = %d body = %q", namespaceRecorder.Code, namespaceRecorder.Body.String())
	}
	var namespace nanoflare.KVNamespace
	if err := json.Unmarshal(namespaceRecorder.Body.Bytes(), &namespace); err != nil {
		t.Fatal(err)
	}
	if namespace.OrgID != paidOrgID || namespace.OAuthClientID != client.ClientID || namespace.ExternalID != "ext-kv-1" {
		t.Fatalf("oauth namespace metadata = %#v", namespace)
	}

	connectionsRequest := httptest.NewRequest(http.MethodGet, "/v1/oauth/connections", nil)
	connectionsRequest.Header.Set("Authorization", "Bearer "+session.Token)
	connectionsRequest.Header.Set("X-Nanoflare-Org-ID", paidOrgID)
	connectionsRecorder := httptest.NewRecorder()
	server.ServeHTTP(connectionsRecorder, connectionsRequest)
	if connectionsRecorder.Code != http.StatusOK {
		t.Fatalf("connections status = %d body = %q", connectionsRecorder.Code, connectionsRecorder.Body.String())
	}
	var connections []nanoflare.OAuthConnection
	if err := json.Unmarshal(connectionsRecorder.Body.Bytes(), &connections); err != nil {
		t.Fatal(err)
	}
	if len(connections) != 1 || connections[0].ClientID != client.ClientID {
		t.Fatalf("connections = %#v, want client %q", connections, client.ClientID)
	}

	refreshed := refreshOAuthToken(t, server, client, token.RefreshToken)
	if refreshed.AccessToken == token.AccessToken || refreshed.RefreshToken == token.RefreshToken {
		t.Fatalf("refresh did not rotate tokens: before=%#v after=%#v", token, refreshed)
	}

	revokeBody := bytes.NewBufferString(`{"token":` + strconv.Quote(refreshed.AccessToken) + `}`)
	revokeRequest := httptest.NewRequest(http.MethodPost, "/v1/oauth/revoke", revokeBody)
	revokeRequest.Header.Set("Content-Type", "application/json")
	revokeRecorder := httptest.NewRecorder()
	server.ServeHTTP(revokeRecorder, revokeRequest)
	if revokeRecorder.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d body = %q", revokeRecorder.Code, revokeRecorder.Body.String())
	}

	createBody = bytes.NewBufferString(`{"name":"Revoked App","hostname":"revoked.example.com"}`)
	createRequest = httptest.NewRequest(http.MethodPost, "/v1/apps", createBody)
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.Header.Set("Authorization", "Bearer "+refreshed.AccessToken)
	createRecorder = httptest.NewRecorder()
	server.ServeHTTP(createRecorder, createRequest)
	if createRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token status = %d body = %q", createRecorder.Code, createRecorder.Body.String())
	}
}

func TestOAuthClientManagementIsOrgOwned(t *testing.T) {
	store := nanoflare.NewStore()
	service := nanoflare.NewService(store, &noopWriter{})
	controlAuth := nanoflare.NewControlAuthService(store, "test-control-secret")
	oauth := nanoflare.NewOAuthService(store)
	server := NewServerWithRuntimeAndOAuth(service, nil, "", nil, controlAuth, oauth, nil)

	session := signupControlUser(t, server)
	defaultClientBody := bytes.NewBufferString(`{"name":"Default External Platform","redirect_uris":["https://external.example.com/oauth/callback"],"scopes":["apps:write"]}`)
	defaultClientRequest := httptest.NewRequest(http.MethodPost, "/v1/oauth/clients", defaultClientBody)
	defaultClientRequest.Header.Set("Content-Type", "application/json")
	defaultClientRequest.Header.Set("Authorization", "Bearer "+session.Token)
	defaultClientRequest.Header.Set("X-Nanoflare-Org-ID", session.ActiveOrgID)
	defaultClientRecorder := httptest.NewRecorder()
	server.ServeHTTP(defaultClientRecorder, defaultClientRequest)
	if defaultClientRecorder.Code != http.StatusPaymentRequired {
		t.Fatalf("default org oauth client status = %d body = %q", defaultClientRecorder.Code, defaultClientRecorder.Body.String())
	}
	paidOrgID := addPaidOrgForSession(t, store, session, "org-paid-oauth-owner")
	client := createOAuthClient(t, server, session.Token, paidOrgID, []string{"apps:write", "kv:write"})

	listRequest := httptest.NewRequest(http.MethodGet, "/v1/oauth/clients", nil)
	listRequest.Header.Set("Authorization", "Bearer "+session.Token)
	listRequest.Header.Set("X-Nanoflare-Org-ID", paidOrgID)
	listRecorder := httptest.NewRecorder()
	server.ServeHTTP(listRecorder, listRequest)
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list clients status = %d body = %q", listRecorder.Code, listRecorder.Body.String())
	}
	var clients []nanoflare.OAuthClient
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &clients); err != nil {
		t.Fatal(err)
	}
	if len(clients) != 1 || clients[0].ID != client.ClientID || clients[0].OwnerOrgID != paidOrgID || len(clients[0].SecretHash) != 0 {
		t.Fatalf("clients = %#v", clients)
	}

	otherOrg := nanoflare.Organization{ID: "org-other", Name: "Other", CreatedAt: time.Now().UTC()}
	if err := store.CreateOrganization(otherOrg); err != nil {
		t.Fatal(err)
	}
	if err := store.AddUserToOrganization(session.User.ID, otherOrg.ID); err != nil {
		t.Fatal(err)
	}

	crossOrgToken := authorizeOAuthClient(t, server, session.Token, otherOrg.ID, client, []string{"apps:write"})
	if crossOrgToken.AccessToken == "" {
		t.Fatalf("cross org token = %#v", crossOrgToken)
	}

	connectionsRequest := httptest.NewRequest(http.MethodGet, "/v1/oauth/clients/"+client.ClientID+"/connections", nil)
	connectionsRequest.Header.Set("Authorization", "Bearer "+session.Token)
	connectionsRequest.Header.Set("X-Nanoflare-Org-ID", paidOrgID)
	connectionsRecorder := httptest.NewRecorder()
	server.ServeHTTP(connectionsRecorder, connectionsRequest)
	if connectionsRecorder.Code != http.StatusOK {
		t.Fatalf("client connections status = %d body = %q", connectionsRecorder.Code, connectionsRecorder.Body.String())
	}
	var clientConnections []nanoflare.OAuthClientConnection
	if err := json.Unmarshal(connectionsRecorder.Body.Bytes(), &clientConnections); err != nil {
		t.Fatal(err)
	}
	if len(clientConnections) != 1 || clientConnections[0].ClientID != client.ClientID || clientConnections[0].OrgID != otherOrg.ID || clientConnections[0].UserEmail != session.User.Email {
		t.Fatalf("client connections = %#v", clientConnections)
	}

	updateBody := bytes.NewBufferString(`{"name":"Wrong Org","redirect_uris":["https://external.example.com/oauth/callback"],"scopes":["apps:write"]}`)
	updateRequest := httptest.NewRequest(http.MethodPatch, "/v1/oauth/clients/"+client.ClientID, updateBody)
	updateRequest.Header.Set("Content-Type", "application/json")
	updateRequest.Header.Set("Authorization", "Bearer "+session.Token)
	updateRequest.Header.Set("X-Nanoflare-Org-ID", otherOrg.ID)
	updateRecorder := httptest.NewRecorder()
	server.ServeHTTP(updateRecorder, updateRequest)
	if updateRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("cross-org update status = %d body = %q", updateRecorder.Code, updateRecorder.Body.String())
	}

	updateBody = bytes.NewBufferString(`{"name":"Updated External Platform","redirect_uris":["https://external.example.com/oauth/callback"],"scopes":["apps:write"]}`)
	updateRequest = httptest.NewRequest(http.MethodPatch, "/v1/oauth/clients/"+client.ClientID, updateBody)
	updateRequest.Header.Set("Content-Type", "application/json")
	updateRequest.Header.Set("Authorization", "Bearer "+session.Token)
	updateRequest.Header.Set("X-Nanoflare-Org-ID", paidOrgID)
	updateRecorder = httptest.NewRecorder()
	server.ServeHTTP(updateRecorder, updateRequest)
	if updateRecorder.Code != http.StatusOK {
		t.Fatalf("update status = %d body = %q", updateRecorder.Code, updateRecorder.Body.String())
	}
	var updated nanoflare.OAuthClient
	if err := json.Unmarshal(updateRecorder.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Updated External Platform" || len(updated.Scopes) != 1 || updated.Scopes[0] != "apps:write" {
		t.Fatalf("updated client = %#v", updated)
	}
}

func TestOAuthClientSecretRotationAndDisable(t *testing.T) {
	store := nanoflare.NewStore()
	service := nanoflare.NewService(store, &noopWriter{})
	controlAuth := nanoflare.NewControlAuthService(store, "test-control-secret")
	oauth := nanoflare.NewOAuthService(store)
	server := NewServerWithRuntimeAndOAuth(service, nil, "", nil, controlAuth, oauth, nil)

	session := signupControlUser(t, server)
	paidOrgID := addPaidOrgForSession(t, store, session, "org-paid-oauth-secret")
	client := createOAuthClient(t, server, session.Token, paidOrgID, []string{"apps:write"})
	token := authorizeOAuthClient(t, server, session.Token, paidOrgID, client, []string{"apps:write"})

	rotateRequest := httptest.NewRequest(http.MethodPost, "/v1/oauth/clients/"+client.ClientID+"/secret", nil)
	rotateRequest.Header.Set("Authorization", "Bearer "+session.Token)
	rotateRequest.Header.Set("X-Nanoflare-Org-ID", paidOrgID)
	rotateRecorder := httptest.NewRecorder()
	server.ServeHTTP(rotateRecorder, rotateRequest)
	if rotateRecorder.Code != http.StatusOK {
		t.Fatalf("rotate status = %d body = %q", rotateRecorder.Code, rotateRecorder.Body.String())
	}
	var rotated struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(rotateRecorder.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.ClientID != client.ClientID || rotated.ClientSecret == "" || rotated.ClientSecret == client.ClientSecret {
		t.Fatalf("rotated = %#v", rotated)
	}
	oldRefreshBody, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     client.ClientID,
		"client_secret": client.ClientSecret,
		"refresh_token": token.RefreshToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	oldRefreshRequest := httptest.NewRequest(http.MethodPost, "/v1/oauth/token", bytes.NewReader(oldRefreshBody))
	oldRefreshRequest.Header.Set("Content-Type", "application/json")
	oldRefreshRecorder := httptest.NewRecorder()
	server.ServeHTTP(oldRefreshRecorder, oldRefreshRequest)
	if oldRefreshRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("old secret refresh status = %d body = %q", oldRefreshRecorder.Code, oldRefreshRecorder.Body.String())
	}

	disableRequest := httptest.NewRequest(http.MethodDelete, "/v1/oauth/clients/"+client.ClientID, nil)
	disableRequest.Header.Set("Authorization", "Bearer "+session.Token)
	disableRequest.Header.Set("X-Nanoflare-Org-ID", paidOrgID)
	disableRecorder := httptest.NewRecorder()
	server.ServeHTTP(disableRecorder, disableRequest)
	if disableRecorder.Code != http.StatusNoContent {
		t.Fatalf("disable status = %d body = %q", disableRecorder.Code, disableRecorder.Body.String())
	}

	createBody := bytes.NewBufferString(`{"name":"Disabled Client App","hostname":"disabled-client.example.com"}`)
	createRequest := httptest.NewRequest(http.MethodPost, "/v1/apps", createBody)
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.Header.Set("Authorization", "Bearer "+token.AccessToken)
	createRecorder := httptest.NewRecorder()
	server.ServeHTTP(createRecorder, createRequest)
	if createRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("disabled client token status = %d body = %q", createRecorder.Code, createRecorder.Body.String())
	}

	assertOAuthInfoRequest := httptest.NewRequest(http.MethodGet, "/v1/oauth/authorize?client_id="+url.QueryEscape(client.ClientID)+"&redirect_uri="+url.QueryEscape("https://external.example.com/oauth/callback")+"&scope=apps:write", nil)
	assertOAuthInfoRequest.Header.Set("Accept", "application/json")
	assertOAuthInfoRecorder := httptest.NewRecorder()
	server.ServeHTTP(assertOAuthInfoRecorder, assertOAuthInfoRequest)
	if assertOAuthInfoRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("disabled authorize info status = %d body = %q", assertOAuthInfoRecorder.Code, assertOAuthInfoRecorder.Body.String())
	}

	restoreRequest := httptest.NewRequest(http.MethodPost, "/v1/oauth/clients/"+client.ClientID+"/restore", nil)
	restoreRequest.Header.Set("Authorization", "Bearer "+session.Token)
	restoreRequest.Header.Set("X-Nanoflare-Org-ID", paidOrgID)
	restoreRecorder := httptest.NewRecorder()
	server.ServeHTTP(restoreRecorder, restoreRequest)
	if restoreRecorder.Code != http.StatusOK {
		t.Fatalf("restore status = %d body = %q", restoreRecorder.Code, restoreRecorder.Body.String())
	}
	var restored nanoflare.OAuthClient
	if err := json.Unmarshal(restoreRecorder.Body.Bytes(), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.ID != client.ClientID || restored.Disabled {
		t.Fatalf("restored client = %#v", restored)
	}

	createRecorder = httptest.NewRecorder()
	server.ServeHTTP(createRecorder, createRequest)
	if createRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("restored client should not revive revoked token: status = %d body = %q", createRecorder.Code, createRecorder.Body.String())
	}

	restoredToken := authorizeOAuthClient(t, server, session.Token, paidOrgID, oauthClientFixture{ClientID: client.ClientID, ClientSecret: rotated.ClientSecret}, []string{"apps:write"})
	createBody = bytes.NewBufferString(`{"name":"Restored Client App","hostname":"restored-client.example.com"}`)
	createRequest = httptest.NewRequest(http.MethodPost, "/v1/apps", createBody)
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.Header.Set("Authorization", "Bearer "+restoredToken.AccessToken)
	createRecorder = httptest.NewRecorder()
	server.ServeHTTP(createRecorder, createRequest)
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("restored client token status = %d body = %q", createRecorder.Code, createRecorder.Body.String())
	}
}

type oauthClientFixture struct {
	ClientID     string
	ClientSecret string
}

func signupControlUser(t *testing.T, server http.Handler) nanoflare.AuthSession {
	t.Helper()
	signupBody := bytes.NewBufferString(`{"email":"admin@example.com","password":"secret","organization_name":"Acme"}`)
	signupRequest := httptest.NewRequest(http.MethodPost, "/v1/setup/signup", signupBody)
	signupRequest.Header.Set("Content-Type", "application/json")
	signupRecorder := httptest.NewRecorder()
	server.ServeHTTP(signupRecorder, signupRequest)
	if signupRecorder.Code != http.StatusCreated {
		t.Fatalf("signup status = %d body = %q", signupRecorder.Code, signupRecorder.Body.String())
	}
	var session nanoflare.AuthSession
	if err := json.Unmarshal(signupRecorder.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	return session
}

func addPaidOrgForSession(t *testing.T, store *nanoflare.Store, session nanoflare.AuthSession, orgID string) string {
	t.Helper()
	org := nanoflare.Organization{ID: orgID, Name: "Paid Org", UsageLevel: nanoflare.UsageLevelPaid, CreatedAt: time.Now().UTC()}
	if err := store.CreateOrganization(org); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertOrganizationMembership(nanoflare.OrganizationMembership{
		UserID:    session.User.ID,
		OrgID:     org.ID,
		Role:      nanoflare.RoleOwner,
		Scopes:    nanoflare.RoleScopes(nanoflare.RoleOwner),
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	return org.ID
}

func createOAuthClient(t *testing.T, server http.Handler, sessionToken, orgID string, scopes []string) oauthClientFixture {
	t.Helper()
	body, err := json.Marshal(nanoflare.CreateOAuthClientInput{
		Name:         "External Platform",
		RedirectURIs: []string{"https://external.example.com/oauth/callback"},
		Scopes:       scopes,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/oauth/clients", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+sessionToken)
	request.Header.Set("X-Nanoflare-Org-ID", orgID)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create oauth client status = %d body = %q", recorder.Code, recorder.Body.String())
	}
	var response struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ClientID == "" || response.ClientSecret == "" {
		t.Fatalf("oauth client response = %#v", response)
	}
	return oauthClientFixture{ClientID: response.ClientID, ClientSecret: response.ClientSecret}
}

func assertOAuthClientInfo(t *testing.T, server http.Handler, clientID string) {
	t.Helper()
	path := "/v1/oauth/authorize?client_id=" + url.QueryEscape(clientID) +
		"&redirect_uri=" + url.QueryEscape("https://external.example.com/oauth/callback") +
		"&scope=" + url.QueryEscape("apps:write kv:write")
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("oauth client info status = %d body = %q", recorder.Code, recorder.Body.String())
	}
	var info nanoflare.OAuthAuthorizeInfo
	if err := json.Unmarshal(recorder.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.ClientID != clientID || info.ClientName != "External Platform" {
		t.Fatalf("oauth client info = %#v", info)
	}
}

func authorizeOAuthClient(t *testing.T, server http.Handler, sessionToken, orgID string, client oauthClientFixture, scopes []string) nanoflare.OAuthTokenResponse {
	t.Helper()
	authorizeBody, err := json.Marshal(nanoflare.OAuthAuthorizeInput{
		ClientID:    client.ClientID,
		RedirectURI: "https://external.example.com/oauth/callback",
		Scopes:      scopes,
		State:       "state-123",
		OrgID:       orgID,
	})
	if err != nil {
		t.Fatal(err)
	}
	authorizeRequest := httptest.NewRequest(http.MethodPost, "/v1/oauth/authorize", bytes.NewReader(authorizeBody))
	authorizeRequest.Header.Set("Content-Type", "application/json")
	authorizeRequest.Header.Set("Authorization", "Bearer "+sessionToken)
	authorizeRecorder := httptest.NewRecorder()
	server.ServeHTTP(authorizeRecorder, authorizeRequest)
	if authorizeRecorder.Code != http.StatusOK {
		t.Fatalf("authorize status = %d body = %q", authorizeRecorder.Code, authorizeRecorder.Body.String())
	}
	var authorize nanoflare.OAuthAuthorizeResponse
	if err := json.Unmarshal(authorizeRecorder.Body.Bytes(), &authorize); err != nil {
		t.Fatal(err)
	}
	if authorize.Code == "" || !strings.Contains(authorize.RedirectTo, "state=state-123") {
		t.Fatalf("authorize response = %#v", authorize)
	}

	tokenBody, err := json.Marshal(map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     client.ClientID,
		"client_secret": client.ClientSecret,
		"code":          authorize.Code,
		"redirect_uri":  "https://external.example.com/oauth/callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	tokenRequest := httptest.NewRequest(http.MethodPost, "/v1/oauth/token", bytes.NewReader(tokenBody))
	tokenRequest.Header.Set("Content-Type", "application/json")
	tokenRecorder := httptest.NewRecorder()
	server.ServeHTTP(tokenRecorder, tokenRequest)
	if tokenRecorder.Code != http.StatusOK {
		t.Fatalf("token status = %d body = %q", tokenRecorder.Code, tokenRecorder.Body.String())
	}
	var token nanoflare.OAuthTokenResponse
	if err := json.Unmarshal(tokenRecorder.Body.Bytes(), &token); err != nil {
		t.Fatal(err)
	}
	if token.AccessToken == "" || token.RefreshToken == "" || token.TokenType != "Bearer" {
		t.Fatalf("token response = %#v", token)
	}
	return token
}

func refreshOAuthToken(t *testing.T, server http.Handler, client oauthClientFixture, refreshToken string) nanoflare.OAuthTokenResponse {
	t.Helper()
	body, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     client.ClientID,
		"client_secret": client.ClientSecret,
		"refresh_token": refreshToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/oauth/token", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("refresh status = %d body = %q", recorder.Code, recorder.Body.String())
	}
	var token nanoflare.OAuthTokenResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &token); err != nil {
		t.Fatal(err)
	}
	return token
}

func containsAppID(apps []nanoflare.App, appID string) bool {
	for _, app := range apps {
		if app.ID == appID {
			return true
		}
	}
	return false
}

type fakeAuthenticator struct {
	validateResult      AuthResult
	validateErr         error
	userInfoResult      AuthResult
	userInfoRaw         map[string]any
	userInfoErr         error
	sessionResult       AuthResult
	sessionToken        string
	sessionOK           bool
	beginAuthURL        string
	beginAuthErr        error
	callbackRedirectURL string
	callbackErr         error
}

func (f fakeAuthenticator) ValidateToken(context.Context, string) (AuthResult, error) {
	return f.validateResult, f.validateErr
}

func (f fakeAuthenticator) UserInfo(context.Context, string) (AuthResult, map[string]any, error) {
	if f.userInfoErr != nil {
		return AuthResult{}, nil, f.userInfoErr
	}
	return f.userInfoResult, f.userInfoRaw, nil
}

func (f fakeAuthenticator) Session(*http.Request) (AuthResult, string, bool) {
	return f.sessionResult, f.sessionToken, f.sessionOK
}

func (f fakeAuthenticator) BeginAuth(w http.ResponseWriter, r *http.Request) error {
	if f.beginAuthErr != nil {
		return f.beginAuthErr
	}
	http.Redirect(w, r, f.beginAuthURL, http.StatusFound)
	return nil
}

func (f fakeAuthenticator) HandleCallback(w http.ResponseWriter, r *http.Request) error {
	if f.callbackErr != nil {
		return f.callbackErr
	}
	http.Redirect(w, r, f.callbackRedirectURL, http.StatusFound)
	return nil
}

type noopWriter struct{}

func (noopWriter) Write([]nanoflare.ActiveDeployment) error { return nil }

func timePointer(value time.Time) *time.Time {
	return &value
}

func TestVerifyAuthRejectsInvalidToken(t *testing.T) {
	server := NewServerWithAuth(nanoflare.NewService(nanoflare.NewStore(), &noopWriter{}), nil, "", fakeAuthenticator{
		userInfoErr: errors.New("invalid token: signature mismatch"),
	})
	request := httptest.NewRequest(http.MethodGet, "/internal/auth/verify", nil)
	request.Header.Set("Authorization", "Bearer jwt-token")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %q", recorder.Code, recorder.Body.String())
	}
}
