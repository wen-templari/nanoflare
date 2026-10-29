package oidc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/browserstore"
	"github.com/clas/nanoflare/internal/nanoflare"
)

// ConfigureBrowserSessions must be called before serving requests. A persistent
// store requires a stable encryption key shared by every replica.
func (v *Verifier) ConfigureBrowserSessions(store browserstore.Store, key string, idle, absolute time.Duration, scopes string) error {
	if idle <= 0 || absolute <= 0 {
		return errors.New("OIDC session limits must be positive")
	}
	if store == nil {
		return errors.New("OIDC browser store is required")
	}
	codec, err := nanoflare.NewSecretCodec(key)
	if err != nil {
		return err
	}
	if !strings.Contains(" "+scopes+" ", " openid ") {
		return errors.New("OIDC scopes must include openid")
	}
	v.store = store
	v.codec = codec
	v.idleTTL = idle
	v.absoluteTTL = absolute
	v.scopes = scopes
	return nil
}

type encryptedRecord struct {
	Key  string
	Data json.RawMessage
}

func (v *Verifier) updateRecord(ctx context.Context, kind, id string, fn func([]byte) ([]byte, time.Time, error)) error {
	hash := sha256.Sum256([]byte(v.issuer + "\x00" + v.clientID + "\x00" + v.redirectURL + "\x00" + kind + "\x00" + id))
	key := hex.EncodeToString(hash[:])
	return v.store.UpdateBrowserRecord(ctx, key, func(payload []byte) ([]byte, time.Time, error) {
		if len(payload) > 0 && v.codec != nil {
			if len(payload) < 12 {
				return nil, time.Time{}, errors.New("invalid OIDC browser record")
			}
			plaintext, err := v.codec.Decrypt(payload[:12], payload[12:])
			if err != nil {
				return nil, time.Time{}, errors.New("cannot decrypt OIDC browser record")
			}
			var envelope encryptedRecord
			if err := json.Unmarshal([]byte(plaintext), &envelope); err != nil || envelope.Key != key {
				return nil, time.Time{}, errors.New("invalid OIDC browser record binding")
			}
			payload = envelope.Data
		}
		data, expiry, err := fn(payload)
		if err != nil || data == nil || v.codec == nil {
			return data, expiry, err
		}
		plaintext, err := json.Marshal(encryptedRecord{key, data})
		if err != nil {
			return nil, time.Time{}, err
		}
		nonce, ciphertext, err := v.codec.Encrypt(string(plaintext))
		return append(nonce, ciphertext...), expiry, err
	})
}
func (v *Verifier) saveRecord(ctx context.Context, kind, id string, value any, expiry time.Time) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return v.updateRecord(ctx, kind, id, func([]byte) ([]byte, time.Time, error) { return data, expiry, nil })
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func accessTokenExpiry(token tokenResponse, now time.Time) time.Time {
	var expiry time.Time
	if token.ExpiresIn > 0 {
		expiry = now.Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	// The token endpoint is trusted; using exp here only tightens the lifetime.
	if _, _, _, claims, err := parseJWT(token.AccessToken); err == nil {
		if exp, ok := numericDate(claims["exp"]); ok && (expiry.IsZero() || exp.Before(expiry)) {
			expiry = exp
		}
	}
	if expiry.IsZero() {
		return now
	}
	return expiry
}

func (v *Verifier) SessionWithError(r *http.Request) (api.AuthResult, string, bool, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return api.AuthResult{}, "", false, nil
	}
	var result api.AuthResult
	var access string
	var valid bool
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	err = v.updateRecord(ctx, "session", cookie.Value, func(data []byte) ([]byte, time.Time, error) {
		if len(data) == 0 {
			return nil, time.Time{}, nil
		}
		var session browserSession
		if err := json.Unmarshal(data, &session); err != nil {
			return nil, time.Time{}, err
		}
		now := time.Now().UTC()
		if !session.ExpiresAt.After(now) || !session.IdleExpiresAt.After(now) {
			return nil, time.Time{}, nil
		}
		if !session.TokenExpiresAt.After(now.Add(30 * time.Second)) {
			if session.RefreshToken == "" {
				if !session.TokenExpiresAt.After(now) {
					return nil, time.Time{}, nil
				}
			} else {
				token, rejected, err := v.refresh(ctx, session.RefreshToken)
				if rejected {
					return nil, time.Time{}, nil
				}
				if err != nil {
					return nil, time.Time{}, err
				}
				// Preserve the rotated credential before any subsequent network operation.
				// Refresh responses need not include a new ID token or refresh token.
				session.AccessToken = token.AccessToken
				if token.RefreshToken != "" {
					session.RefreshToken = token.RefreshToken
				}
				session.TokenExpiresAt = accessTokenExpiry(token, now)
				if !session.TokenExpiresAt.After(time.Now()) {
					return nil, time.Time{}, errors.New("OIDC refresh returned no usable access-token lifetime")
				}
			}
		}
		if !session.ExpiresAt.After(time.Now()) {
			return nil, time.Time{}, nil
		}
		session.IdleExpiresAt = time.Now().UTC().Add(v.idleTTL)
		result = session.Result
		access = session.AccessToken
		valid = true
		encoded, err := json.Marshal(session)
		return encoded, minTime(session.ExpiresAt, session.IdleExpiresAt), err
	})
	if err != nil {
		return api.AuthResult{}, "", false, errors.New("OIDC browser authentication temporarily unavailable")
	}
	return result, access, valid, nil
}

func (v *Verifier) refresh(ctx context.Context, refreshToken string) (tokenResponse, bool, error) {
	discovery, err := v.discoveryForContext(ctx)
	if err != nil {
		return tokenResponse{}, false, err
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}, "client_id": {v.clientID}}
	if v.clientSecret != "" {
		form.Set("client_secret", v.clientSecret)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, discovery.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, false, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := v.client.Do(request)
	if err != nil {
		return tokenResponse{}, false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(response.Body).Decode(&failure)
		return tokenResponse{}, response.StatusCode == http.StatusBadRequest && failure.Error == "invalid_grant", errors.New("OIDC refresh failed")
	}
	var token tokenResponse
	if err := json.NewDecoder(response.Body).Decode(&token); err != nil {
		return tokenResponse{}, false, err
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return tokenResponse{}, false, errors.New("OIDC refresh returned empty access token")
	}
	return token, false, nil
}
