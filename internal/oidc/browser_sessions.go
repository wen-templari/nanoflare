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
		if _, _, _, claims, err := parseJWT(token.IDToken); err == nil {
			if exp, ok := numericDate(claims["exp"]); ok {
				return exp
			}
		}
		return now.Add(time.Hour)
	}
	return expiry
}

// Browser limits are independent of token validity and checked at every phase.
func browserSessionActive(s browserSession, now time.Time) bool {
	return s.ExpiresAt.After(now) && s.IdleExpiresAt.After(now)
}
func sessionUsable(s browserSession, now time.Time) bool {
	return browserSessionActive(s, now) && !s.IdentityPending && s.TokenExpiresAt.After(now)
}

// acceptedSession retains the last verified identity while rotated tokens await checks.
// Records written before accepted credentials were introduced remain usable only
// when their current credentials have already passed identity verification.
func acceptedSession(s browserSession, now time.Time) (browserSession, bool) {
	if s.AcceptedAccessToken != "" {
		s.AccessToken = s.AcceptedAccessToken
		s.TokenExpiresAt = s.AcceptedTokenExpiresAt
	} else if s.IdentityPending {
		return s, false
	}
	return s, browserSessionActive(s, now) && s.TokenExpiresAt.After(now)
}
func (v *Verifier) activityDue(s browserSession, now time.Time) bool {
	return !s.LastActivityAt.Add(min(browserIdleWriteInterval, v.idleTTL/2)).After(now)
}
func encodeSession(session browserSession) ([]byte, time.Time, error) {
	data, err := json.Marshal(session)
	return data, minTime(session.ExpiresAt, session.IdleExpiresAt), err
}

var errBrowserTemporary = errors.New("OIDC browser authentication temporarily unavailable")

func (v *Verifier) readSession(ctx context.Context, id string) (browserSession, bool, error) {
	key := v.recordKey("session", id)
	payload, err := v.store.ReadBrowserRecord(ctx, key)
	if err != nil {
		return browserSession{}, false, err
	}
	if len(payload) == 0 {
		return browserSession{}, false, nil
	}
	data, decodeErr := v.decodeRecord(key, payload)
	var session browserSession
	if decodeErr != nil || json.Unmarshal(data, &session) != nil || !browserSessionActive(session, time.Now()) {
		// Recheck under the short update lock before deleting a stale/corrupt row.
		err = v.updateRecord(ctx, "session", id, func(data []byte) ([]byte, time.Time, error) {
			var current browserSession
			if len(data) == 0 || json.Unmarshal(data, &current) != nil || !browserSessionActive(current, time.Now()) {
				return nil, time.Time{}, nil
			}
			return nil, time.Time{}, browserstore.ErrUnchanged
		})
		return browserSession{}, false, err
	}
	return session, true, nil
}
func (v *Verifier) SessionWithError(r *http.Request) (api.AuthResult, string, bool, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return api.AuthResult{}, "", false, nil
	}
	// A bounded independent lifetime protects rotated credentials from disconnects.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 35*time.Second)
	defer cancel()
	id := cookie.Value
	refreshed := false
	waitDelay := 25 * time.Millisecond
	for {
		session, found, err := v.readSession(ctx, id)
		if err != nil {
			return api.AuthResult{}, "", false, errBrowserTemporary
		}
		if !found {
			return api.AuthResult{}, "", false, nil
		}
		now := time.Now().UTC()
		refreshDue := session.RefreshToken != "" && (!session.TokenExpiresAt.After(now) || (!session.IdentityPending && !refreshed && !session.TokenExpiresAt.After(now.Add(30*time.Second))))
		if !session.TokenExpiresAt.After(now) && session.RefreshToken == "" {
			_ = v.deleteSession(ctx, id, session)
			return api.AuthResult{}, "", false, nil
		}
		if !refreshDue && !session.IdentityPending {
			if v.activityDue(session, now) {
				var accepted, retry bool
				session, accepted, retry, err = v.renewActivity(ctx, id, session)
				if err != nil {
					return api.AuthResult{}, "", false, errBrowserTemporary
				}
				if retry {
					continue
				}
				if !accepted {
					return api.AuthResult{}, "", false, nil
				}
			}
			if sessionUsable(session, time.Now()) {
				return sessionIdentity(session), session.AccessToken, true, nil
			}
			continue
		}
		if session.LeaseOwner != "" && session.LeaseUntil.After(now) {
			if accepted, ok := acceptedSession(session, now); ok {
				return sessionIdentity(accepted), accepted.AccessToken, true, nil
			}
			timer := time.NewTimer(waitDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return api.AuthResult{}, "", false, errBrowserTemporary
			case <-timer.C:
				waitDelay = min(2*waitDelay, 250*time.Millisecond)
				continue
			}
		}
		owner, err := randomToken()
		if err != nil {
			return api.AuthResult{}, "", false, errBrowserTemporary
		}
		var claimed bool
		session, claimed, err = v.claimLease(ctx, id, owner, session)
		if err != nil {
			return api.AuthResult{}, "", false, errBrowserTemporary
		}
		if !claimed {
			continue
		}
		updated, permanent, err := v.runLeasedOperation(ctx, session, refreshDue, now)
		if refreshDue && err == nil {
			refreshed = true
		}
		committed, commitErr := v.commitLeased(ctx, id, owner, updated, permanent, err)
		if commitErr != nil {
			return api.AuthResult{}, "", false, errBrowserTemporary
		}
		if !committed {
			continue
		}
		if permanent {
			return api.AuthResult{}, "", false, nil
		}
		if err != nil {
			// Proactive refresh is best effort until the accepted token really expires.
			if accepted, ok := acceptedSession(session, time.Now()); ok {
				return sessionIdentity(accepted), accepted.AccessToken, true, nil
			}
			return api.AuthResult{}, "", false, errBrowserTemporary
		}
		// After rotation, the next iteration verifies pending identity using the
		// already durable new credentials. Expired pending tokens may refresh again.
	}
}
func (v *Verifier) renewActivity(ctx context.Context, id string, session browserSession) (updated browserSession, accepted, retry bool, err error) {
	accepted, retry = false, false
	err = v.updateRecord(ctx, "session", id, func(data []byte) ([]byte, time.Time, error) {
		var current browserSession
		if len(data) == 0 || json.Unmarshal(data, &current) != nil || !browserSessionActive(current, time.Now()) {
			return nil, time.Time{}, nil
		}
		if !sessionUsable(current, time.Now()) || current.AccessToken != session.AccessToken {
			retry = true
			return nil, time.Time{}, browserstore.ErrUnchanged
		}
		accepted = true
		if v.activityDue(current, time.Now()) {
			current.LastActivityAt = time.Now().UTC()
			current.IdleExpiresAt = current.LastActivityAt.Add(v.idleTTL)
			session = current
			return encodeSession(current)
		}
		session = current
		return nil, time.Time{}, browserstore.ErrUnchanged
	})
	return session, accepted, retry, err
}

