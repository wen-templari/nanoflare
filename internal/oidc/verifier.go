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
)

const (
	sessionCookieName = "nanoflare_oidc_session"
	defaultStateTTL   = 10 * time.Minute
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

	mu        sync.RWMutex
	discovery discoveryDocument
	keys      map[string]any
	states    map[string]browserState
	sessions  map[string]browserSession
}

type browserState struct {
	ReturnURL    string
	CodeVerifier string
	ExpiresAt    time.Time
}

type browserSession struct {
	AccessToken string
	Result      api.AuthResult
	ExpiresAt   time.Time
}

type discoveryDocument struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
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
		sessions:     make(map[string]browserSession),
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
		sessions:     make(map[string]browserSession),
	}
}

func (v *Verifier) BrowserFlowEnabled() bool {
	return v.clientID != "" && v.publicURL != ""
}

func (v *Verifier) Issuer() string {
	return v.issuer
}

func (v *Verifier) Session(r *http.Request) (api.AuthResult, string, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return api.AuthResult{}, "", false
	}
	now := time.Now().UTC()

	v.mu.RLock()
	session, ok := v.sessions[cookie.Value]
	v.mu.RUnlock()
	if !ok {
		return api.AuthResult{}, "", false
	}
	if !session.ExpiresAt.IsZero() && !session.ExpiresAt.After(now) {
		v.mu.Lock()
		delete(v.sessions, cookie.Value)
		v.mu.Unlock()
		return api.AuthResult{}, "", false
	}
	return session.Result, session.AccessToken, true
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

	v.mu.Lock()
	v.states[state] = browserState{
		ReturnURL:    returnURL,
		CodeVerifier: verifier,
		ExpiresAt:    time.Now().UTC().Add(defaultStateTTL),
	}
	v.mu.Unlock()

	http.Redirect(w, r, authorizationURL(discovery, v.clientID, v.redirectURL, state, verifier), http.StatusFound)
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

	v.mu.Lock()
	state, ok := v.states[stateValue]
	if ok {
		delete(v.states, stateValue)
	}
	v.mu.Unlock()
	if !ok {
		return errors.New("oidc callback state is invalid or expired")
	}
	if !state.ExpiresAt.After(time.Now().UTC()) {
		return errors.New("oidc callback state is invalid or expired")
	}

	discovery, err := v.discoveryForContext(r.Context())
	if err != nil {
		return err
	}
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
	var expiresAt time.Time
	if token.ExpiresIn > 0 {
		expiresAt = time.Now().UTC().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	if result.ExpiresAt != nil && (expiresAt.IsZero() || result.ExpiresAt.Before(expiresAt)) {
		expiresAt = result.ExpiresAt.UTC()
	}
	if expiresAt.IsZero() {
		expiresAt = time.Now().UTC().Add(time.Hour)
	}
	sessionID, err := randomToken()
	if err != nil {
		return err
	}

	v.mu.Lock()
	v.sessions[sessionID] = browserSession{
		AccessToken: token.AccessToken,
		Result:      result,
		ExpiresAt:   expiresAt,
	}
	v.mu.Unlock()

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
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

func authorizationURL(discovery discoveryDocument, clientID, redirectURL, state, verifier string) string {
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURL},
		"scope":                 {"openid email profile"},
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
		return nil, fmt.Errorf("invalid token: %w", err)
	}
	if err := v.verifySignature(ctx, header, signingInput, signature); err != nil {
		return nil, err
	}
	if claimsIssuer, _ := claims["iss"].(string); claimsIssuer != v.issuer {
		return nil, errors.New("invalid token: issuer mismatch")
	}
	if !audienceMatches(claims["aud"], audience) {
		return nil, errors.New("invalid token: audience mismatch")
	}
	if expiration, ok := numericDate(claims["exp"]); ok && !expiration.After(time.Now().UTC()) {
		return nil, errors.New("token expired")
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

func (v *Verifier) verifySignature(ctx context.Context, header jwtHeader, signingInput string, signature []byte) error {
	properties, err := jwtProperties(header.Alg)
	if err != nil {
		return fmt.Errorf("invalid token: %w", err)
	}
	key, err := v.keyForToken(ctx, header.Kid)
	if err != nil {
		return err
	}
	digest := properties.Hash.New()
	_, _ = digest.Write([]byte(signingInput))
	sum := digest.Sum(nil)
	switch typed := key.(type) {
	case *rsa.PublicKey:
		if properties.KeyType != "RSA" {
			return fmt.Errorf("invalid token: signing key type mismatch for %s", header.Alg)
		}
		if err := rsa.VerifyPKCS1v15(typed, properties.Hash, sum, signature); err != nil {
			return fmt.Errorf("invalid token: %w", err)
		}
	case *ecdsa.PublicKey:
		if properties.KeyType != "EC" {
			return fmt.Errorf("invalid token: signing key type mismatch for %s", header.Alg)
		}
		if err := verifyECDSASignature(typed, sum, signature); err != nil {
			return fmt.Errorf("invalid token: %w", err)
		}
	default:
		return errors.New("invalid token: unsupported signing key type")
	}
	return nil
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

func (v *Verifier) keyForToken(ctx context.Context, kid string) (any, error) {
	keys, err := v.keysForContext(ctx)
	if err != nil {
		return nil, err
	}
	if kid != "" {
		if key, ok := keys[kid]; ok {
			return key, nil
		}
	}
	if len(keys) == 1 {
		for _, key := range keys {
			return key, nil
		}
	}
	return nil, errors.New("invalid token: signing key not found")
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

func (v *Verifier) keysForContext(ctx context.Context) (map[string]any, error) {
	v.mu.RLock()
	if len(v.keys) > 0 {
		keys := cloneKeys(v.keys)
		v.mu.RUnlock()
		return keys, nil
	}
	v.mu.RUnlock()

	discovery, err := v.discoveryForContext(ctx)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, discovery.JWKSURI, nil)
	if err != nil {
		return nil, err
	}
	response, err := v.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("jwks request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks request failed: status %d", response.StatusCode)
	}
	var document jwksDocument
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		return nil, fmt.Errorf("decode jwks response: %w", err)
	}
	keys := make(map[string]any, len(document.Keys))
	for _, item := range document.Keys {
		key, err := keyFromJWK(item)
		if err != nil {
			return nil, err
		}
		keys[item.Kid] = key
	}
	v.mu.Lock()
	v.keys = keys
	v.mu.Unlock()
	return cloneKeys(keys), nil
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
