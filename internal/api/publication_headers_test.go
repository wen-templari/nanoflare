package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

func TestWorkerGatewayUsesOnlyTrustedRuntimeHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Nanoflare-Spoofed") != "" {
			t.Error("client supplied reserved header reached runtime")
		}
		if r.Header.Get("X-Nanoflare-Execution") != "trusted-execution" {
			t.Error("trusted execution identity not forwarded")
		}
		if r.Header.Get("X-Nanoflare-Runner-Token") != "trusted-runner" {
			t.Error("trusted runner credential not forwarded")
		}
		if r.Header.Get("Authorization") != "Bearer user" {
			t.Error("user authorization changed")
		}
		if r.Header.Get("X-Nanoflare-User-JWT") != "user" || r.Header.Get("X-Nanoflare-User-Email") != "user@example.com" {
			t.Error("ForwardAuth identity headers lost")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	server := NewServer(nanoflare.NewService(nanoflare.NewStore(), nil))
	server.auth = fakeAuthenticator{userInfoResult: AuthResult{Valid: true, Email: "user@example.com"}}
	request := httptest.NewRequest(http.MethodGet, "http://worker.example.com/hello", nil)
	request.Header.Set("X-Nanoflare-Spoofed", "attacker")
	request.Header.Set("X-Nanoflare-Execution", "attacker")
	request.Header.Set("X-Nanoflare-Runner-Token", "attacker")
	request.Header.Set("Authorization", "Bearer user")
	request.Header.Set("X-Nanoflare-User-JWT", "signed-assertion")
	request.Header.Set("X-Nanoflare-User-Email", "user@example.com")
	response, err := server.workerResponse(request, runtime.EnsuredWorker{URL: upstream.URL, Token: "trusted-runner", Headers: http.Header{"X-Nanoflare-Execution": []string{"trusted-execution"}}}, "/hello", "")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if request.Header.Get("X-Nanoflare-Execution") != "attacker" {
		t.Fatal("gateway mutated original request")
	}
}

func TestWorkerGatewayRejectsForgedIdentity(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		t.Run(map[bool]string{false: "public", true: "browser-session"}[authenticated], func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wantJWT, wantEmail := "", ""
				if authenticated {
					wantJWT, wantEmail = "session-token", "real@example.com"
				}
				if r.Header.Get("X-Nanoflare-User-JWT") != wantJWT || r.Header.Get("X-Nanoflare-User-Email") != wantEmail {
					t.Errorf("untrusted identity forwarded: %v", r.Header)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer upstream.Close()
			server := NewServer(nanoflare.NewService(nanoflare.NewStore(), nil))
			if authenticated {
				server.auth = fakeAuthenticator{sessionOK: true, sessionToken: "session-token", sessionResult: AuthResult{Valid: true, Email: "real@example.com"}}
			}
			request := httptest.NewRequest(http.MethodGet, "http://worker.example.com/", nil)
			request.Header.Set("X-Nanoflare-User-JWT", "not-a-jwt")
			request.Header.Set("X-Nanoflare-User-Email", "admin@example.com")
			response, err := server.workerResponse(request, runtime.EnsuredWorker{URL: upstream.URL}, "/", "")
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
		})
	}
}

func TestPublicWorkerSurvivesOptionalAuthenticationFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, header := range []string{"X-Nanoflare-User-JWT", "X-Nanoflare-User-Email", "X-Nanoflare-Session-Proof"} {
			if r.Header.Get(header) != "" {
				t.Errorf("untrusted %s forwarded", header)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	server := NewServer(nanoflare.NewService(nanoflare.NewStore(), nil))
	server.auth = unavailableBrowserAuthenticator{}
	request := httptest.NewRequest("GET", "https://worker.example.com/public", nil)
	request.AddCookie(&http.Cookie{Name: "nanoflare_oidc_session", Value: "session"})
	request.Header.Set("X-Nanoflare-User-JWT", "forged")
	request.Header.Set("X-Nanoflare-User-Email", "forged")
	request.Header.Set("X-Nanoflare-Session-Proof", "forged")
	response, err := server.workerResponse(request, runtime.EnsuredWorker{URL: upstream.URL}, "/public", "")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%d", response.StatusCode)
	}
}
