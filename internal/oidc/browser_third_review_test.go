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
	"sync/atomic"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/browserstore"
)

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
		_, _, ok := v.LookupSession(r)
		if ok != (state == "leased") || store.updates.Load() != 0 || time.Since(started) > time.Second {
			t.Fatalf("worker %s path performed full auth", state)
		}
	}
	store.slow = true
	started := time.Now()
	if _, _, ok := v.LookupSession(r); ok {
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