func (v *Verifier) claimLease(ctx context.Context, id, owner string, session browserSession) (updated browserSession, claimed bool, err error) {
	claimed = false
	err = v.updateRecord(ctx, "session", id, func(data []byte) ([]byte, time.Time, error) {
		var current browserSession
		if len(data) == 0 || json.Unmarshal(data, &current) != nil || !browserSessionActive(current, time.Now()) {
			return nil, time.Time{}, nil
		}
		if current.AccessToken != session.AccessToken || current.RefreshToken != session.RefreshToken || current.IdentityPending != session.IdentityPending || !current.TokenExpiresAt.Equal(session.TokenExpiresAt) || (current.LeaseOwner != "" && current.LeaseUntil.After(time.Now())) {
			return nil, time.Time{}, browserstore.ErrUnchanged
		}
		current.LeaseOwner = owner
		current.LeaseUntil = time.Now().Add(30 * time.Second)
		session = current
		claimed = true
		return encodeSession(current)
	})
	return session, claimed, err
}

func (v *Verifier) runLeasedOperation(ctx context.Context, session browserSession, refreshDue bool, now time.Time) (updated browserSession, permanent bool, err error) {
	// No database transaction/connection is held during any IdP operation.
	opCtx, opCancel := context.WithTimeout(ctx, 10*time.Second)
	updated = session
	permanent = false
	if refreshDue {
		// Older persisted records may not yet have explicit accepted credentials.
		// Capture the verified current token before replacing it with pending work.
		if !session.IdentityPending {
			updated.AcceptedAccessToken = session.AccessToken
			updated.AcceptedTokenExpiresAt = session.TokenExpiresAt
		}
		var token tokenResponse
		token, permanent, err = v.refresh(opCtx, session.RefreshToken)
		if err == nil {
			updated.AccessToken = token.AccessToken
			if token.RefreshToken != "" {
				updated.RefreshToken = token.RefreshToken
			}
			updated.TokenExpiresAt = accessTokenExpiry(token, now)
			updated.PendingIDToken = token.IDToken
			updated.IdentityPending = true
			if !updated.TokenExpiresAt.After(time.Now()) {
				permanent = true
				err = errIdentityRejected
			}
		}
	} else {
		var result api.AuthResult
		result, permanent, err = v.refreshIdentity(opCtx, session)
		if err == nil {
			updated.Result = result
			updated.AcceptedAccessToken = updated.AccessToken
			updated.AcceptedTokenExpiresAt = updated.TokenExpiresAt
			updated.IdentityPending = false
			updated.PendingIDToken = ""
		}
	}
	opCancel()
	return updated, permanent, err
}

