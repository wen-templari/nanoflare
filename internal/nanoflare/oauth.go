package nanoflare

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	defaultOAuthCodeTTL    = 10 * time.Minute
	defaultOAuthAccessTTL  = time.Hour
	defaultOAuthRefreshTTL = 90 * 24 * time.Hour
)

var (
	ErrOAuthClientNotFound = errors.New("oauth client not found")
	ErrOAuthTokenNotFound  = errors.New("oauth token not found")
	ErrOAuthInvalidGrant   = errors.New("invalid oauth grant")
	ErrOAuthInvalidScope   = errors.New("invalid oauth scope")
)

type OAuthClient struct {
	ID           string    `json:"client_id"`
	Name         string    `json:"name"`
	RedirectURIs []string  `json:"redirect_uris"`
	Scopes       []string  `json:"scopes"`
	SecretHash   []byte    `json:"-"`
	Disabled     bool      `json:"disabled,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type CreateOAuthClientInput struct {
	Name         string   `json:"name"`
	RedirectURIs []string `json:"redirect_uris"`
	Scopes       []string `json:"scopes"`
}

type OAuthClientCreated struct {
	OAuthClient
	ClientSecret string `json:"client_secret"`
}

type OAuthAuthorizationCode struct {
	CodeHash    string
	ClientID    string
	UserID      string
	OrgID       string
	RedirectURI string
	Scopes      []string
	ExpiresAt   time.Time
	UsedAt      *time.Time
	CreatedAt   time.Time
}

type OAuthToken struct {
	TokenHash        string
	RefreshTokenHash string
	ClientID         string
	UserID           string
	OrgID            string
	Scopes           []string
	ExpiresAt        time.Time
	RefreshExpiresAt time.Time
	RevokedAt        *time.Time
	CreatedAt        time.Time
}

type OAuthAccess struct {
	ClientID string
	UserID   string
	OrgID    string
	Scopes   []string
}

type OAuthConnection struct {
	ClientID  string    `json:"client_id"`
	Name      string    `json:"name"`
	Scopes    []string  `json:"scopes"`
	CreatedAt time.Time `json:"created_at"`
}

type OAuthAuthorizeInfo struct {
	ClientID    string   `json:"client_id"`
	ClientName  string   `json:"client_name"`
	RedirectURI string   `json:"redirect_uri"`
	Scopes      []string `json:"scopes"`
}

type OAuthAuthorizeInput struct {
	ClientID    string   `json:"client_id"`
	RedirectURI string   `json:"redirect_uri"`
	Scopes      []string `json:"scopes"`
	State       string   `json:"state,omitempty"`
	OrgID       string   `json:"org_id"`
}

type OAuthAuthorizeResponse struct {
	RedirectTo string `json:"redirect_to"`
	Code       string `json:"code"`
	State      string `json:"state,omitempty"`
}

type OAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope"`
}

type OAuthService struct {
	store      Repository
	now        func() time.Time
	randomID   func() (string, error)
	hashCost   int
	codeTTL    time.Duration
	accessTTL  time.Duration
	refreshTTL time.Duration
}

func NewOAuthService(store Repository) *OAuthService {
	return &OAuthService{
		store:      store,
		now:        time.Now,
		randomID:   randomToken,
		hashCost:   bcrypt.DefaultCost,
		codeTTL:    defaultOAuthCodeTTL,
		accessTTL:  defaultOAuthAccessTTL,
		refreshTTL: defaultOAuthRefreshTTL,
	}
}

