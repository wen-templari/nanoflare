package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/browserstore"
	"github.com/clas/nanoflare/internal/database"
)

const browserTestKey = "0123456789abcdef0123456789abcdef"

func TestBrowserSessionRefreshAcrossInstances(t *testing.T) {
	stores := map[string]func(t *testing.T) browserstore.Store{
		"memory": func(t *testing.T) browserstore.Store { return browserstore.NewMemory() },
		"postgres": func(t *testing.T) browserstore.Store {
			dsn := os.Getenv("NANOFLARE_TEST_POSTGRES_URL")
			if dsn == "" {
				t.Skip("NANOFLARE_TEST_POSTGRES_URL is not configured")
			}
			store, err := database.Open(context.Background(), dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Close() })
			return store
		},
	}
	for name, factory := range stores {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			var calls atomic.Int32
			var mode atomic.Int32
			var expectedRefresh = "refresh-original"
			var issuer string
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/.well-known/openid-configuration":
					json.NewEncoder(w).Encode(map[string]string{"authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "userinfo_endpoint": issuer + "/userinfo"})
				case "/userinfo":
					json.NewEncoder(w).Encode(map[string]any{"sub": "user", "email": "user@example.com"})
				case "/token":
					calls.Add(1)
					r.ParseForm()
					if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != expectedRefresh || r.Form.Get("client_secret") != "client-secret" {
						t.Error("incorrect refresh request")
						w.WriteHeader(400)
						return
					}
					switch mode.Load() {
					case 1:
						w.WriteHeader(503)
						return
					case 2:
						w.WriteHeader(400)
						fmt.Fprint(w, `{"error":"invalid_grant"}`)
						return
					}
					response := map[string]any{"access_token": "access-refreshed", "expires_in": 3600}
					if expectedRefresh == "refresh-original" {
						response["refresh_token"] = "refresh-rotated"
					}
					json.NewEncoder(w).Encode(response)
				default:
					http.NotFound(w, r)
				}
			}))
			defer provider.Close()
			issuer = provider.URL
			makeVerifier := func() *Verifier {
				sessionStore := store
				if name == "postgres" {
					sessionStore = factory(t)
				}
				v := NewBrowserVerifier(issuer, "audience", "email", "client", "client-secret", "https://auth.example.com", "", provider.Client())
				if err := v.ConfigureBrowserSessions(sessionStore, browserTestKey, time.Hour, 24*time.Hour, "openid email profile offline_access"); err != nil {
					t.Fatal(err)
				}
				return v
			}
			first, second := makeVerifier(), makeVerifier()
			id, err := randomToken()
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest("GET", "/", nil)
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: id})
			seed := func(tokenExpiry, idleExpiry, absoluteExpiry time.Time) {
				t.Helper()
				err := first.saveRecord(context.Background(), "session", id, browserSession{AccessToken: "access-original", RefreshToken: expectedRefresh, Result: api.AuthResult{Valid: true, Subject: "user", Email: "user@example.com", ExpiresAt: expiredIdentityTime(time.Now().Add(-time.Hour))}, TokenExpiresAt: tokenExpiry, IdleExpiresAt: idleExpiry, ExpiresAt: absoluteExpiry}, absoluteExpiry)
				if err != nil {
					t.Fatal(err)
				}
			}
			expireToken := func() { seed(time.Now().Add(-time.Minute), time.Now().Add(time.Hour), time.Now().Add(24*time.Hour)) }
			expireToken()
			var wg sync.WaitGroup
			for i := 0; i < 16; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					v := first
					if i%2 == 0 {
						v = second
					}
					result, token, ok, err := v.SessionWithError(request)
					if err != nil || !ok || token != "access-refreshed" || result.Email != "user@example.com" {
						t.Errorf("refresh: valid=%v error=%v", ok, err)
					}
				}(i)
			}
			wg.Wait()
			if calls.Load() != 1 {
				t.Fatalf("refresh calls=%d, want 1", calls.Load())
			}
			// Restarted verifier reads the shared encrypted record; force a second refresh
			// and verify that an omitted replacement preserves the rotated refresh token.
			expectedRefresh = "refresh-rotated"
			err = second.updateRecord(context.Background(), "session", id, func(data []byte) ([]byte, time.Time, error) {
				var session browserSession
				if err := json.Unmarshal(data, &session); err != nil {
					return nil, time.Time{}, err
				}
				if session.RefreshToken != expectedRefresh {
					t.Fatal("rotation was not persisted")
				}
				session.TokenExpiresAt = time.Now().Add(-time.Second)
				data, err := json.Marshal(session)
				return data, session.ExpiresAt, err
			})
			if err != nil {
				t.Fatal(err)
			}
			restarted := makeVerifier()
			if _, _, ok, err := restarted.SessionWithError(request); !ok || err != nil {
				t.Fatalf("restart: %v %v", ok, err)
			}
			mode.Store(1)
			expireToken()
			if _, _, ok, err := second.SessionWithError(request); ok || err == nil {
				t.Fatal("temporary outage should return an error")
			}
			mode.Store(0)
			if _, _, ok, err := second.SessionWithError(request); !ok || err != nil {
				t.Fatal("temporary failure destroyed session")
			}
			mode.Store(2)
			expireToken()
			if _, _, ok, err := second.SessionWithError(request); ok || err != nil {
				t.Fatal("invalid_grant should require reauthentication")
			}
			mode.Store(0)
			if _, _, ok, _ := second.SessionWithError(request); ok {
				t.Fatal("revoked session retained")
			}
			for _, limit := range []string{"idle", "absolute"} {
				idle, absolute := time.Now().Add(time.Hour), time.Now().Add(time.Hour)
				if limit == "idle" {
					idle = time.Now().Add(-time.Second)
				} else {
					absolute = time.Now().Add(-time.Second)
				}
				seed(time.Now().Add(time.Hour), idle, absolute)
				if _, _, ok, err := second.SessionWithError(request); ok || err != nil {
					t.Fatalf("%s limit ignored", limit)
				}
			}
		})
	}
}

