package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/browserstore"
	"github.com/clas/nanoflare/internal/database"
)

func TestPendingIdentityRecoveryAndEarlyRefreshFallback(t *testing.T) {
	var issuer string
	var trustedKey *rsa.PublicKey
	var mode atomic.Int32
	var refreshes atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]string{"authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "userinfo_endpoint": issuer + "/userinfo"})
		case "/token":
			refreshes.Add(1)
			if mode.Load() == 1 {
				w.WriteHeader(503)
				return
			}
			r.ParseForm()
			if r.Form.Get("refresh_token") != "rotated-refresh" {
				t.Error("pending session did not use durable rotated token")
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "recovered-token", "refresh_token": "recovered-refresh"}) // No expires_in or ID token.
		case "/keys":
			if trustedKey == nil {
				w.WriteHeader(503)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kid": "test-key", "kty": "RSA", "n": base64.RawURLEncoding.EncodeToString(trustedKey.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(trustedKey.E)).Bytes())}}})
		case "/userinfo":
			switch mode.Load() {
			case 2:
				w.WriteHeader(503)
				return
			case 3:
				json.NewEncoder(w).Encode(map[string]string{"sub": "user"})
				return
			case 4:
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"sub": "user", "email": "new@example.com"})
		}
	}))
	defer provider.Close()
	issuer = provider.URL
	store := browserstore.NewMemory()
	v := NewBrowserVerifier(issuer, "aud", "email", "client", "secret", "https://auth", "", provider.Client())
	if err := v.ConfigureBrowserSessions(store, "", time.Hour, 24*time.Hour, DefaultBrowserScopes); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "pending"})
	now := time.Now().UTC()
	original := browserSession{AccessToken: "current-token", RefreshToken: "rotated-refresh", Result: api.AuthResult{Valid: true, Subject: "user", Email: "old@example.com"}, ExpiresAt: now.Add(24 * time.Hour), IdleExpiresAt: now.Add(time.Hour), LastActivityAt: now, TokenExpiresAt: now.Add(20 * time.Second)}
	seed := func(s browserSession) {
		t.Helper()
		if err := v.saveRecord(context.Background(), "session", "pending", s, s.IdleExpiresAt); err != nil {
			t.Fatal(err)
		}
	}
	mode.Store(1)
	seed(original)
	if _, token, ok, err := v.SessionWithError(request); err != nil || !ok || token != "current-token" {
		t.Fatalf("early outage rejected valid token: %v %v", ok, err)
	}
	expired := original
	expired.TokenExpiresAt = now.Add(-time.Second)
	seed(expired)
	if _, _, ok, err := v.SessionWithError(request); err == nil || ok {
		t.Fatal("expired token accepted during outage")
	}
	mode.Store(2)
	pending := original
	pending.IdentityPending = true
	pending.TokenExpiresAt = now.Add(time.Hour)
	seed(pending)
	if _, _, ok, err := v.SessionWithError(request); err == nil || ok {
		t.Fatal("pending identity outage not reported")
	}
	payload, _ := store.ReadBrowserRecord(context.Background(), v.recordKey("session", "pending"))
	var after browserSession
	if err := json.Unmarshal(payload, &after); err != nil {
		t.Fatal(err)
	}
	if !after.IdleExpiresAt.Equal(pending.IdleExpiresAt) || !after.LastActivityAt.Equal(pending.LastActivityAt) {
		t.Fatal("failed identity request extended idle session")
	}
	// The pending token expires during the outage. A later healthy request must
	// refresh using the rotated grant, rather than retry the dead token forever.
	pending.TokenExpiresAt = now.Add(-time.Second)
	seed(pending)
	mode.Store(0)
	before := refreshes.Load()
	result, token, ok, err := v.SessionWithError(request)
	if err != nil || !ok || token != "recovered-token" || result.Email != "new@example.com" || refreshes.Load() != before+1 {
		t.Fatalf("pending session stuck: %v %v", ok, err)
	}
	for _, permanent := range []int32{3, 4} {
		mode.Store(permanent)
		pending.TokenExpiresAt = time.Now().Add(time.Hour)
		seed(pending)
		if _, _, ok, err := v.SessionWithError(request); err != nil || ok {
			t.Fatalf("permanent identity failure must require login: %v %v", ok, err)
		}
		payload, _ := store.ReadBrowserRecord(context.Background(), v.recordKey("session", "pending"))
		if len(payload) != 0 {
			t.Fatal("permanent failure retained session")
		}
	}
	// A cryptographically invalid refreshed ID token is definitive, not a
	// retriable key-service outage that can strand the session for its lifetime.
	trusted, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	trustedKey = &trusted.PublicKey
	v.keys = map[string]any{"test-key": &trusted.PublicKey}
	v.keysLoadedAt = time.Now()
	mode.Store(0)
	pending.TokenExpiresAt = time.Now().Add(time.Hour)
	pending.PendingIDToken = signedJWT(t, wrong, issuer, "client", map[string]any{"sub": "user", "email": "new@example.com"})
	seed(pending)
	if _, _, ok, err := v.SessionWithError(request); ok || err != nil {
		t.Fatalf("invalid ID signature did not require login: %v %v", ok, err)
	}

}

