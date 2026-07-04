package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestVerifierValidatesJWTAndLoadsUserInfo(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"authorization_endpoint": issuer + "/authorize",
				"token_endpoint":         issuer + "/token",
				"jwks_uri":               issuer + "/jwks",
				"userinfo_endpoint":      issuer + "/userinfo",
			})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"keys": []map[string]string{{
					"kid": "test-key",
					"kty": "RSA",
					"n":   base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes()),
					"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.PublicKey.E)).Bytes()),
				}},
			})
		case "/userinfo":
			_ = json.NewEncoder(w).Encode(map[string]any{"sub": "user-123", "email": "person@example.com"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	issuer = server.URL

	verifier := NewVerifier(issuer, "nanoflare", "email", server.Client())
	token := signedJWT(t, privateKey, issuer, "nanoflare", map[string]any{"sub": "user-123", "email": "person@example.com"})

	result, raw, err := verifier.UserInfo(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || result.Subject != "user-123" || result.Email != "person@example.com" {
		t.Fatalf("result = %#v", result)
	}
	if raw["email"] != "person@example.com" {
		t.Fatalf("raw = %#v", raw)
	}
}

func TestVerifierRejectsWrongAudience(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"authorization_endpoint": issuer + "/authorize",
				"token_endpoint":         issuer + "/token",
				"jwks_uri":               issuer + "/jwks",
				"userinfo_endpoint":      issuer + "/userinfo",
			})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"keys": []map[string]string{{
					"kid": "test-key",
					"kty": "RSA",
					"n":   base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes()),
					"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.PublicKey.E)).Bytes()),
				}},
			})
		case "/userinfo":
			_ = json.NewEncoder(w).Encode(map[string]any{"sub": "user-123", "email": "person@example.com"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	issuer = server.URL

	verifier := NewVerifier(issuer, "nanoflare", "email", server.Client())
	token := signedJWT(t, privateKey, issuer, "different-audience", map[string]any{"sub": "user-123"})

	_, err = verifier.ValidateToken(context.Background(), token)
	if err == nil || !strings.Contains(err.Error(), "audience mismatch") {
		t.Fatalf("error = %v", err)
	}
}

func TestVerifierValidatesECIDToken(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"authorization_endpoint": issuer + "/authorize",
				"token_endpoint":         issuer + "/token",
				"jwks_uri":               issuer + "/jwks",
				"userinfo_endpoint":      issuer + "/userinfo",
			})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"keys": []map[string]string{{
					"kid": "ec-key",
					"kty": "EC",
					"crv": "P-256",
					"x":   base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.X.Bytes()),
					"y":   base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.Y.Bytes()),
				}},
			})
		case "/userinfo":
			_ = json.NewEncoder(w).Encode(map[string]any{"sub": "user-123", "email": "person@example.com"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	issuer = server.URL

	verifier := NewVerifier(issuer, "nanoflare", "email", server.Client())
	token := signedECDSAJWT(t, privateKey, issuer, "nanoflare", "ec-key", map[string]any{"sub": "user-123", "email": "person@example.com"})

	result, raw, err := verifier.UserInfo(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || result.Subject != "user-123" || result.Email != "person@example.com" {
		t.Fatalf("result = %#v", result)
	}
	if raw["email"] != "person@example.com" {
		t.Fatalf("raw = %#v", raw)
	}
}

func TestVerifierBrowserFlowCreatesSession(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"authorization_endpoint": issuer + "/authorize",
				"token_endpoint":         issuer + "/token",
				"jwks_uri":               issuer + "/jwks",
				"userinfo_endpoint":      issuer + "/userinfo",
			})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"keys": []map[string]string{{
					"kid": "test-key",
					"kty": "RSA",
					"n":   base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes()),
					"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.PublicKey.E)).Bytes()),
				}},
			})
		case "/token":
			_ = r.ParseForm()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "opaque-access-token",
				"id_token":     signedJWT(t, privateKey, issuer, "client-id", map[string]any{"sub": "user-123", "email": "person@example.com"}),
				"token_type":   "Bearer",
				"expires_in":   1800,
			})
		case "/userinfo":
			_ = json.NewEncoder(w).Encode(map[string]any{"sub": "user-123", "email": "person@example.com"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	issuer = server.URL

	verifier := NewBrowserVerifier(issuer, "nanoflare", "email", "client-id", "secret", "https://nanoflare.example.com:8443", ".local.nbtca.space", server.Client())
	verifyRequest := httptest.NewRequest(http.MethodGet, "/internal/auth/verify", nil)
	verifyRequest.Header.Set("X-Forwarded-Proto", "https")
	verifyRequest.Header.Set("X-Forwarded-Host", "worker.example.com")
	verifyRequest.Header.Set("X-Forwarded-Uri", "/preview/logo.svg")
	verifyRecorder := httptest.NewRecorder()
	if err := verifier.BeginAuth(verifyRecorder, verifyRequest); err != nil {
		t.Fatal(err)
	}
	if verifyRecorder.Code != http.StatusFound {
		t.Fatalf("status = %d body = %q", verifyRecorder.Code, verifyRecorder.Body.String())
	}
	location := verifyRecorder.Header().Get("Location")
	if location == "" {
		t.Fatal("missing authorize redirect")
	}
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatal("missing state")
	}
	callbackRequest := httptest.NewRequest(http.MethodGet, "/internal/auth/callback?state="+url.QueryEscape(state)+"&code=oauth-code", nil)
	callbackRecorder := httptest.NewRecorder()
	if err := verifier.HandleCallback(callbackRecorder, callbackRequest); err != nil {
		t.Fatal(err)
	}
	if callbackRecorder.Code != http.StatusFound {
		t.Fatalf("callback status = %d body = %q", callbackRecorder.Code, callbackRecorder.Body.String())
	}
	if got := callbackRecorder.Header().Get("Location"); got != "https://worker.example.com/preview/logo.svg" {
		t.Fatalf("callback location = %q", got)
	}
	response := callbackRecorder.Result()
	cookies := response.Cookies()
	if len(cookies) == 0 {
		t.Fatal("missing session cookie")
	}
	if cookies[0].Domain != "local.nbtca.space" {
		t.Fatalf("cookie domain = %q", cookies[0].Domain)
	}
	sessionRequest := httptest.NewRequest(http.MethodGet, "/internal/auth/verify", nil)
	sessionRequest.AddCookie(cookies[0])
	result, token, ok := verifier.Session(sessionRequest)
	if !ok {
		t.Fatal("session missing")
	}
	if !result.Valid || result.Email != "person@example.com" || token == "" {
		t.Fatalf("session result = %#v token=%q", result, token)
	}
}

