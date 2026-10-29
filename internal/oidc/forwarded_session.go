package oidc

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/clas/nanoflare/internal/api"
)

// ForwardAuth proofs avoid repeating session I/O in the gateway. They are bound
// to the cookie, identity headers and original URL, expire quickly, and are never
// passed to workers. Caller-supplied identity headers alone are never trusted.
type forwardedSessionProof struct {
	CookieHash string
	TokenHash  string
	Email      string
	URL        string
	ExpiresAt  time.Time
}

func proofHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
func (v *Verifier) proofMAC(data []byte) []byte {
	mac := hmac.New(sha256.New, v.proofKey)
	mac.Write(data)
	return mac.Sum(nil)
}
func (v *Verifier) BrowserSessionProof(r *http.Request, result api.AuthResult, token string) string {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || len(v.proofKey) == 0 {
		return ""
	}
	expiry := time.Now().Add(10 * time.Second)
	// SessionWithError caps result expiry at the token and browser deadlines.
	if result.ExpiresAt != nil && result.ExpiresAt.Before(expiry) {
		expiry = *result.ExpiresAt
	}
	proof := forwardedSessionProof{proofHash(cookie.Value), proofHash(token), result.Email, forwardedURL(r), expiry}
	data, err := json.Marshal(proof)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(v.proofMAC(data))
}
func (v *Verifier) ForwardedBrowserSession(r *http.Request) (api.AuthResult, string, bool) {
	parts := strings.Split(r.Header.Get("X-Nanoflare-Session-Proof"), ".")
	if len(parts) != 2 || len(v.proofKey) == 0 {
		return api.AuthResult{}, "", false
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return api.AuthResult{}, "", false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, v.proofMAC(data)) {
		return api.AuthResult{}, "", false
	}
	var proof forwardedSessionProof
	if json.Unmarshal(data, &proof) != nil || !proof.ExpiresAt.After(time.Now()) {
		return api.AuthResult{}, "", false
	}
	cookie, err := r.Cookie(sessionCookieName)
	token := r.Header.Get("X-Nanoflare-User-JWT")
	// Bind to the actual gateway path, never a caller-supplied forwarded URI.
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "http"
		if r.TLS != nil {
			scheme = "https"
		}
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	originalURL := scheme + "://" + host + r.URL.RequestURI()

	if err != nil || proof.CookieHash != proofHash(cookie.Value) || proof.TokenHash != proofHash(token) || proof.Email != r.Header.Get("X-Nanoflare-User-Email") || proof.URL != originalURL {
		return api.AuthResult{}, "", false
	}
	return api.AuthResult{Valid: true, Email: proof.Email, ExpiresAt: &proof.ExpiresAt}, token, true
}