func (s *OAuthService) CreateClient(input CreateOAuthClientInput) (OAuthClientCreated, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return OAuthClientCreated{}, errors.New("name is required")
	}
	redirects, err := normalizeRedirectURIs(input.RedirectURIs)
	if err != nil {
		return OAuthClientCreated{}, err
	}
	scopes, err := normalizeOAuthScopes(input.Scopes)
	if err != nil {
		return OAuthClientCreated{}, err
	}
	clientID, err := s.randomID()
	if err != nil {
		return OAuthClientCreated{}, err
	}
	secret, err := s.randomID()
	if err != nil {
		return OAuthClientCreated{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(secret), s.hashCost)
	if err != nil {
		return OAuthClientCreated{}, err
	}
	now := s.now().UTC()
	client := OAuthClient{ID: clientID, Name: name, RedirectURIs: redirects, Scopes: scopes, SecretHash: hash, CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreateOAuthClient(client); err != nil {
		return OAuthClientCreated{}, err
	}
	return OAuthClientCreated{OAuthClient: client, ClientSecret: secret}, nil
}

func (s *OAuthService) Authorize(user User, input OAuthAuthorizeInput) (OAuthAuthorizeResponse, error) {
	client, scopes, err := s.validClientRequest(input.ClientID, input.RedirectURI, input.Scopes)
	if err != nil {
		return OAuthAuthorizeResponse{}, err
	}
	orgID := strings.TrimSpace(input.OrgID)
	if orgID == "" {
		return OAuthAuthorizeResponse{}, errors.New("org_id is required")
	}
	ok, err := s.store.UserBelongsToOrganization(user.ID, orgID)
	if err != nil {
		return OAuthAuthorizeResponse{}, err
	}
	if !ok {
		return OAuthAuthorizeResponse{}, ErrMembershipNotFound
	}
	code, err := s.randomID()
	if err != nil {
		return OAuthAuthorizeResponse{}, err
	}
	now := s.now().UTC()
	authCode := OAuthAuthorizationCode{
		CodeHash:    tokenHash(code),
		ClientID:    client.ID,
		UserID:      user.ID,
		OrgID:       orgID,
		RedirectURI: input.RedirectURI,
		Scopes:      scopes,
		ExpiresAt:   now.Add(s.codeTTL),
		CreatedAt:   now,
	}
	if err := s.store.CreateOAuthAuthorizationCode(authCode); err != nil {
		return OAuthAuthorizeResponse{}, err
	}
	redirect, err := oauthRedirect(input.RedirectURI, code, strings.TrimSpace(input.State))
	if err != nil {
		return OAuthAuthorizeResponse{}, err
	}
	return OAuthAuthorizeResponse{RedirectTo: redirect, Code: code, State: strings.TrimSpace(input.State)}, nil
}

func (s *OAuthService) AuthorizeInfo(clientID, redirectURI string, scopes []string) (OAuthAuthorizeInfo, error) {
	client, normalizedScopes, err := s.validClientRequest(clientID, redirectURI, scopes)
	if err != nil {
		return OAuthAuthorizeInfo{}, err
	}
	return OAuthAuthorizeInfo{
		ClientID:    client.ID,
		ClientName:  client.Name,
		RedirectURI: strings.TrimSpace(redirectURI),
		Scopes:      normalizedScopes,
	}, nil
}

func (s *OAuthService) ExchangeAuthorizationCode(clientID, clientSecret, code, redirectURI string) (OAuthTokenResponse, error) {
	client, err := s.validateClientSecret(clientID, clientSecret)
	if err != nil {
		return OAuthTokenResponse{}, err
	}
	record, err := s.store.OAuthAuthorizationCode(tokenHash(code))
	if err != nil {
		return OAuthTokenResponse{}, ErrOAuthInvalidGrant
	}
	now := s.now().UTC()
	if record.ClientID != client.ID || record.RedirectURI != strings.TrimSpace(redirectURI) || record.UsedAt != nil || !record.ExpiresAt.After(now) {
		return OAuthTokenResponse{}, ErrOAuthInvalidGrant
	}
	used := now
	record.UsedAt = &used
	if err := s.store.UpdateOAuthAuthorizationCode(record); err != nil {
		return OAuthTokenResponse{}, err
	}
	return s.issueTokens(record.ClientID, record.UserID, record.OrgID, record.Scopes)
}

func (s *OAuthService) Refresh(clientID, clientSecret, refreshToken string) (OAuthTokenResponse, error) {
	client, err := s.validateClientSecret(clientID, clientSecret)
	if err != nil {
		return OAuthTokenResponse{}, err
	}
	existing, err := s.store.OAuthRefreshToken(tokenHash(refreshToken))
	if err != nil {
		return OAuthTokenResponse{}, ErrOAuthInvalidGrant
	}
	now := s.now().UTC()
	if existing.ClientID != client.ID || existing.RevokedAt != nil || !existing.RefreshExpiresAt.After(now) {
		return OAuthTokenResponse{}, ErrOAuthInvalidGrant
	}
	revoked := now
	existing.RevokedAt = &revoked
	if err := s.store.UpdateOAuthToken(existing); err != nil {
		return OAuthTokenResponse{}, err
	}
	return s.issueTokens(existing.ClientID, existing.UserID, existing.OrgID, existing.Scopes)
}

func (s *OAuthService) Revoke(token string) error {
	hash := tokenHash(token)
	record, err := s.store.OAuthAccessToken(hash)
	if err != nil {
		record, err = s.store.OAuthRefreshToken(hash)
	}
	if err != nil {
		return nil
	}
	now := s.now().UTC()
	record.RevokedAt = &now
	return s.store.UpdateOAuthToken(record)
}

func (s *OAuthService) Connections(userID, orgID string) ([]OAuthConnection, error) {
	return s.store.OAuthConnections(strings.TrimSpace(userID), strings.TrimSpace(orgID))
}

func (s *OAuthService) Disconnect(userID, orgID, clientID string) error {
	return s.store.RevokeOAuthClientTokens(strings.TrimSpace(userID), strings.TrimSpace(orgID), strings.TrimSpace(clientID), s.now().UTC())
}

func (s *OAuthService) ValidateAccessToken(token string) (OAuthAccess, error) {
	record, err := s.store.OAuthAccessToken(tokenHash(token))
	if err != nil {
		return OAuthAccess{}, err
	}
	if record.RevokedAt != nil || !record.ExpiresAt.After(s.now().UTC()) {
		return OAuthAccess{}, ErrOAuthTokenNotFound
	}
	return OAuthAccess{ClientID: record.ClientID, UserID: record.UserID, OrgID: record.OrgID, Scopes: append([]string(nil), record.Scopes...)}, nil
}

func (s *OAuthService) validClientRequest(clientID, redirectURI string, requested []string) (OAuthClient, []string, error) {
	client, err := s.store.OAuthClient(strings.TrimSpace(clientID))
	if err != nil {
		return OAuthClient{}, nil, err
	}
	if client.Disabled {
		return OAuthClient{}, nil, ErrOAuthClientNotFound
	}
	redirectURI = strings.TrimSpace(redirectURI)
	if !stringInSlice(redirectURI, client.RedirectURIs) {
		return OAuthClient{}, nil, errors.New("redirect_uri is not allowed")
	}
	scopes, err := normalizeOAuthScopes(requested)
	if err != nil {
		return OAuthClient{}, nil, err
	}
	for _, scope := range scopes {
		if !stringInSlice(scope, client.Scopes) {
			return OAuthClient{}, nil, ErrOAuthInvalidScope
		}
	}
	return client, scopes, nil
}

func (s *OAuthService) validateClientSecret(clientID, clientSecret string) (OAuthClient, error) {
	client, err := s.store.OAuthClient(strings.TrimSpace(clientID))
	if err != nil {
		return OAuthClient{}, err
	}
	if client.Disabled || bcrypt.CompareHashAndPassword(client.SecretHash, []byte(strings.TrimSpace(clientSecret))) != nil {
		return OAuthClient{}, ErrOAuthClientNotFound
	}
	return client, nil
}

func (s *OAuthService) issueTokens(clientID, userID, orgID string, scopes []string) (OAuthTokenResponse, error) {
	access, err := s.randomID()
	if err != nil {
		return OAuthTokenResponse{}, err
	}
	refresh, err := s.randomID()
	if err != nil {
		return OAuthTokenResponse{}, err
	}
	now := s.now().UTC()
	record := OAuthToken{
		TokenHash:        tokenHash(access),
		RefreshTokenHash: tokenHash(refresh),
		ClientID:         clientID,
		UserID:           userID,
		OrgID:            orgID,
		Scopes:           append([]string(nil), scopes...),
		ExpiresAt:        now.Add(s.accessTTL),
		RefreshExpiresAt: now.Add(s.refreshTTL),
		CreatedAt:        now,
	}
	if err := s.store.CreateOAuthToken(record); err != nil {
		return OAuthTokenResponse{}, err
	}
	return OAuthTokenResponse{
		AccessToken:  access,
		TokenType:    "Bearer",
		ExpiresIn:    int64(s.accessTTL.Seconds()),
		RefreshToken: refresh,
		Scope:        strings.Join(scopes, " "),
	}, nil
}

func normalizeRedirectURIs(values []string) ([]string, error) {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, errors.New("redirect_uris must be absolute URLs")
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("redirect_uris is required")
	}
	sort.Strings(result)
	return result, nil
}

func normalizeOAuthScopes(values []string) ([]string, error) {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		for _, scope := range strings.Fields(value) {
			if !isAllowedOAuthScope(scope) {
				return nil, ErrOAuthInvalidScope
			}
			if !seen[scope] {
				seen[scope] = true
				result = append(result, scope)
			}
		}
	}
	if len(result) == 0 {
		return nil, errors.New("scope is required")
	}
	sort.Strings(result)
	return result, nil
}

func isAllowedOAuthScope(scope string) bool {
	switch scope {
	case "apps:read", "apps:write", "deployments:write", "secrets:write", "kv:read", "kv:write", "objects:read", "objects:write":
		return true
	default:
		return false
	}
}

func oauthRedirect(base, code, state string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("code", code)
	if state != "" {
		query.Set("state", state)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func stringInSlice(value string, values []string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
