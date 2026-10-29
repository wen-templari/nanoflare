package oidc

import (
	"context"
	"crypto/rand"
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
	var codec *nanoflare.SecretCodec
	if store.Persistent() || key != "" {
		var err error
		codec, err = nanoflare.NewSecretCodec(key)
		if err != nil {
			return err
		}
	}
	if !strings.Contains(" "+scopes+" ", " openid ") {
		return errors.New("OIDC scopes must include openid")
	}
	if codec != nil {
		v.proofKey = codec.DeriveKey("nanoflare/browser-forwardauth/v1\x00" + v.issuer + "\x00" + v.clientID + "\x00" + v.redirectURL)
	} else {
		v.proofKey = make([]byte, 32)
		if _, err := rand.Read(v.proofKey); err != nil {
			return err
		}
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

var errInvalidBrowserRecord = errors.New("invalid OIDC browser record")

func (v *Verifier) recordKey(kind, id string) string {
	hash := sha256.Sum256([]byte(v.issuer + "\x00" + v.clientID + "\x00" + v.redirectURL + "\x00" + kind + "\x00" + id))
	return hex.EncodeToString(hash[:])
}
func (v *Verifier) decodeRecord(key string, payload []byte) ([]byte, error) {
	if len(payload) == 0 || v.codec == nil {
		return payload, nil
	}
	if len(payload) < 12 {
		return nil, errInvalidBrowserRecord
	}
	plaintext, err := v.codec.Decrypt(payload[:12], payload[12:])
	if err != nil {
		return nil, errInvalidBrowserRecord
	}
	var envelope encryptedRecord
	if err := json.Unmarshal([]byte(plaintext), &envelope); err != nil || envelope.Key != key {
		return nil, errInvalidBrowserRecord
	}
	return envelope.Data, nil
}
func (v *Verifier) updateRecord(ctx context.Context, kind, id string, fn func([]byte) ([]byte, time.Time, error)) error {
	key := v.recordKey(kind, id)
	return v.store.UpdateBrowserRecord(ctx, key, func(payload []byte) ([]byte, time.Time, error) {
		data, err := v.decodeRecord(key, payload)
		if errors.Is(err, errInvalidBrowserRecord) {
			return nil, time.Time{}, nil
		}
		if err != nil {
			return nil, time.Time{}, err
		}
		data, expiry, err := fn(data)
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

func (v *Verifier) sessionNeedsUpdate(session browserSession, now time.Time) bool {
	interval := min(browserIdleWriteInterval, v.idleTTL/2)
	return session.IdentityPending || !session.ExpiresAt.After(now) || !session.IdleExpiresAt.After(now) ||
		!session.TokenExpiresAt.After(now) || (session.RefreshToken != "" && !session.TokenExpiresAt.After(now.Add(30*time.Second))) ||
		!session.LastActivityAt.Add(interval).After(now)
}
func encodeSession(session browserSession) ([]byte, time.Time, error) {
	data, err := json.Marshal(session)
	return data, minTime(session.ExpiresAt, session.IdleExpiresAt), err
}
func (v *Verifier) SessionWithError(r *http.Request) (api.AuthResult, string, bool, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return api.AuthResult{}, "", false, nil
	}
	// Disconnecting a browser must not roll back a refresh that the IdP has
	// already rotated. Both the transaction and provider call have a bounded,
	// independent lifetime.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
	defer cancel()
	key := v.recordKey("session", cookie.Value)
	payload, err := v.store.ReadBrowserRecord(ctx, key)
	var session browserSession
	if err == nil {
		data, decodeErr := v.decodeRecord(key, payload)
		if decodeErr == nil && len(data) == 0 {
			return api.AuthResult{}, "", false, nil
		}
		if decodeErr == nil && json.Unmarshal(data, &session) == nil && !v.sessionNeedsUpdate(session, time.Now()) {
			return sessionIdentity(session), session.AccessToken, true, nil
		}
	}
	valid := false
	if err == nil {
		err = v.updateRecord(ctx, "session", cookie.Value, func(data []byte) ([]byte, time.Time, error) {
			if len(data) == 0 || json.Unmarshal(data, &session) != nil {
				return nil, time.Time{}, nil
			}
			now := time.Now().UTC()
			if !session.ExpiresAt.After(now) || !session.IdleExpiresAt.After(now) {
				return nil, time.Time{}, nil
			}
			valid = true
			if !v.sessionNeedsUpdate(session, now) {
				return nil, time.Time{}, browserstore.ErrUnchanged
			}
			if !session.IdentityPending && !session.TokenExpiresAt.After(now.Add(30*time.Second)) {
				if session.RefreshToken == "" {
					if !session.TokenExpiresAt.After(now) {
						valid = false
						return nil, time.Time{}, nil
					}
				} else {
					token, rejected, err := v.refresh(ctx, session.RefreshToken)
					if rejected {
						valid = false
						return nil, time.Time{}, nil
					}
					if err != nil {
						return nil, time.Time{}, err
					}
					session.AccessToken = token.AccessToken
					if token.RefreshToken != "" {
						session.RefreshToken = token.RefreshToken
					}
					session.TokenExpiresAt = accessTokenExpiry(token, now)
					session.IdentityPending = true
					session.PendingIDToken = token.IDToken
				}
			}
			if !session.ExpiresAt.After(time.Now()) {
				valid = false
				return nil, time.Time{}, nil
			}
			session.LastActivityAt = now
			session.IdleExpiresAt = now.Add(v.idleTTL)
			return encodeSession(session)
		})
	}
	if err != nil {
		return api.AuthResult{}, "", false, errors.New("OIDC browser authentication temporarily unavailable")
	}
	if !valid {
		return api.AuthResult{}, "", false, nil
	}
	if session.IdentityPending {
		// Rotation is durable before fetching/verifying identity. A transient failure
		// leaves the pending identity in storage for the next request to retry.
		identityCtx, identityCancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
		defer identityCancel()
		valid = false
		err = v.updateRecord(identityCtx, "session", cookie.Value, func(data []byte) ([]byte, time.Time, error) {
			if len(data) == 0 || json.Unmarshal(data, &session) != nil {
				return nil, time.Time{}, nil
			}
			now := time.Now().UTC()
			if !session.ExpiresAt.After(now) || !session.IdleExpiresAt.After(now) {
				return nil, time.Time{}, nil
			}
			if session.IdentityPending {
				result, _, err := v.callbackIdentity(identityCtx, tokenResponse{AccessToken: session.AccessToken, IDToken: session.PendingIDToken})
				if err != nil {
					return nil, time.Time{}, err
				}
				if result.Subject == "" || result.Subject != session.Result.Subject {
					return nil, time.Time{}, nil
				}
				result.ExpiresAt = &session.TokenExpiresAt
				session.Result = result
				session.IdentityPending = false
				session.PendingIDToken = ""
				valid = true
				return encodeSession(session)
			}
			valid = true
			return nil, time.Time{}, browserstore.ErrUnchanged
		})
		if err != nil {
			return api.AuthResult{}, "", false, errors.New("OIDC browser identity temporarily unavailable")
		}
	}
	if !valid || !session.TokenExpiresAt.After(time.Now()) {
		return api.AuthResult{}, "", false, nil
	}
	return sessionIdentity(session), session.AccessToken, true, nil
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

func sessionIdentity(session browserSession) api.AuthResult {
	result := session.Result
	expiry := minTime(session.TokenExpiresAt, minTime(session.ExpiresAt, session.IdleExpiresAt))
	result.ExpiresAt = &expiry
	return result
}