func TestVerifierBrowserFlowCreatesSessionWithoutIDToken(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"authorization_endpoint": issuer + "/authorize",
				"token_endpoint":         issuer + "/token",
				"jwks_uri":               issuer + "/jwks",
				"userinfo_endpoint":      issuer + "/userinfo",
			})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"keys": []map[string]string{{
					"kid": "test-key",
					"kty": "RSA",
					"n":   base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes()),
					"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.PublicKey.E)).Bytes()),
				}},
			})
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "opaque-access-token",
				"token_type":   "Bearer",
				"expires_in":   1800,
			})
		case "/userinfo":
			_ = json.NewEncoder(w).Encode(map[string]any{"sub": "user-123", "email": "person@example.com"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	issuer = server.URL

	verifier := NewBrowserVerifier(issuer, "nanoflare", "email", "client-id", "secret", "https://nanoflare.example.com:8443", ".local.nbtca.space", server.Client())
	verifyRequest := httptest.NewRequest(http.MethodGet, "/internal/auth/verify", nil)
	verifyRequest.Header.Set("X-Forwarded-Proto", "https")
	verifyRequest.Header.Set("X-Forwarded-Host", "worker.example.com")
	verifyRequest.Header.Set("X-Forwarded-Uri", "/preview/logo.svg")
	verifyRecorder := httptest.NewRecorder()
	if err := verifier.BeginAuth(verifyRecorder, verifyRequest); err != nil {
		t.Fatal(err)
	}
	location := verifyRecorder.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	state := parsed.Query().Get("state")
	callbackRequest := httptest.NewRequest(http.MethodGet, "/internal/auth/callback?state="+url.QueryEscape(state)+"&code=oauth-code", nil)
	callbackRecorder := httptest.NewRecorder()
	if err := verifier.HandleCallback(callbackRecorder, callbackRequest); err != nil {
		t.Fatal(err)
	}
	if callbackRecorder.Code != http.StatusFound {
		t.Fatalf("callback status = %d body = %q", callbackRecorder.Code, callbackRecorder.Body.String())
	}
}

func TestVerifierRejectsInvalidCookieDomain(t *testing.T) {
	verifier := NewBrowserVerifier("https://auth.example.com/oidc", "nanoflare", "email", "client-id", "", "https://nanoflare.local.nbtca.space:8443", ".other.example.com", nil)
	if err := verifier.ValidateBrowserConfig(); err == nil || !strings.Contains(err.Error(), "cookie domain") {
		t.Fatalf("error = %v", err)
	}
}

func signedJWT(t *testing.T, privateKey *rsa.PrivateKey, issuer, audience string, extraClaims map[string]any) string {
	t.Helper()
	headerPayload := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(data)
	}
	claims := map[string]any{
		"iss": issuer,
		"aud": audience,
		"exp": time.Now().Add(30 * time.Minute).Unix(),
	}
	for key, value := range extraClaims {
		claims[key] = value
	}
	header := headerPayload(map[string]any{"alg": "RS256", "typ": "JWT", "kid": "test-key"})
	payload := headerPayload(claims)
	signingInput := header + "." + payload
	sum := crypto.SHA256.New()
	_, _ = sum.Write([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, sum.Sum(nil))
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func signedECDSAJWT(t *testing.T, privateKey *ecdsa.PrivateKey, issuer, audience, kid string, extraClaims map[string]any) string {
	t.Helper()
	headerPayload := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(data)
	}
	claims := map[string]any{
		"iss": issuer,
		"aud": audience,
		"exp": time.Now().Add(30 * time.Minute).Unix(),
	}
	for key, value := range extraClaims {
		claims[key] = value
	}
	header := headerPayload(map[string]any{"alg": "ES256", "typ": "JWT", "kid": kid})
	payload := headerPayload(claims)
	signingInput := header + "." + payload
	sum := crypto.SHA256.New()
	_, _ = sum.Write([]byte(signingInput))
	digest := sum.Sum(nil)
	r, s, err := ecdsa.Sign(rand.Reader, privateKey, digest)
	if err != nil {
		t.Fatal(err)
	}
	size := (privateKey.Curve.Params().BitSize + 7) / 8
	signature := make([]byte, size*2)
	r.FillBytes(signature[:size])
	s.FillBytes(signature[size:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}
