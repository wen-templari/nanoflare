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

type countedBrowserStore struct {
	browserstore.Store
	reads, writes atomic.Int32
}

func (s *countedBrowserStore) ReadBrowserRecord(ctx context.Context, key string) ([]byte, error) {
	s.reads.Add(1)
	return s.Store.ReadBrowserRecord(ctx, key)
}
func (s *countedBrowserStore) UpdateBrowserRecord(ctx context.Context, key string, fn func([]byte) ([]byte, time.Time, error)) error {
	return s.Store.UpdateBrowserRecord(ctx, key, func(data []byte) ([]byte, time.Time, error) {
		data, expiry, err := fn(data)
		if err == nil {
			s.writes.Add(1)
		}
		return data, expiry, err
	})
}
func TestBrowserRefreshSurvivesDisconnectAndIdentityOutage(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var store browserstore.Store = browserstore.NewMemory()
			if backend == "postgres" {
				dsn := os.Getenv("NANOFLARE_TEST_POSTGRES_URL")
				if dsn == "" {
					t.Skip("NANOFLARE_TEST_POSTGRES_URL is not configured")
				}
				db, err := database.Open(context.Background(), dsn)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				store = db
			}
			counted := &countedBrowserStore{Store: store}
			private, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatal(err)
			}
			browserCtx, disconnect := context.WithCancel(context.Background())
			defer disconnect()
			var refreshes atomic.Int32
			var identityOutage atomic.Bool
			identityOutage.Store(true)
			var issuer string
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/.well-known/openid-configuration":
					json.NewEncoder(w).Encode(map[string]string{"authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks", "userinfo_endpoint": issuer + "/userinfo"})
				case "/token":
					refreshes.Add(1)
					r.ParseForm()
					if r.Form.Get("refresh_token") != "original-refresh" {
						t.Error("lost or reused rotated refresh token")
					}
					disconnect()
					json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "refresh_token": "rotated-refresh", "expires_in": 3600, "id_token": signedJWT(t, private, issuer, "client", map[string]any{"sub": "user", "email": "new@example.com", "role": "updated"})})
				case "/userinfo":
					if identityOutage.Load() {
						w.WriteHeader(503)
						return
					}
					json.NewEncoder(w).Encode(map[string]string{"sub": "user", "email": "new@example.com"})
				case "/jwks":
					json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kid": "test-key", "kty": "RSA", "n": base64.RawURLEncoding.EncodeToString(private.PublicKey.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(private.PublicKey.E)).Bytes())}}})
				}
			}))
			defer provider.Close()
			issuer = provider.URL
			v := NewBrowserVerifier(issuer, "aud", "email", "client", "secret", "https://auth.example.com", "", provider.Client())
			if err := v.ConfigureBrowserSessions(counted, browserTestKey, DefaultBrowserIdleTTL, DefaultBrowserAbsoluteTTL, DefaultBrowserScopes); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			session := browserSession{AccessToken: "old-access", RefreshToken: "original-refresh", Result: api.AuthResult{Valid: true, Subject: "user", Email: "old@example.com"}, ExpiresAt: now.Add(24 * time.Hour), IdleExpiresAt: now.Add(time.Hour), LastActivityAt: now, TokenExpiresAt: now.Add(-time.Second)}
			if err := v.saveRecord(context.Background(), "session", "disconnect", session, session.ExpiresAt); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest("GET", "https://worker.example.com/save", nil)
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "disconnect"})
			if _, _, valid, err := v.SessionWithError(request.WithContext(browserCtx)); err == nil || valid {
				t.Fatal("identity outage must fail authentication temporarily")
			}
			if browserCtx.Err() == nil {
				t.Fatal("test did not disconnect browser")
			}
			payload, err := store.ReadBrowserRecord(context.Background(), v.recordKey("session", "disconnect"))
			if err != nil {
				t.Fatal(err)
			}
			plaintext, err := v.decodeRecord(v.recordKey("session", "disconnect"), payload)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(plaintext, &session); err != nil {
				t.Fatal(err)
			}
			if session.RefreshToken != "rotated-refresh" || !session.IdentityPending {
				t.Fatal("rotation was not persisted before identity failure")
			}
			identityOutage.Store(false)
			result, token, valid, err := v.SessionWithError(request)
			if err != nil || !valid || token != "new-access" || result.Email != "new@example.com" || result.Claims["role"] != "updated" || result.ExpiresAt == nil || !result.ExpiresAt.After(time.Now().Add(59*time.Minute)) {
				t.Fatalf("identity not updated: valid=%v result=%+v error=%v", valid, result, err)
			}
			if refreshes.Load() != 1 {
				t.Fatal("identity retry refreshed an already rotated token")
			}
			counted.writes.Store(0)
			for i := 0; i < 50; i++ {
				if _, _, valid, err := v.SessionWithError(request); err != nil || !valid {
					t.Fatal("healthy read failed", err)
				}
			}
			if counted.writes.Load() != 0 {
				t.Fatalf("asset requests wrote %d records", counted.writes.Load())
			}
			// The next activity sample should perform exactly one update.
			session.IdentityPending = false
			session.PendingIDToken = ""
			session.Result = result
			session.LastActivityAt = time.Now().Add(-2 * time.Minute)
			if err := v.saveRecord(context.Background(), "session", "disconnect", session, session.ExpiresAt); err != nil {
				t.Fatal(err)
			}
			counted.writes.Store(0)
			if _, _, valid, err := v.SessionWithError(request); err != nil || !valid || counted.writes.Load() != 1 {
				t.Fatalf("idle bump: valid=%v writes=%d error=%v", valid, counted.writes.Load(), err)
			}
		})
	}
}

