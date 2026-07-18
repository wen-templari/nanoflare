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

	createBody = bytes.NewBufferString(`{"name":"Generated App"}`)
	createRequest = httptest.NewRequest(http.MethodPost, "/v1/apps", createBody)
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.Header.Set("Authorization", "Bearer "+session.Token)
	createRequest.Header.Set("X-Nanoflare-Org-ID", session.ActiveOrgID)
	createRecorder = httptest.NewRecorder()
	server.ServeHTTP(createRecorder, createRequest)
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("generated create status = %d body = %q", createRecorder.Code, createRecorder.Body.String())
	}
	var generatedApp nanoflare.App
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &generatedApp); err != nil {
		t.Fatal(err)
	}
	if generatedApp.Hostname != "generated-app-acme.workers.example.test" {
		t.Fatalf("generated hostname = %q", generatedApp.Hostname)
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
	if len(apps) != 2 || !containsAppID(apps, app.ID) || !containsAppID(apps, generatedApp.ID) {
		t.Fatalf("apps = %#v", apps)
	}
}

func TestOAuthAppCanManageApprovedOrgResources(t *testing.T) {
	store := nanoflare.NewStore()
	service := nanoflare.NewService(store, &noopWriter{})
	controlAuth := nanoflare.NewControlAuthService(store, "test-control-secret")
	oauth := nanoflare.NewOAuthService(store)
	server := NewServerWithRuntimeAndOAuth(service, nil, "", nil, controlAuth, oauth, nil)

	session := signupControlUser(t, server)
	client := createOAuthClient(t, server, session.Token, []string{"apps:read", "apps:write", "kv:write"})
	assertOAuthClientInfo(t, server, client.ClientID)
	token := authorizeOAuthClient(t, server, session.Token, session.ActiveOrgID, client, []string{"apps:write", "kv:write"})

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
	if app.OrgID != session.ActiveOrgID || app.OAuthClientID != client.ClientID || app.ExternalID != "ext-app-1" {
		t.Fatalf("oauth app metadata = %#v, session org = %q client = %q", app, session.ActiveOrgID, client.ClientID)
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
	if namespace.OrgID != session.ActiveOrgID || namespace.OAuthClientID != client.ClientID || namespace.ExternalID != "ext-kv-1" {
		t.Fatalf("oauth namespace metadata = %#v", namespace)
	}

	connectionsRequest := httptest.NewRequest(http.MethodGet, "/v1/oauth/connections", nil)
	connectionsRequest.Header.Set("Authorization", "Bearer "+session.Token)
	connectionsRequest.Header.Set("X-Nanoflare-Org-ID", session.ActiveOrgID)
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

func createOAuthClient(t *testing.T, server http.Handler, sessionToken string, scopes []string) oauthClientFixture {
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