func TestTokenExpiryCompatibilityFallbacks(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	idExpiry := now.Add(20 * time.Minute)
	payload, _ := json.Marshal(map[string]any{"exp": idExpiry.Unix()})
	idToken := "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".unused"
	for _, tc := range []struct {
		name  string
		token tokenResponse
		want  time.Time
	}{
		{"opaque-without-metadata", tokenResponse{AccessToken: "opaque"}, now.Add(time.Hour)},
		{"original-id-token", tokenResponse{AccessToken: "opaque", IDToken: idToken}, idExpiry},
		{"explicit-expiry", tokenResponse{AccessToken: "opaque", IDToken: idToken, ExpiresIn: 300}, now.Add(5 * time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := accessTokenExpiry(tc.token, now); !got.Equal(tc.want) {
				t.Fatalf("expiry=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestIdPCallsDoNotHoldPostgresConnections(t *testing.T) {
	dsn := os.Getenv("NANOFLARE_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("NANOFLARE_TEST_POSTGRES_URL is not configured")
	}
	db, err := database.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	stages := make(chan string, 3)
	release := make(chan struct{}, 3)
	var issuer string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			stages <- r.URL.Path
			<-release
		}
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]string{"authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "userinfo_endpoint": issuer + "/userinfo"})
		case "/token":
			json.NewEncoder(w).Encode(map[string]any{"access_token": "new", "refresh_token": "new-refresh", "expires_in": 3600, "id_token": signedJWT(t, private, issuer, "client", map[string]any{"sub": "user", "email": "user@example.com"})})
		case "/userinfo":
			json.NewEncoder(w).Encode(map[string]string{"sub": "user", "email": "user@example.com"})
		case "/keys":
			json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kid": "test-key", "kty": "RSA", "n": base64.RawURLEncoding.EncodeToString(private.PublicKey.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(private.PublicKey.E)).Bytes())}}})
		}
	}))
	defer provider.Close()
	issuer = provider.URL
	// Always release a blocked handler if an assertion fails.
	defer func() {
		for i := 0; i < 3; i++ {
			select {
			case release <- struct{}{}:
			default:
			}
		}
	}()
	v := NewBrowserVerifier(issuer, "aud", "email", "client", "secret", "https://auth", "", provider.Client())
	if err := v.ConfigureBrowserSessions(db, browserTestKey, time.Hour, 24*time.Hour, DefaultBrowserScopes); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	id, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	s := browserSession{AccessToken: "old", RefreshToken: "refresh", Result: api.AuthResult{Valid: true, Subject: "user", Email: "user@example.com"}, ExpiresAt: now.Add(24 * time.Hour), IdleExpiresAt: now.Add(time.Hour), LastActivityAt: now, TokenExpiresAt: now.Add(-time.Second)}
	if err := v.saveRecord(context.Background(), "session", id, s, s.IdleExpiresAt); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: id})
	done := make(chan error, 1)
	go func() {
		_, _, ok, err := v.SessionWithError(request)
		if err == nil && !ok {
			err = errIdentityRejected
		}
		done <- err
	}()
	for i := 0; i < 3; i++ {
		select {
		case stage := <-stages:
			if inUse := db.PoolStats().InUse; inUse != 0 {
				t.Errorf("%s holds %d database connections", stage, inUse)
			}
			// Taking the same record lock must also succeed while the IdP is blocked.
			checkCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			err := db.UpdateBrowserRecord(checkCtx, v.recordKey("session", id), func([]byte) ([]byte, time.Time, error) { return nil, time.Time{}, browserstore.ErrUnchanged })
			cancel()
			if err != nil {
				t.Errorf("IdP call held session advisory lock: %v", err)
			}
			release <- struct{}{}
		case err := <-done:
			t.Fatalf("refresh ended before all stages: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("provider stage timeout")
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// There is no signed proof bypass: deletion by another instance is observed
	// immediately by the next ordinary/gateway session lookup.
	if err := db.UpdateBrowserRecord(context.Background(), v.recordKey("session", id), func([]byte) ([]byte, time.Time, error) { return nil, time.Time{}, nil }); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := v.SessionWithError(request); ok || err != nil {
		t.Fatalf("revoked session accepted: %v %v", ok, err)
	}

	// Revoke while a refresh is in flight. The fenced commit must not insert a
	// new record in place of the deleted one or return its identity.
	if err := v.saveRecord(context.Background(), "session", id, s, s.IdleExpiresAt); err != nil {
		t.Fatal(err)
	}
	revoked := make(chan error, 1)
	go func() {
		_, _, ok, err := v.SessionWithError(request)
		if ok {
			t.Error("in-flight refresh resurrected revoked session")
		}
		revoked <- err
	}()
	select {
	case stage := <-stages:
		if stage != "/token" {
			t.Fatalf("unexpected stage %s", stage)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not start")
	}
	if err := db.UpdateBrowserRecord(context.Background(), v.recordKey("session", id), func([]byte) ([]byte, time.Time, error) { return nil, time.Time{}, nil }); err != nil {
		t.Fatal(err)
	}
	release <- struct{}{}
	select {
	case err := <-revoked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revoked refresh did not finish")
	}

}
