package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/browserstore"
	"github.com/clas/nanoflare/internal/nanoflare"
)

const (
	DefaultBrowserScopes      = "openid email profile"
	DefaultBrowserIdleTTL     = time.Hour
	DefaultBrowserAbsoluteTTL = 24 * time.Hour
	browserIdleWriteInterval  = time.Minute
	sessionCookieName         = "nanoflare_oidc_session"
	defaultStateTTL           = 10 * time.Minute
)

type Verifier struct {
	issuer       string
	audience     string
	emailClaim   string
	clientID     string
	clientSecret string
	publicURL    string
	redirectURL  string
	cookieDomain string
	client       *http.Client

	mu            sync.RWMutex
	discovery     discoveryDocument
	keys          map[string]any
	keysMu        sync.Mutex
	keysLoadedAt  time.Time
	keysFetchedAt time.Time
	forcedKeysAt  time.Time
	forcedKeysErr error
	states        map[string]browserState
	store         browserstore.Store
	codec         *nanoflare.SecretCodec
	idleTTL       time.Duration
	absoluteTTL   time.Duration
	scopes        string
}

type browserState struct {
	ReturnURL    string
	CodeVerifier string
	ExpiresAt    time.Time
}

type browserSession struct {
	AccessToken            string
	AcceptedAccessToken    string
	AcceptedTokenExpiresAt time.Time
	Result                 api.AuthResult
	ExpiresAt              time.Time
	RefreshToken           string
	TokenExpiresAt         time.Time
	IdleExpiresAt          time.Time
	LastActivityAt         time.Time
	IdentityPending        bool
	PendingIDToken         string
	LeaseOwner             string
	LeaseUntil             time.Time
}