func TestEncryptedBrowserRecordBinding(t *testing.T) {
	store := browserstore.NewMemory()
	v := NewBrowserVerifier("https://issuer", "aud", "email", "client", "", "https://auth", "", nil)
	if err := v.ConfigureBrowserSessions(store, browserTestKey, time.Hour, time.Hour, "openid"); err != nil {
		t.Fatal(err)
	}
	if err := v.saveRecord(context.Background(), "session", "raw-cookie-secret", map[string]string{"token": "raw-access-secret"}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// A wrapper records the opaque key and encrypted payload as a database would.
	capture := &captureBrowserStore{Store: store}
	v.store = capture
	if err := v.updateRecord(context.Background(), "session", "raw-cookie-secret", func(data []byte) ([]byte, time.Time, error) { return data, time.Now().Add(time.Hour), nil }); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(capture.key, "raw-cookie-secret") || strings.Contains(string(capture.payload), "raw-access-secret") {
		t.Fatal("plaintext credential persisted")
	}
	originalKey := capture.key
	originalPayload := append([]byte(nil), capture.payload...)
	if err := v.saveRecord(context.Background(), "session", "another-cookie", map[string]string{"token": "another-token"}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	otherKey := capture.key
	if err := store.UpdateBrowserRecord(context.Background(), otherKey, func([]byte) ([]byte, time.Time, error) { return originalPayload, time.Now().Add(time.Hour), nil }); err != nil {
		t.Fatal(err)
	}
	if err := v.updateRecord(context.Background(), "session", "another-cookie", func(data []byte) ([]byte, time.Time, error) {
		t.Fatal("swapped ciphertext was accepted")
		return nil, time.Time{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	payload, err := store.ReadBrowserRecord(context.Background(), otherKey)
	if err != nil || len(payload) != 0 {
		t.Fatal("invalid encrypted record was not deleted")
	}
	capture.key = originalKey

	// Transaction rollback preserves the previous payload on failure.
	sentinel := errors.New("rollback")
	if err := store.UpdateBrowserRecord(context.Background(), capture.key, func([]byte) ([]byte, time.Time, error) { return nil, time.Time{}, sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if err := v.updateRecord(context.Background(), "session", "raw-cookie-secret", func(data []byte) ([]byte, time.Time, error) {
		if len(data) == 0 {
			t.Fatal("rollback deleted record")
		}
		return nil, time.Time{}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

type captureBrowserStore struct {
	browserstore.Store
	key     string
	payload []byte
}

func (c *captureBrowserStore) UpdateBrowserRecord(ctx context.Context, key string, fn func([]byte) ([]byte, time.Time, error)) error {
	c.key = key
	return c.Store.UpdateBrowserRecord(ctx, key, func(data []byte) ([]byte, time.Time, error) {
		c.payload = append([]byte(nil), data...)
		return fn(data)
	})
}

func expiredIdentityTime(value time.Time) *time.Time { return &value }
func TestBrowserSessionWithoutRefreshToken(t *testing.T) {
	v := NewBrowserVerifier("https://issuer", "aud", "email", "client", "", "https://auth", "", nil)
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session"})
	for _, expired := range []bool{false, true} {
		expiry := time.Now().Add(10 * time.Second)
		if expired {
			expiry = time.Now().Add(-time.Second)
		}
		err := v.saveRecord(context.Background(), "session", "session", browserSession{AccessToken: "access", Result: api.AuthResult{Valid: true}, TokenExpiresAt: expiry, IdleExpiresAt: time.Now().Add(time.Hour), ExpiresAt: time.Now().Add(time.Hour)}, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		_, token, valid, err := v.SessionWithError(request)
		if err != nil || valid == expired {
			t.Fatalf("expired=%v valid=%v error=%v", expired, valid, err)
		}
		if expired && token != "" {
			t.Fatal("expired access token forwarded")
		}
	}
}

func TestPendingRefreshKeepsAcceptedIdentity(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		json.NewEncoder(w).Encode(map[string]string{"sub": "user", "email": "new@example.com"})
	}))
	defer provider.Close()
	v := NewBrowserVerifier("https://issuer", "aud", "email", "client", "", "https://auth", "", provider.Client())
	v.discovery = discoveryDocument{AuthorizationEndpoint: "https://issuer/auth", TokenEndpoint: "https://issuer/token", JWKSURI: "https://issuer/keys", UserInfoEndpoint: provider.URL}
	now := time.Now()
	session := browserSession{AccessToken: "pending", AcceptedAccessToken: "accepted", AcceptedTokenExpiresAt: now.Add(time.Minute), TokenExpiresAt: now.Add(time.Hour), IdentityPending: true, Result: api.AuthResult{Valid: true, Subject: "user", Email: "old@example.com"}, ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Hour), LastActivityAt: now}
	if err := v.saveRecord(context.Background(), "session", "pending", session, session.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "pending"})
	done := make(chan struct{})
	go func() { defer close(done); v.SessionWithError(request) }()
	defer func() { close(release); <-done }()
	<-entered
	for _, lookup := range []func(*http.Request) (api.AuthResult, string, bool, error){v.LookupSession, v.SessionWithError} {
		result, token, ok, err := lookup(request)
		if err != nil || !ok || token != "accepted" || result.Email != "old@example.com" {
			t.Fatalf("lost accepted identity: %v %q %v %v", result, token, ok, err)
		}
	}
	// Expired accepted credentials must never expose the pending token.
	session.AcceptedTokenExpiresAt = now.Add(-time.Second)
	if _, ok := acceptedSession(session, time.Now()); ok {
		t.Fatal("expired accepted token forwarded")
	}
}

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

func TestSigningKeyRotationAndOutagePreservePendingSession(t *testing.T) {
	oldKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, rotation := range []string{"new-kid", "same-kid", "cache-ttl"} {
		t.Run(rotation, func(t *testing.T) {
			var issuer string
			var outage atomic.Bool
			outage.Store(true)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/.well-known/openid-configuration":
					json.NewEncoder(w).Encode(map[string]string{"authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "userinfo_endpoint": issuer + "/userinfo"})
				case "/keys":
					if outage.Load() {
						http.Error(w, "invalid token from temporary proxy", 503)
						return
					}
					json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kid": "test-key", "kty": "RSA", "n": base64.RawURLEncoding.EncodeToString(newKey.PublicKey.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(newKey.PublicKey.E)).Bytes())}}})
				case "/userinfo":
					json.NewEncoder(w).Encode(map[string]string{"sub": "user", "email": "new@example.com"})
				default:
					t.Errorf("unexpected provider request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer provider.Close()
			issuer = provider.URL
			store := browserstore.NewMemory()
			v := NewBrowserVerifier(issuer, "aud", "email", "client", "", "https://auth", "", provider.Client())
			if err := v.ConfigureBrowserSessions(store, "", time.Hour, 24*time.Hour, DefaultBrowserScopes); err != nil {
				t.Fatal(err)
			}
			oldKid := "test-key"
			if rotation == "new-kid" {
				oldKid = "old-kid"
			}
			v.keys = map[string]any{oldKid: &oldKey.PublicKey}
			v.keysLoadedAt = time.Now()
			if rotation == "cache-ttl" {
				v.keysLoadedAt = time.Now().Add(-10 * time.Minute)
			}
			now := time.Now().UTC()
			s := browserSession{AccessToken: "access", RefreshToken: "rotated", Result: api.AuthResult{Valid: true, Subject: "user", Email: "old@example.com"}, ExpiresAt: now.Add(24 * time.Hour), IdleExpiresAt: now.Add(time.Hour), LastActivityAt: now, TokenExpiresAt: now.Add(time.Hour), IdentityPending: true, PendingIDToken: signedJWT(t, newKey, issuer, "client", map[string]any{"sub": "user", "email": "new@example.com"})}
			if err := v.saveRecord(context.Background(), "session", "rotation", s, s.IdleExpiresAt); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest("GET", "/", nil)
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "rotation"})
			if _, _, ok, err := v.SessionWithError(request); ok || err == nil {
				t.Fatal("JWKS outage must be temporary")
			}
			payload, err := store.ReadBrowserRecord(context.Background(), v.recordKey("session", "rotation"))
			if err != nil || len(payload) == 0 {
				t.Fatal("JWKS outage deleted pending session")
			}
			outage.Store(false)
			v.forcedKeysAt = time.Now().Add(-forcedJWKSReloadInterval)
			if result, _, ok, err := v.SessionWithError(request); err != nil || !ok || result.Email != "new@example.com" {
				t.Fatalf("rotation failed without restart: valid=%v error=%v", ok, err)
			}
		})
	}
}

func TestRefreshWithoutIDTokenPreservesLoginClaims(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"sub": "user", "email": "new@example.com"})
	}))
	defer provider.Close()
	v := NewBrowserVerifier("https://issuer", "aud", "email", "client", "", "https://auth", "", provider.Client())
	v.discovery = discoveryDocument{AuthorizationEndpoint: "https://issuer/auth", TokenEndpoint: "https://issuer/token", JWKSURI: "https://issuer/keys", UserInfoEndpoint: provider.URL}
	oldClaims := map[string]any{"sub": "user", "aud": "client", "groups": []any{"editors"}, "custom": "value", "email": "old@example.com"}
	session := browserSession{Result: api.AuthResult{Valid: true, Subject: "user", Email: "old@example.com", Claims: oldClaims}, AccessToken: "access", TokenExpiresAt: time.Now().Add(time.Hour)}
	result, permanent, err := v.refreshIdentity(context.Background(), session)
	if err != nil || permanent || result.Claims["aud"] != "client" || result.Claims["custom"] != "value" || len(result.Claims["groups"].([]any)) != 1 || result.Claims["email"] != "new@example.com" {
		t.Fatalf("claims lost: %+v error=%v", result, err)
	}
	if oldClaims["email"] != "old@example.com" {
		t.Fatal("refresh mutated login claims in place")
	}
}

func TestLoginRejectsMissingSubjectBeforeCreatingSession(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"email": "user@example.com"})
	}))
	defer provider.Close()
	v := NewBrowserVerifier("https://issuer", "aud", "email", "client", "", "https://auth", "", provider.Client())
	v.discovery = discoveryDocument{AuthorizationEndpoint: "https://issuer/auth", TokenEndpoint: "https://issuer/token", JWKSURI: "https://issuer/keys", UserInfoEndpoint: provider.URL}
	if _, _, err := v.callbackIdentity(context.Background(), tokenResponse{AccessToken: "access"}); err == nil {
		t.Fatal("login accepted identity without a subject")
	}
}