func TestBrowserConfigurationPreservesMemoryOnlyLogin(t *testing.T) {
	memory := browserstore.NewMemory()
	v := NewBrowserVerifier("https://issuer", "aud", "email", "client", "", "https://auth", "", nil)
	if err := v.ConfigureBrowserSessions(memory, "", DefaultBrowserIdleTTL, DefaultBrowserAbsoluteTTL, DefaultBrowserScopes); err != nil {
		t.Fatal(err)
	}
	if v.codec != nil || v.scopes != "openid email profile" {
		t.Fatal("memory-only configuration changed login defaults")
	}
	if err := v.ConfigureBrowserSessions(persistentBrowserStore{memory}, "", DefaultBrowserIdleTTL, DefaultBrowserAbsoluteTTL, DefaultBrowserScopes); err == nil {
		t.Fatal("persistent storage accepted no encryption key")
	}
}

type persistentBrowserStore struct{ browserstore.Store }

func (persistentBrowserStore) Persistent() bool { return true }

func TestCorruptBrowserSessionsRequireLogin(t *testing.T) {
	for _, kind := range []string{"ciphertext", "json", "rotated-key"} {
		t.Run(kind, func(t *testing.T) {
			store := browserstore.NewMemory()
			v := NewBrowserVerifier("https://issuer", "aud", "email", "client", "", "https://auth", "", nil)
			key := browserTestKey
			if kind == "json" {
				key = ""
			}
			if err := v.ConfigureBrowserSessions(store, key, time.Hour, time.Hour, DefaultBrowserScopes); err != nil {
				t.Fatal(err)
			}
			id := "corrupt-session"
			lookup := v.recordKey("session", id)
			if kind == "rotated-key" {
				if err := v.saveRecord(context.Background(), "session", id, map[string]string{"token": "old"}, time.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
				if err := v.ConfigureBrowserSessions(store, "abcdef0123456789abcdef0123456789", time.Hour, time.Hour, DefaultBrowserScopes); err != nil {
					t.Fatal(err)
				}
			} else if err := store.UpdateBrowserRecord(context.Background(), lookup, func([]byte) ([]byte, time.Time, error) { return []byte("broken"), time.Now().Add(time.Hour), nil }); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest("GET", "/", nil)
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: id})
			if _, _, valid, err := v.SessionWithError(request); valid || err != nil {
				t.Fatalf("corruption returned valid=%v error=%v", valid, err)
			}
			data, err := store.ReadBrowserRecord(context.Background(), lookup)
			if err != nil || len(data) != 0 {
				t.Fatal("bad session was not deleted")
			}
		})
	}
}