func (v *Verifier) commitLeased(ctx context.Context, id, owner string, updated browserSession, permanent bool, err error) (committed bool, commitErr error) {
	// Commit using the lease as a fencing token. A stale owner cannot overwrite
	// another refresh, resurrect a deleted session, or renew an expired browser.
	committed = false
	commitCtx, commitCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	commitErr = v.updateRecord(commitCtx, "session", id, func(data []byte) ([]byte, time.Time, error) {
		var current browserSession
		if len(data) == 0 || json.Unmarshal(data, &current) != nil {
			return nil, time.Time{}, nil
		}
		if current.LeaseOwner != owner {
			return nil, time.Time{}, browserstore.ErrUnchanged
		}
		if !browserSessionActive(current, time.Now()) || permanent {
			committed = true
			return nil, time.Time{}, nil
		}
		if err != nil {
			updated = current
		}
		updated.LeaseOwner = ""
		updated.LeaseUntil = time.Time{}
		committed = true
		return encodeSession(updated)
	})
	commitCancel()
	return committed, commitErr
}

func (v *Verifier) deleteSession(ctx context.Context, id string, expected browserSession) error {
	return v.updateRecord(ctx, "session", id, func(data []byte) ([]byte, time.Time, error) {
		var current browserSession
		if json.Unmarshal(data, &current) == nil && current.AccessToken != expected.AccessToken {
			return nil, time.Time{}, browserstore.ErrUnchanged
		}
		return nil, time.Time{}, nil
	})
}
func (v *Verifier) refreshIdentity(ctx context.Context, session browserSession) (api.AuthResult, bool, error) {
	raw, err := v.fetchUserInfo(ctx, session.AccessToken)
	if err != nil {
		return api.AuthResult{}, errors.Is(err, errIdentityRejected), err
	}
	var result api.AuthResult
	if session.PendingIDToken != "" {
		claims, err := v.parseAndValidateForAudience(ctx, session.PendingIDToken, v.clientID)
		if err != nil {
			return api.AuthResult{}, errors.Is(err, errInvalidIdentityToken), err
		}
		result = claimsToResult(claims)
		if sub, _ := raw["sub"].(string); sub != "" && sub != result.Subject {
			return api.AuthResult{}, true, errIdentityRejected
		}
	} else {
		// Without a replacement ID token, retain the authenticated login claims
		// and overlay only fields actually supplied by fresh userinfo.
		result = session.Result
		result.Claims = make(map[string]any, len(session.Result.Claims)+len(raw))
		for key, value := range session.Result.Claims {
			result.Claims[key] = value
		}
		for key, value := range raw {
			result.Claims[key] = value
		}
		if subject, ok := raw["sub"].(string); ok {
			result.Subject = subject
		}
	}
	result.Email, _ = raw["email"].(string)
	if result.Email == "" {
		result.Email, _ = raw[v.emailClaim].(string)
	}
	if result.Email == "" {
		result.Email, _ = result.Claims[v.emailClaim].(string)
	}
	if result.Subject == "" || result.Subject != session.Result.Subject || result.Email == "" {
		return api.AuthResult{}, true, errIdentityRejected
	}
	result.ExpiresAt = &session.TokenExpiresAt
	return result, false, nil
}

var errIdentityRejected = errors.New("OIDC identity rejected")

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

var _ api.BrowserAuthenticator = (*Verifier)(nil)

// LookupSession is optional worker identity only. It never renews activity,
// refreshes a token, fetches identity/keys, deletes records or waits for a lease.
// A slow store is allowed at most this small request-bound latency budget.
func (v *Verifier) LookupSession(r *http.Request) (api.AuthResult, string, bool, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return api.AuthResult{}, "", false, nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 100*time.Millisecond)
	defer cancel()
	key := v.recordKey("session", cookie.Value)
	payload, err := v.store.ReadBrowserRecord(ctx, key)
	if err != nil {
		return api.AuthResult{}, "", false, err
	}
	data, err := v.decodeRecord(key, payload)
	if err != nil {
		return api.AuthResult{}, "", false, nil
	}
	var session browserSession
	if json.Unmarshal(data, &session) != nil {
		return api.AuthResult{}, "", false, nil
	}
	accepted, ok := acceptedSession(session, time.Now())
	if !ok {
		return api.AuthResult{}, "", false, nil
	}
	return sessionIdentity(accepted), accepted.AccessToken, true, nil
}