func TestExpiredRefreshResultDoesNotRotateInLoop(t *testing.T) {
	var issuer string
	var refreshes atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]string{"authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "userinfo_endpoint": issuer + "/userinfo"})
		case "/token":
			refreshes.Add(1)
			payload, _ := json.Marshal(map[string]any{"exp": time.Now().Add(-time.Minute).Unix()})
			json.NewEncoder(w).Encode(map[string]any{"access_token": "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".c2ln", "refresh_token": "rotated", "expires_in": 3600})
		default:
			t.Error("expired token reached identity lookup")
		}
	}))
	defer provider.Close()
	issuer = provider.URL
	v := NewBrowserVerifier(issuer, "aud", "email", "client", "", "https://auth", "", provider.Client())
	now := time.Now().UTC()
	s := browserSession{AccessToken: "old", RefreshToken: "refresh", Result: api.AuthResult{Valid: true, Subject: "user"}, ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Hour), LastActivityAt: now, TokenExpiresAt: now.Add(-time.Minute)}
	if err := v.saveRecord(context.Background(), "session", "expired", s, s.IdleExpiresAt); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "expired"})
	started := time.Now()
	if _, _, ok, err := v.SessionWithError(r); err != nil || ok {
		t.Fatalf("expired result did not require login: %v %v", ok, err)
	}
	if refreshes.Load() != 1 || time.Since(started) > time.Second {
		t.Fatalf("refresh loop: calls=%d duration=%v", refreshes.Load(), time.Since(started))
	}
}

