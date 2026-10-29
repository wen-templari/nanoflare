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
	"sync/atomic"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestForcedJWKSReloadIsRateLimited(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, outage := range []bool{false, true} {
		t.Run(fmt.Sprint(outage), func(t *testing.T) {
			var fetches atomic.Int32
			var rotated atomic.Bool
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fetches.Add(1)
				if outage {
					w.WriteHeader(503)
					return
				}
				kid := "cached-key"
				if rotated.Load() {
					kid = "test-key"
				}
				json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kid": kid, "kty": "RSA", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
			}))
			defer provider.Close()
			v := NewVerifier("https://issuer", "aud", "email", provider.Client())
			v.discovery = discoveryDocument{AuthorizationEndpoint: "auth", TokenEndpoint: "token", JWKSURI: provider.URL, UserInfoEndpoint: "userinfo"}
			v.keys = map[string]any{"old": &key.PublicKey}
			v.keysLoadedAt = time.Now()
			for i := 0; i < 10; i++ {
				v.verifySignature(context.Background(), jwtHeader{Alg: "RS256", Kid: fmt.Sprintf("forged-%d", i)}, "forged", []byte("invalid"), time.Time{})
			}
			if fetches.Load() != 1 {
				t.Fatalf("JWKS requests=%d", fetches.Load())
			}
			// Failed signatures also share the same reload budget.
			for i := 0; i < 10; i++ {
				v.verifySignature(context.Background(), jwtHeader{Alg: "RS256", Kid: "cached-key"}, "forged", []byte("invalid"), time.Time{})
			}
			if fetches.Load() != 1 {
				t.Fatalf("signature failures fetched %d times", fetches.Load())
			}
			rotated.Store(true)
			v.forcedKeysAt = time.Now().Add(-forcedJWKSReloadInterval)
			_, err = v.parseAndValidate(context.Background(), signedJWT(t, key, "https://issuer", "aud", map[string]any{"sub": "user"}))
			if !outage && err != nil {
				t.Fatalf("rotation after cooldown failed: %v", err)
			}
			if outage && err == nil {
				t.Fatal("outage became invalid credentials")
			}
			if fetches.Load() != 2 {
				t.Fatalf("rotation requests=%d", fetches.Load())
			}
		})
	}
}

func TestUnknownSigningKeyHTTPStatus(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, outage := range []bool{false, true} {
		t.Run(fmt.Sprint(outage), func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if outage {
					w.WriteHeader(503)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"keys": []any{}})
			}))
			defer provider.Close()
			v := NewVerifier("https://issuer", "aud", "email", provider.Client())
			v.discovery = discoveryDocument{AuthorizationEndpoint: "auth", TokenEndpoint: "token", JWKSURI: provider.URL, UserInfoEndpoint: "userinfo"}
			server := api.NewServerWithAuth(nanoflare.NewService(nanoflare.NewStore(), nil), nil, "", v)
			request := httptest.NewRequest("GET", "/internal/auth/verify", nil)
			request.Header.Set("Authorization", "Bearer "+signedJWT(t, key, "https://issuer", "aud", map[string]any{"sub": "user"}))
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			want := http.StatusUnauthorized
			if outage {
				want = http.StatusBadGateway
			}
			if recorder.Code != want {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestKeyRotationDuringCooldownPreservesBrowserSession(t *testing.T) {
	oldKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, sameID := range []bool{false, true} {
		for _, forcedLoad := range []bool{false, true} {
			t.Run(fmt.Sprintf("same-ID=%v/forced-load=%v", sameID, forcedLoad), func(t *testing.T) {
				var fetches atomic.Int32
				var rotated atomic.Bool
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/userinfo" {
						json.NewEncoder(w).Encode(map[string]string{"sub": "user", "email": "user@example.com"})
						return
					}
					fetches.Add(1)
					key, kid := oldKey, "old-key"
					if sameID {
						kid = "test-key"
					}
					if rotated.Load() {
						key, kid = newKey, "test-key"
					}
					json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kid": kid, "kty": "RSA", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
				}))
				defer provider.Close()
				v := NewBrowserVerifier("https://issuer", "aud", "email", "client", "", "https://auth", "", provider.Client())
				v.discovery = discoveryDocument{AuthorizationEndpoint: "auth", TokenEndpoint: "token", JWKSURI: provider.URL + "/keys", UserInfoEndpoint: provider.URL + "/userinfo"}
				if _, err := v.loadKeys(context.Background(), forcedLoad); err != nil {
					t.Fatal(err)
				}
				// Advance the test clock by aging the cache, without waiting for the cooldown.
				loadedAt := time.Now().Add(-5 * time.Second)
				v.keysFetchedAt, v.keysLoadedAt, v.forcedKeysAt = loadedAt, loadedAt, loadedAt
				rotated.Store(true)
				now := time.Now()
				token := signedJWT(t, newKey, "https://issuer", "client", map[string]any{"sub": "user", "iat": now.Unix()})
				session := browserSession{AccessToken: "rotated-access", TokenExpiresAt: now.Add(time.Hour), IdentityPending: true, PendingIDToken: token, Result: api.AuthResult{Valid: true, Subject: "user", Email: "user@example.com"}, ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Hour), LastActivityAt: now}
				if err := v.saveRecord(context.Background(), "session", "cooldown", session, session.ExpiresAt); err != nil {
					t.Fatal(err)
				}
				request := httptest.NewRequest("GET", "/", nil)
				request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "cooldown"})
				if _, _, ok, err := v.SessionWithError(request); ok || err == nil {
					t.Fatalf("rotation must be temporary: ok=%v err=%v", ok, err)
				}
				if _, found, err := v.readSession(context.Background(), "cooldown"); err != nil || !found {
					t.Fatalf("cooldown deleted session: %v", err)
				}
				server := api.NewServerWithAuth(nanoflare.NewService(nanoflare.NewStore(), nil), nil, "", v)
				bearer := httptest.NewRequest("GET", "/internal/auth/verify", nil)
				bearer.Header.Set("Authorization", "Bearer "+signedJWT(t, newKey, "https://issuer", "aud", map[string]any{"sub": "user", "iat": now.Unix()}))
				recorder := httptest.NewRecorder()
				server.ServeHTTP(recorder, bearer)
				if recorder.Code != http.StatusBadGateway {
					t.Fatalf("bearer status=%d body=%s", recorder.Code, recorder.Body.String())
				}
				if fetches.Load() != 1 {
					t.Fatalf("cooldown fetched %d times", fetches.Load())
				}
				v.forcedKeysAt = time.Now().Add(-forcedJWKSReloadInterval)
				if _, _, ok, err := v.SessionWithError(request); err != nil || !ok {
					t.Fatalf("session did not recover after cooldown: %v", err)
				}
				if fetches.Load() != 2 {
					t.Fatalf("rotation fetched %d times", fetches.Load())
				}
			})
		}
	}
}