type discoveryDocument struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	EndSessionEndpoint    string `json:"end_session_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	UserInfoEndpoint      string `json:"userinfo_endpoint"`
}

type jwksDocument struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

func NewVerifier(issuer, audience, emailClaim string, client *http.Client) *Verifier {
	return NewBrowserVerifier(issuer, audience, emailClaim, "", "", "", "", client)
}

func NewBrowserVerifier(issuer, audience, emailClaim, clientID, clientSecret, publicURL, cookieDomain string, client *http.Client) *Verifier {
	if client == nil {
		client = http.DefaultClient
	}
	if emailClaim == "" {
		emailClaim = "email"
	}
	publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	return &Verifier{
		issuer:       strings.TrimRight(strings.TrimSpace(issuer), "/"),
		audience:     strings.TrimSpace(audience),
		emailClaim:   strings.TrimSpace(emailClaim),
		clientID:     strings.TrimSpace(clientID),
		clientSecret: strings.TrimSpace(clientSecret),
		publicURL:    publicURL,
		redirectURL:  publicURL + "/internal/auth/callback",
		cookieDomain: strings.TrimSpace(cookieDomain),
		client:       client,
		states:       make(map[string]browserState),
		store:        browserstore.NewMemory(), idleTTL: DefaultBrowserIdleTTL, absoluteTTL: DefaultBrowserAbsoluteTTL, scopes: DefaultBrowserScopes,
	}
}

func NewConsoleVerifier(issuer, emailClaim, clientID, clientSecret, publicURL string, client *http.Client) *Verifier {
	if client == nil {
		client = http.DefaultClient
	}
	if emailClaim == "" {
		emailClaim = "email"
	}
	publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	return &Verifier{
		issuer:       strings.TrimRight(strings.TrimSpace(issuer), "/"),
		audience:     strings.TrimSpace(clientID),
		emailClaim:   strings.TrimSpace(emailClaim),
		clientID:     strings.TrimSpace(clientID),
		clientSecret: strings.TrimSpace(clientSecret),
		publicURL:    publicURL,
		redirectURL:  publicURL + "/v1/auth/oidc/callback",
		client:       client,
		states:       make(map[string]browserState),
		store:        browserstore.NewMemory(), idleTTL: DefaultBrowserIdleTTL, absoluteTTL: DefaultBrowserAbsoluteTTL, scopes: DefaultBrowserScopes,
	}
}

func (v *Verifier) BrowserFlowEnabled() bool {
	return v.clientID != "" && v.publicURL != ""
}

func (v *Verifier) Issuer() string {
	return v.issuer
}

func (v *Verifier) BeginAuth(w http.ResponseWriter, r *http.Request) error {
	if !v.BrowserFlowEnabled() {
		return errors.New("oidc browser flow is not configured")
	}
	discovery, err := v.discoveryForContext(r.Context())
	if err != nil {
		return err
	}
	state, err := randomToken()
	if err != nil {
		return err
	}
	verifier, err := randomVerifier()
	if err != nil {
		return err
	}
	returnURL := forwardedURL(r)
	if returnURL == "" {
		returnURL = "/"
	}

	if err := v.saveRecord(r.Context(), "state", state, browserState{ReturnURL: returnURL, CodeVerifier: verifier, ExpiresAt: time.Now().UTC().Add(defaultStateTTL)}, time.Now().UTC().Add(defaultStateTTL)); err != nil {
		return err
	}

	http.Redirect(w, r, authorizationURLWithScopes(discovery, v.clientID, v.redirectURL, state, verifier, v.scopes), http.StatusFound)
	return nil
}

func (v *Verifier) BeginConsoleAuth(w http.ResponseWriter, r *http.Request, next string) error {
	if !v.BrowserFlowEnabled() {
		return errors.New("oidc browser flow is not configured")
	}
	discovery, err := v.discoveryForContext(r.Context())
	if err != nil {
		return err
	}
	state, err := randomToken()
	if err != nil {
		return err
	}
	verifier, err := randomVerifier()
	if err != nil {
		return err
	}
	v.mu.Lock()
	v.states[state] = browserState{
		ReturnURL:    safeConsoleNext(next),
		CodeVerifier: verifier,
		ExpiresAt:    time.Now().UTC().Add(defaultStateTTL),
	}
	v.mu.Unlock()
	http.Redirect(w, r, authorizationURL(discovery, v.clientID, v.redirectURL, state, verifier), http.StatusFound)
	return nil
}

func (v *Verifier) HandleCallback(w http.ResponseWriter, r *http.Request) error {
	if !v.BrowserFlowEnabled() {
		return errors.New("oidc browser flow is not configured")
	}
	stateValue := strings.TrimSpace(r.URL.Query().Get("state"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if stateValue == "" || code == "" {
		return errors.New("oidc callback requires state and code")
	}

	var state browserState
	if err := v.updateRecord(r.Context(), "state", stateValue, func(data []byte) ([]byte, time.Time, error) {
		if len(data) == 0 {
			return nil, time.Time{}, errors.New("oidc callback state is invalid or expired")
		}
		if err := json.Unmarshal(data, &state); err != nil {
			return nil, time.Time{}, err
		}
		return nil, time.Time{}, nil
	}); err != nil {
		return err
	}
	if !state.ExpiresAt.After(time.Now()) {
		return errors.New("oidc callback state is invalid or expired")
	}

	discovery, err := v.discoveryForContext(r.Context())
	if err != nil {
		return err
	}
	tokenIssuedAt := time.Now().UTC()
	token, err := v.exchangeCode(r.Context(), discovery, code, state.CodeVerifier)
	if err != nil {
		return err
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return errors.New("oidc token exchange returned an empty access token")
	}

	result, _, err := v.callbackIdentity(r.Context(), token)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	expiresAt := now.Add(v.absoluteTTL)
	tokenExpiry := accessTokenExpiry(token, tokenIssuedAt)
	if !tokenExpiry.After(now) {
		return errors.New("oidc access token requires a usable expiry")
	}

	sessionID, err := randomToken()
	if err != nil {
		return err
	}

	if err := v.saveRecord(r.Context(), "session", sessionID, browserSession{
		AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, Result: result,
		AcceptedAccessToken: token.AccessToken, AcceptedTokenExpiresAt: tokenExpiry,
		ExpiresAt: expiresAt, TokenExpiresAt: tokenExpiry, IdleExpiresAt: now.Add(v.idleTTL), LastActivityAt: now,
	}, minTime(expiresAt, now.Add(v.idleTTL))); err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Domain:   v.cookieDomain,
		Path:     "/",
		HttpOnly: true,
		Secure:   strings.HasPrefix(v.redirectURL, "https://"),
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
	})
	http.Redirect(w, r, state.ReturnURL, http.StatusFound)
	return nil
}

func (v *Verifier) HandleConsoleCallback(r *http.Request) (api.AuthResult, string, error) {
	if !v.BrowserFlowEnabled() {
		return api.AuthResult{}, "", errors.New("oidc browser flow is not configured")
	}
	stateValue := strings.TrimSpace(r.URL.Query().Get("state"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if stateValue == "" || code == "" {
		return api.AuthResult{}, "", errors.New("oidc callback requires state and code")
	}
	v.mu.Lock()
	state, ok := v.states[stateValue]
	if ok {
		delete(v.states, stateValue)
	}
	v.mu.Unlock()
	if !ok || !state.ExpiresAt.After(time.Now().UTC()) {
		return api.AuthResult{}, "", errors.New("oidc callback state is invalid or expired")
	}
	discovery, err := v.discoveryForContext(r.Context())
	if err != nil {
		return api.AuthResult{}, "", err
	}
	token, err := v.exchangeCode(r.Context(), discovery, code, state.CodeVerifier)
	if err != nil {
		return api.AuthResult{}, "", err
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return api.AuthResult{}, "", errors.New("oidc token exchange returned an empty access token")
	}
	result, _, err := v.callbackIdentity(r.Context(), token)
	if err != nil {
		return api.AuthResult{}, "", err
	}
	if strings.TrimSpace(result.Subject) == "" {
		return api.AuthResult{}, "", errors.New("oidc subject is required")
	}
	return result, safeConsoleNext(state.ReturnURL), nil
}

func (v *Verifier) ConsoleLogoutURL(ctx context.Context, next string) (string, error) {
	next = safeConsoleNext(next)
	if next == "/" {
		next = "/login?sso_logged_out=1"
	}
	discovery, err := v.discoveryForContext(ctx)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(discovery.EndSessionEndpoint) == "" {
		return next, nil
	}
	query := url.Values{
		"client_id":                {v.clientID},
		"post_logout_redirect_uri": {v.publicURL + next},
	}
	return discovery.EndSessionEndpoint + "?" + query.Encode(), nil
}

func (v *Verifier) RedirectURL() string {
	return v.redirectURL
}

func (v *Verifier) PublicHost() string {
	parsed, err := url.Parse(v.publicURL)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func (v *Verifier) ValidateBrowserConfig() error {
	if !v.BrowserFlowEnabled() {
		return nil
	}
	parsed, err := url.Parse(v.publicURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("oidc public url must be an absolute URL")
	}
	if v.cookieDomain != "" {
		host := parsed.Hostname()
		domain := strings.TrimPrefix(v.cookieDomain, ".")
		if host != domain && !strings.HasSuffix(host, "."+domain) {
			return errors.New("oidc cookie domain must match the callback host or its parent domain")
		}
	}
	return nil
}

func (v *Verifier) ValidateToken(ctx context.Context, token string) (api.AuthResult, error) {
	claims, err := v.parseAndValidate(ctx, token)
	if err != nil {
		return api.AuthResult{}, err
	}
	result := claimsToResult(claims)
	if email, _ := claims[v.emailClaim].(string); email != "" {
		result.Email = email
	}
	return result, nil
}

func (v *Verifier) UserInfo(ctx context.Context, token string) (api.AuthResult, map[string]any, error) {
	claims, err := v.parseAndValidate(ctx, token)
	if err != nil {
		return api.AuthResult{}, nil, err
	}
	raw, err := v.fetchUserInfo(ctx, token)
	if err != nil {
		return api.AuthResult{}, nil, err
	}
	result := claimsToResult(claims)
	result.Email, _ = raw["email"].(string)
	if result.Email == "" {
		result.Email, _ = claims[v.emailClaim].(string)
	}
	if result.Email == "" {
		return api.AuthResult{}, nil, errors.New("userinfo email is required")
	}
	if result.Subject == "" {
		result.Subject, _ = raw["sub"].(string)
	}
	return result, raw, nil
}

func (v *Verifier) callbackIdentity(ctx context.Context, token tokenResponse) (api.AuthResult, map[string]any, error) {
	raw, err := v.fetchUserInfo(ctx, token.AccessToken)
	if err != nil {
		return api.AuthResult{}, nil, err
	}
	if strings.TrimSpace(token.IDToken) != "" {
		claims, err := v.parseAndValidateForAudience(ctx, token.IDToken, v.clientID)
		if err != nil {
			return api.AuthResult{}, nil, err
		}
		result := claimsToResult(claims)
		result.Email, _ = raw["email"].(string)
		if result.Email == "" {
			result.Email, _ = claims[v.emailClaim].(string)
		}
		if result.Email == "" {
			return api.AuthResult{}, nil, errors.New("userinfo email is required")
		}
		if result.Subject == "" {
			result.Subject, _ = raw["sub"].(string)
		}
		if strings.TrimSpace(result.Subject) == "" {
			return api.AuthResult{}, nil, errors.New("oidc subject is required")
		}
		return result, raw, nil
	}
	result := api.AuthResult{Valid: true, Claims: raw}
	result.Subject, _ = raw["sub"].(string)
	result.Email, _ = raw["email"].(string)
	if result.Email == "" {
		result.Email, _ = raw[v.emailClaim].(string)
	}
	if result.Email == "" {
		return api.AuthResult{}, nil, errors.New("userinfo email is required")
	}
	if strings.TrimSpace(result.Subject) == "" {
		return api.AuthResult{}, nil, errors.New("oidc subject is required")
	}
	return result, raw, nil
}

func claimsToResult(claims map[string]any) api.AuthResult {
	result := api.AuthResult{Valid: true, Claims: claims}
	if subject, _ := claims["sub"].(string); subject != "" {
		result.Subject = subject
	}
	if expiration, ok := numericDate(claims["exp"]); ok {
		value := expiration.UTC()
		result.ExpiresAt = &value
	}
	return result
}

func forwardedURL(r *http.Request) string {
	proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	uri := strings.TrimSpace(r.Header.Get("X-Forwarded-Uri"))
	if proto == "" || host == "" {
		return uri
	}
	if uri == "" {
		uri = "/"
	}
	return proto + "://" + host + uri
}

func safeConsoleNext(next string) string {
	next = strings.TrimSpace(next)
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

func authorizationURL(discovery discoveryDocument, clientID, redirectURL, state, verifier string) string {
	return authorizationURLWithScopes(discovery, clientID, redirectURL, state, verifier, DefaultBrowserScopes)
}

func authorizationURLWithScopes(discovery discoveryDocument, clientID, redirectURL, state, verifier, scopes string) string {
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURL},
		"scope":                 {scopes},
		"state":                 {state},
		"code_challenge":        {pkceChallenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	return discovery.AuthorizationEndpoint + "?" + query.Encode()
}

func (v *Verifier) exchangeCode(ctx context.Context, discovery discoveryDocument, code, verifier string) (tokenResponse, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {v.redirectURL},
		"client_id":     {v.clientID},
		"code_verifier": {verifier},
	}
	if v.clientSecret != "" {
		form.Set("client_secret", v.clientSecret)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, discovery.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := v.client.Do(request)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("oidc token exchange failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return tokenResponse{}, fmt.Errorf("oidc token exchange failed: status %d", response.StatusCode)
	}
	var token tokenResponse
	if err := json.NewDecoder(response.Body).Decode(&token); err != nil {
		return tokenResponse{}, fmt.Errorf("decode oidc token response: %w", err)
	}
	return token, nil
}

func randomVerifier() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func pkceChallenge(verifier string) string {
	sum := crypto.SHA256.New()
	_, _ = sum.Write([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum.Sum(nil))
}

func (v *Verifier) parseAndValidate(ctx context.Context, token string) (map[string]any, error) {
	return v.parseAndValidateForAudience(ctx, token, v.audience)
}

func (v *Verifier) parseAndValidateForAudience(ctx context.Context, token string, audience string) (map[string]any, error) {
	header, signingInput, signature, claims, err := parseJWT(token)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidIdentityToken, err)
	}
	issuedAt, _ := numericDate(claims["iat"])
	if err := v.verifySignature(ctx, header, signingInput, signature, issuedAt); err != nil {
		return nil, err
	}
	if claimsIssuer, _ := claims["iss"].(string); claimsIssuer != v.issuer {
		return nil, fmt.Errorf("%w: issuer mismatch", errInvalidIdentityToken)
	}
	if !audienceMatches(claims["aud"], audience) {
		return nil, fmt.Errorf("%w: audience mismatch", errInvalidIdentityToken)
	}
	if expiration, ok := numericDate(claims["exp"]); ok && !expiration.After(time.Now().UTC()) {
		return nil, fmt.Errorf("%w: token expired", errInvalidIdentityToken)
	}
	return claims, nil
}

func parseJWT(token string) (jwtHeader, string, []byte, map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return jwtHeader{}, "", nil, nil, errors.New("token must have header, payload, and signature")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return jwtHeader{}, "", nil, nil, fmt.Errorf("decode header: %w", err)
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return jwtHeader{}, "", nil, nil, fmt.Errorf("decode payload: %w", err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return jwtHeader{}, "", nil, nil, fmt.Errorf("decode signature: %w", err)
	}
	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return jwtHeader{}, "", nil, nil, fmt.Errorf("decode header json: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return jwtHeader{}, "", nil, nil, fmt.Errorf("decode payload json: %w", err)
	}
	return header, parts[0] + "." + parts[1], signature, claims, nil
}

var errInvalidIdentityToken = errors.New("invalid token")

// A cooldown can defer checking newly rotated keys. Such failures must not end
// browser sessions until a sufficiently recent key set can reject the token.
var errSigningKeysStale = errors.New("OIDC signing key verification deferred until JWKS reload")

// Keep fetch provenance with the exact key snapshot, including coalesced loads.
// fetchedAt is the request start time of the last successful JWKS fetch.
type signingKeys struct {
	keys      map[string]any
	fetchedAt time.Time
}

func (keys signingKeys) canReject(verificationStarted, issuedAt time.Time) bool {
	// A fetch started during this verification necessarily follows token arrival.
	// Otherwise, iat can establish that a cached set postdates issuance. Compare
	// whole seconds conservatively: JWT NumericDate commonly has second precision.
	return !keys.fetchedAt.Before(verificationStarted) ||
		(!issuedAt.IsZero() && issuedAt.Before(keys.fetchedAt.Truncate(time.Second)))
}

func (v *Verifier) verifySignature(ctx context.Context, header jwtHeader, signingInput string, signature []byte, issuedAt time.Time) error {
	started := time.Now()
	properties, err := jwtProperties(header.Alg)
	if err != nil {
		return fmt.Errorf("%w: %v", errInvalidIdentityToken, err)
	}
	keys, err := v.keysForContext(ctx)
	if err != nil {
		return err
	}
	if key, ok := selectSigningKey(keys.keys, header.Kid); ok {
		if verifySignatureWithKey(properties, key, signingInput, signature) == nil {
			return nil
		}
	}
	// Both unknown kids and same-ID rotation require a recent key set before
	// classifying verification failure as permanently invalid credentials.
	keys, err = v.loadKeys(ctx, true)
	if err != nil {
		return err
	}
	key, ok := selectSigningKey(keys.keys, header.Kid)
	if ok {
		err = verifySignatureWithKey(properties, key, signingInput, signature)
		if err == nil {
			return nil
		}
	}
	if !keys.canReject(started, issuedAt) {
		return errSigningKeysStale
	}
	if !ok {
		return fmt.Errorf("%w: signing key not found", errInvalidIdentityToken)
	}
	return fmt.Errorf("%w: %v", errInvalidIdentityToken, err)
}
func verifySignatureWithKey(properties jwtAlgorithm, key any, signingInput string, signature []byte) error {
	digest := properties.Hash.New()
	_, _ = digest.Write([]byte(signingInput))
	sum := digest.Sum(nil)
	switch key := key.(type) {
	case *rsa.PublicKey:
		if properties.KeyType != "RSA" {
			return errors.New("signing key type mismatch")
		}
		return rsa.VerifyPKCS1v15(key, properties.Hash, sum, signature)
	case *ecdsa.PublicKey:
		if properties.KeyType != "EC" {
			return errors.New("signing key type mismatch")
		}
		return verifyECDSASignature(key, sum, signature)
	default:
		return errors.New("unsupported signing key type")
	}
}

type jwtAlgorithm struct {
	Hash    crypto.Hash
	KeyType string
}

func jwtProperties(alg string) (jwtAlgorithm, error) {
	switch alg {
	case "RS256":
		return jwtAlgorithm{Hash: crypto.SHA256, KeyType: "RSA"}, nil
	case "RS384":
		return jwtAlgorithm{Hash: crypto.SHA384, KeyType: "RSA"}, nil
	case "RS512":
		return jwtAlgorithm{Hash: crypto.SHA512, KeyType: "RSA"}, nil
	case "ES256":
		return jwtAlgorithm{Hash: crypto.SHA256, KeyType: "EC"}, nil
	case "ES384":
		return jwtAlgorithm{Hash: crypto.SHA384, KeyType: "EC"}, nil
	case "ES512":
		return jwtAlgorithm{Hash: crypto.SHA512, KeyType: "EC"}, nil
	default:
		return jwtAlgorithm{}, fmt.Errorf("unsupported jwt alg %q", alg)
	}
}

func numericDate(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case float64:
		return time.Unix(int64(typed), 0), true
	case json.Number:
		seconds, err := typed.Int64()
		if err == nil {
			return time.Unix(seconds, 0), true
		}
	}
	return time.Time{}, false
}

func audienceMatches(value any, audience string) bool {
	switch typed := value.(type) {
	case string:
		return typed == audience
	case []any:
		for _, item := range typed {
			if text, _ := item.(string); text == audience {
				return true
			}
		}
	}
	return false
}

func selectSigningKey(keys map[string]any, kid string) (any, bool) {
	if kid != "" {
		key, ok := keys[kid]
		return key, ok
	}
	if len(keys) == 1 {
		for _, key := range keys {
			return key, true
		}
	}
	return nil, false
}
func (v *Verifier) fetchUserInfo(ctx context.Context, token string) (map[string]any, error) {
	discovery, err := v.discoveryForContext(ctx)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, discovery.UserInfoEndpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := v.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("userinfo request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, errIdentityRejected
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo request failed: status %d", response.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode userinfo response: %w", err)
	}
	return payload, nil
}

func (v *Verifier) discoveryForContext(ctx context.Context) (discoveryDocument, error) {
	v.mu.RLock()
	if v.discovery.JWKSURI != "" && v.discovery.UserInfoEndpoint != "" && v.discovery.AuthorizationEndpoint != "" && v.discovery.TokenEndpoint != "" {
		document := v.discovery
		v.mu.RUnlock()
		return document, nil
	}
	v.mu.RUnlock()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, v.issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return discoveryDocument{}, err
	}
	response, err := v.client.Do(request)
	if err != nil {
		return discoveryDocument{}, fmt.Errorf("oidc discovery failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return discoveryDocument{}, fmt.Errorf("oidc discovery failed: status %d", response.StatusCode)
	}
	var document discoveryDocument
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		return discoveryDocument{}, fmt.Errorf("decode oidc discovery: %w", err)
	}
	if document.JWKSURI == "" || document.UserInfoEndpoint == "" || document.AuthorizationEndpoint == "" || document.TokenEndpoint == "" {
		return discoveryDocument{}, errors.New("oidc discovery is missing required endpoints")
	}

	v.mu.Lock()
	v.discovery = document
	v.mu.Unlock()
	return document, nil
}

func (v *Verifier) keysForContext(ctx context.Context) (signingKeys, error) {
	return v.loadKeys(ctx, false)
}

const forcedJWKSReloadInterval = 30 * time.Second

func (v *Verifier) loadKeys(ctx context.Context, force bool) (result signingKeys, loadErr error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	started := time.Now()
	// Coalescing must respect cancellation even when another caller's JWKS
	// request is stalled. Never wait unboundedly on a process-local mutex.
	for !v.keysMu.TryLock() {
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return signingKeys{}, ctx.Err()
		case <-timer.C:
		}
	}
	defer v.keysMu.Unlock()
	// Rate-limit attempts as well as successful reloads, so a failing IdP cannot
	// be flooded. Preserve the temporary error throughout the cooldown.
	if force && time.Since(v.forcedKeysAt) < forcedJWKSReloadInterval {
		v.mu.RLock()
		keys := signingKeys{cloneKeys(v.keys), v.keysFetchedAt}
		v.mu.RUnlock()
		return keys, v.forcedKeysErr
	}
	if force {
		v.forcedKeysAt = time.Now()
		defer func() { v.forcedKeysErr = loadErr }()
	}
	v.mu.RLock()
	if v.keys != nil && ((!force && time.Since(v.keysLoadedAt) < 5*time.Minute) || v.keysLoadedAt.After(started)) {
		keys := signingKeys{cloneKeys(v.keys), v.keysFetchedAt}
		v.mu.RUnlock()
		return keys, nil
	}
	v.mu.RUnlock()
	discovery, err := v.discoveryForContext(ctx)
	if err != nil {
		return signingKeys{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, discovery.JWKSURI, nil)
	if err != nil {
		return signingKeys{}, err
	}
	fetchedAt := time.Now()
	response, err := v.client.Do(request)
	if err != nil {
		return signingKeys{}, fmt.Errorf("jwks request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return signingKeys{}, fmt.Errorf("jwks request failed: status %d", response.StatusCode)
	}
	var document jwksDocument
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		return signingKeys{}, fmt.Errorf("decode jwks response: %w", err)
	}
	keys := make(map[string]any, len(document.Keys))
	for _, item := range document.Keys {
		key, err := keyFromJWK(item)
		if err != nil {
			return signingKeys{}, err
		}
		keys[item.Kid] = key
	}
	v.mu.Lock()
	v.keys = keys
	v.keysLoadedAt = time.Now()
	v.keysFetchedAt = fetchedAt
	v.forcedKeysAt = v.keysLoadedAt
	v.forcedKeysErr = nil
	v.mu.Unlock()
	return signingKeys{cloneKeys(keys), fetchedAt}, nil
}

func cloneKeys(keys map[string]any) map[string]any {
	out := make(map[string]any, len(keys))
	for key, value := range keys {
		out[key] = value
	}
	return out
}

func keyFromJWK(value jwk) (any, error) {
	switch value.Kty {
	case "RSA":
		return rsaKeyFromJWK(value)
	case "EC":
		return ecKeyFromJWK(value)
	default:
		return nil, fmt.Errorf("unsupported jwk kty %q", value.Kty)
	}
}

func rsaKeyFromJWK(value jwk) (*rsa.PublicKey, error) {
	if value.N == "" || value.E == "" {
		return nil, errors.New("jwks key is missing modulus or exponent")
	}
	modulusBytes, err := base64.RawURLEncoding.DecodeString(value.N)
	if err != nil {
		return nil, fmt.Errorf("decode jwk modulus: %w", err)
	}
	exponentBytes, err := base64.RawURLEncoding.DecodeString(value.E)
	if err != nil {
		return nil, fmt.Errorf("decode jwk exponent: %w", err)
	}
	exponent := 0
	for _, b := range exponentBytes {
		exponent = exponent<<8 + int(b)
	}
	if exponent == 0 {
		return nil, errors.New("jwks exponent is invalid")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(modulusBytes), E: exponent}, nil
}

func ecKeyFromJWK(value jwk) (*ecdsa.PublicKey, error) {
	if value.Crv == "" || value.X == "" || value.Y == "" {
		return nil, errors.New("jwks key is missing curve or coordinates")
	}
	curve, err := ellipticCurve(value.Crv)
	if err != nil {
		return nil, err
	}
	xBytes, err := base64.RawURLEncoding.DecodeString(value.X)
	if err != nil {
		return nil, fmt.Errorf("decode jwk x coordinate: %w", err)
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(value.Y)
	if err != nil {
		return nil, fmt.Errorf("decode jwk y coordinate: %w", err)
	}
	x := new(big.Int).SetBytes(xBytes)
	y := new(big.Int).SetBytes(yBytes)
	if !curve.IsOnCurve(x, y) {
		return nil, errors.New("jwks EC point is not on curve")
	}
	return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, nil
}

func ellipticCurve(name string) (elliptic.Curve, error) {
	switch name {
	case "P-256":
		return elliptic.P256(), nil
	case "P-384":
		return elliptic.P384(), nil
	case "P-521":
		return elliptic.P521(), nil
	default:
		return nil, fmt.Errorf("unsupported jwk curve %q", name)
	}
}

func verifyECDSASignature(key *ecdsa.PublicKey, digest []byte, signature []byte) error {
	size := (key.Curve.Params().BitSize + 7) / 8
	if len(signature) != size*2 {
		return errors.New("invalid ECDSA signature length")
	}
	type ecdsaSignature struct {
		R, S *big.Int
	}
	value := ecdsaSignature{
		R: new(big.Int).SetBytes(signature[:size]),
		S: new(big.Int).SetBytes(signature[size:]),
	}
	if value.R.Sign() <= 0 || value.S.Sign() <= 0 {
		return errors.New("invalid ECDSA signature values")
	}
	if !ecdsa.Verify(key, digest, value.R, value.S) {
		return errors.New("ecdsa verification failed")
	}
	return nil
}

func randomToken() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