func TestPendingIDTokenExpiryRecovery(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"recover", "expired-replacement", "no-refresh-grant", "invalid-issuer", "revoked-grant", "refresh-outage"} {
		t.Run(mode, func(t *testing.T) {
			var refreshes atomic.Int32
			var outage atomic.Bool
			outage.Store(true)
			now := time.Now().UTC()
			expiredClaims := map[string]any{"sub": "user", "exp": now.Add(-time.Minute).Unix()}
			pendingIssuer := "https://issuer"
			if mode == "invalid-issuer" {
				pendingIssuer = "https://other-issuer"
			}
			pendingID := signedJWT(t, key, pendingIssuer, "client", expiredClaims)
			replacementID := signedJWT(t, key, "https://issuer", "client", map[string]any{"sub": "user"})
			if mode == "expired-replacement" {
				replacementID = signedJWT(t, key, "https://issuer", "client", expiredClaims)
			}
			var v *Verifier
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/token":
					refreshes.Add(1)
					if err := r.ParseForm(); err != nil || r.Form.Get("refresh_token") != "saved-refresh" {
						t.Error("recovery did not use the persisted refresh token")
					}
					if mode == "revoked-grant" {
						w.WriteHeader(http.StatusBadRequest)
						fmt.Fprint(w, `{"error":"invalid_grant"}`)
						return
					}
					if mode == "refresh-outage" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					json.NewEncoder(w).Encode(tokenResponse{AccessToken: "fresh-access", RefreshToken: "fresh-refresh", IDToken: replacementID, ExpiresIn: 3600})
				case "/userinfo":
					if outage.Load() {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					if r.Header.Get("Authorization") == "Bearer fresh-access" {
						stored, found, err := v.readSession(r.Context(), "pending-expiry")
						if err != nil || !found || stored.RefreshToken != "fresh-refresh" || !stored.IdentityPending {
							t.Error("recovered credentials were not durable before identity verification")
						}
					}
					json.NewEncoder(w).Encode(map[string]string{"sub": "user", "email": "user@example.com"})
				default:
					t.Errorf("unexpected provider request: %s", r.URL.Path)
				}
			}))
			defer provider.Close()
			v = NewBrowserVerifier("https://issuer", "aud", "email", "client", "", "https://auth", "", provider.Client())
			v.discovery = discoveryDocument{AuthorizationEndpoint: provider.URL + "/authorize", TokenEndpoint: provider.URL + "/token", JWKSURI: provider.URL + "/keys", UserInfoEndpoint: provider.URL + "/userinfo"}
			v.keys = map[string]any{"test-key": &key.PublicKey}
			v.keysLoadedAt = now
			s := browserSession{AccessToken: "pending-access", RefreshToken: "saved-refresh", IdentityPending: true, PendingIDToken: pendingID, Result: api.AuthResult{Valid: true, Subject: "user", Email: "user@example.com"}, TokenExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(24 * time.Hour), LastActivityAt: now}
			if mode == "no-refresh-grant" {
				s.RefreshToken = ""
			}
			if err := v.saveRecord(context.Background(), "session", "pending-expiry", s, s.IdleExpiresAt); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "/", nil)
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "pending-expiry"})
			if _, _, ok, err := v.SessionWithError(r); ok || err == nil {
				t.Fatal("initial identity outage should be temporary")
			}
			outage.Store(false)
			result, token, ok, err := v.SessionWithError(r)
			stored, found, readErr := v.readSession(context.Background(), "pending-expiry")
			if readErr != nil {
				t.Fatal(readErr)
			}
			switch mode {
			case "recover":
				if err != nil || !ok || token != "fresh-access" || result.Subject != "user" || !found || stored.IdentityPending || stored.RefreshToken != "fresh-refresh" {
					t.Fatalf("recovery failed: ok=%v token=%q found=%v err=%v", ok, token, found, err)
				}
			case "expired-replacement", "refresh-outage":
				if ok || err == nil || !found || !stored.IdentityPending || !stored.IdleExpiresAt.Equal(s.IdleExpiresAt) || !stored.LastActivityAt.Equal(s.LastActivityAt) {
					t.Fatalf("temporary recovery failure destroyed or renewed session: ok=%v found=%v err=%v", ok, found, err)
				}
				if mode == "expired-replacement" && stored.RefreshToken != "fresh-refresh" {
					t.Fatal("replacement failure lost rotated refresh token")
				}
			default:
				if ok || err != nil || found {
					t.Fatalf("unrecoverable credentials did not require login: ok=%v found=%v err=%v", ok, found, err)
				}
			}
			wantRefreshes := int32(1)
			if mode == "invalid-issuer" || mode == "no-refresh-grant" {
				wantRefreshes = 0
			}
			if refreshes.Load() != wantRefreshes {
				t.Fatalf("refreshes=%d, want %d", refreshes.Load(), wantRefreshes)
			}
		})
	}
}