func TestCachedSigningFailureUsesConservativeIssuedAt(t *testing.T) {
	oldKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	foreignKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fetchedAt := time.Now().Add(-5 * time.Second).Truncate(time.Second).Add(500 * time.Millisecond)
	for _, sameID := range []bool{false, true} {
		for _, tc := range []struct {
			name      string
			iat       any
			temporary bool
		}{
			{"older", fetchedAt.Add(-time.Minute).Unix(), false},
			{"newer", time.Now().Unix(), true},
			{"same-second", fetchedAt.Unix(), true},
			{"missing", nil, true},
			{"malformed", "invalid", true},
		} {
			t.Run(fmt.Sprintf("same-ID=%v/%s", sameID, tc.name), func(t *testing.T) {
				v := NewVerifier("https://issuer", "aud", "email", nil)
				kid := "old-key"
				if sameID {
					kid = "test-key"
				}
				v.keys = map[string]any{kid: &oldKey.PublicKey}
				v.keysLoadedAt, v.keysFetchedAt, v.forcedKeysAt = fetchedAt, fetchedAt, fetchedAt
				token := signedJWT(t, foreignKey, "https://issuer", "aud", map[string]any{"sub": "user", "iat": tc.iat})
				_, err := v.parseAndValidate(context.Background(), token)
				if tc.temporary {
					if !errors.Is(err, errSigningKeysStale) || errors.Is(err, errInvalidIdentityToken) {
						t.Fatalf("uncertain keys became permanent: %v", err)
					}
				} else if !errors.Is(err, errInvalidIdentityToken) {
					t.Fatalf("old token did not get permanent rejection: %v", err)
				}
				server := api.NewServerWithAuth(nanoflare.NewService(nanoflare.NewStore(), nil), nil, "", v)
				request := httptest.NewRequest("GET", "/internal/auth/verify", nil)
				request.Header.Set("Authorization", "Bearer "+token)
				recorder := httptest.NewRecorder()
				server.ServeHTTP(recorder, request)
				want := http.StatusUnauthorized
				if tc.temporary {
					want = http.StatusBadGateway
				}
				if recorder.Code != want {
					t.Fatalf("bearer status=%d, want %d: %s", recorder.Code, want, recorder.Body.String())
				}
			})
		}
	}
}
