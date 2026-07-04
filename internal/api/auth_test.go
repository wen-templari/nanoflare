package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