type boundedLookupStore struct {
	browserstore.Store
	reads, updates atomic.Int32
	slow           bool
}

func (s *boundedLookupStore) ReadBrowserRecord(ctx context.Context, key string) ([]byte, error) {
	s.reads.Add(1)
	if s.slow {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.Store.ReadBrowserRecord(ctx, key)
}
func (s *boundedLookupStore) UpdateBrowserRecord(ctx context.Context, key string, fn func([]byte) ([]byte, time.Time, error)) error {
	s.updates.Add(1)
	return s.Store.UpdateBrowserRecord(ctx, key, fn)
}
func TestOptionalWorkerLookupIsBoundedAndNeverRefreshes(t *testing.T) {
	store := &boundedLookupStore{Store: browserstore.NewMemory()}
	v := NewBrowserVerifier("https://unreachable-issuer", "aud", "email", "client", "", "https://auth", "", nil)
	v.store = store
	now := time.Now().UTC()
	session := browserSession{AccessToken: "access", RefreshToken: "refresh", Result: api.AuthResult{Valid: true, Subject: "user"}, ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Hour), LastActivityAt: now, TokenExpiresAt: now.Add(-time.Second), LeaseOwner: "other-replica", LeaseUntil: now.Add(30 * time.Second)}
	r := httptest.NewRequest("GET", "/public", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "optional"})
	for _, state := range []string{"expired", "pending", "leased"} {
		session.TokenExpiresAt = now.Add(time.Hour)
		session.IdentityPending = false
		if state == "expired" {
			session.TokenExpiresAt = now.Add(-time.Second)
		}
		if state == "pending" {
			session.IdentityPending = true
		}
		if err := v.saveRecord(context.Background(), "session", "optional", session, session.IdleExpiresAt); err != nil {
			t.Fatal(err)
		}
		store.updates.Store(0)
		started := time.Now()
		_, _, ok, _ := v.LookupSession(r)
		if ok != (state == "leased") || store.updates.Load() != 0 || time.Since(started) > time.Second {
			t.Fatalf("worker %s path performed full auth", state)
		}
	}
	store.slow = true
	started := time.Now()
	if _, _, ok, _ := v.LookupSession(r); ok {
		t.Fatal("slow unavailable store returned identity")
	}
	if elapsed := time.Since(started); elapsed < 75*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Fatalf("lookup exceeded bounded latency: %v", elapsed)
	}
	if store.updates.Load() != 0 {
		t.Fatal("worker lookup wrote records")
	}
}

