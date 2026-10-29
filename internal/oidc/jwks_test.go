package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
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
				v.keyForToken(context.Background(), fmt.Sprintf("forged-%d", i))
			}
			if fetches.Load() != 1 {
				t.Fatalf("JWKS requests=%d", fetches.Load())
			}
			// Failed signatures also share the same reload budget.
			for i := 0; i < 10; i++ {
				v.verifySignature(context.Background(), jwtHeader{Alg: "RS256", Kid: "cached-key"}, "forged", []byte("invalid"))
			}
			if fetches.Load() != 1 {
				t.Fatalf("signature failures fetched %d times", fetches.Load())
			}
			rotated.Store(true)
			v.forcedKeysAt = time.Now().Add(-forcedJWKSReloadInterval)
			_, err = v.keyForToken(context.Background(), "test-key")
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
