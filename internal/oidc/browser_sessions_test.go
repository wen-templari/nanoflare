package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	}); err == nil {
		t.Fatal("record binding was not checked")
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