func TestJWKSRefreshWaitRespectsCancellation(t *testing.T) {
	v := NewVerifier("https://issuer", "aud", "email", nil)
	v.keysMu.Lock()
	defer v.keysMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := v.loadKeys(ctx, true); err == nil {
		t.Fatal("blocked JWKS refresh ignored cancellation")
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("waiting for a peer's key refresh exceeded deadline")
	}
}

func TestRefreshCapturesAcceptedCredentialsFromOlderRecord(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": "rotated", "expires_in": 3600})
	}))
	defer provider.Close()
	v := NewBrowserVerifier("https://issuer", "aud", "email", "client", "", "https://auth", "", provider.Client())
	v.discovery = discoveryDocument{AuthorizationEndpoint: "auth", TokenEndpoint: provider.URL, JWKSURI: "keys", UserInfoEndpoint: "userinfo"}
	now := time.Now()
	session := browserSession{AccessToken: "accepted-before-upgrade", RefreshToken: "refresh", TokenExpiresAt: now.Add(time.Minute), ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Hour), Result: api.AuthResult{Valid: true, Email: "user@example.com"}}
	updated, permanent, err := v.runLeasedOperation(context.Background(), session, true, now)
	if err != nil || permanent || !updated.IdentityPending {
		t.Fatalf("refresh failed: %v %v", permanent, err)
	}
	accepted, ok := acceptedSession(updated, time.Now())
	if !ok || accepted.AccessToken != session.AccessToken || !accepted.TokenExpiresAt.Equal(session.TokenExpiresAt) {
		t.Fatal("refresh dropped pre-upgrade accepted credentials")
	}
}
