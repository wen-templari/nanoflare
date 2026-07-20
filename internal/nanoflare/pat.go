package nanoflare

import (
	"errors"
	"sort"
	"strings"
	"time"
)

const (
	PATScopeTypeUser = "user"
	PATScopeTypeOrg  = "org"
)

type CreatePersonalAccessTokenInput struct {
	Name      string     `json:"name"`
	ScopeType string     `json:"scope_type"`
	OrgID     string     `json:"org_id,omitempty"`
	Scopes    []string   `json:"scopes,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type PATAccess struct {
	Token     PersonalAccessToken
	User      User
	OrgID     string
	Scopes    []string
	ScopeType string
}

func (s *ControlAuthService) CreatePersonalAccessToken(userID string, input CreatePersonalAccessTokenInput) (PersonalAccessTokenCreated, error) {
	user, err := s.store.UserByID(strings.TrimSpace(userID))
	if err != nil {
		return PersonalAccessTokenCreated{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return PersonalAccessTokenCreated{}, errors.New("name is required")
	}
	scopeType := strings.ToLower(strings.TrimSpace(input.ScopeType))
	if scopeType == "" {
		scopeType = PATScopeTypeUser
	}
	if scopeType != PATScopeTypeUser && scopeType != PATScopeTypeOrg {
		return PersonalAccessTokenCreated{}, errors.New("scope_type must be user or org")
	}
	orgID := strings.TrimSpace(input.OrgID)
	if scopeType == PATScopeTypeOrg && orgID == "" {
		return PersonalAccessTokenCreated{}, errors.New("org_id is required for org-scoped personal access tokens")
	}
	if scopeType == PATScopeTypeUser {
		orgID = ""
	}
	scopes := normalizePATScopes(input.Scopes)
	if scopeType == PATScopeTypeOrg {
		membership, err := s.store.OrganizationMembership(user.ID, orgID)
		if err != nil {
			return PersonalAccessTokenCreated{}, err
		}
		if len(scopes) == 0 {
			scopes = append([]string(nil), membership.Scopes...)
		}
		if !scopesSubset(scopes, membership.Scopes) {
			return PersonalAccessTokenCreated{}, errors.New("personal access token scopes exceed organization membership")
		}
	} else if len(scopes) == 0 {
		scopes = AllowedControlScopes()
	} else if !allKnownScopes(scopes) {
		return PersonalAccessTokenCreated{}, errors.New("personal access token contains an unknown scope")
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(s.now().UTC()) {
		return PersonalAccessTokenCreated{}, errors.New("expires_at must be in the future")
	}
	id, err := s.randomID()
	if err != nil {
		return PersonalAccessTokenCreated{}, err
	}
	plain, err := s.randomID()
	if err != nil {
		return PersonalAccessTokenCreated{}, err
	}
	now := s.now().UTC()
	token := PersonalAccessToken{
		ID:        id,
		TokenHash: tokenHash(plain),
		Name:      name,
		UserID:    user.ID,
		OrgID:     orgID,
		ScopeType: scopeType,
		Scopes:    scopes,
		ExpiresAt: input.ExpiresAt,
		CreatedAt: now,
	}
	if err := s.store.CreatePersonalAccessToken(token); err != nil {
		return PersonalAccessTokenCreated{}, err
	}
	return PersonalAccessTokenCreated{PersonalAccessToken: token, Token: plain}, nil
}

func (s *ControlAuthService) PersonalAccessTokens(userID string) ([]PersonalAccessToken, error) {
	return s.store.PersonalAccessTokensByUser(strings.TrimSpace(userID))
}

func (s *ControlAuthService) RevokePersonalAccessToken(userID, tokenID string) error {
	tokenID = strings.TrimSpace(tokenID)
	tokens, err := s.store.PersonalAccessTokensByUser(strings.TrimSpace(userID))
	if err != nil {
		return err
	}
	for _, token := range tokens {
		if token.ID != tokenID {
			continue
		}
		now := s.now().UTC()
		token.RevokedAt = &now
		return s.store.UpdatePersonalAccessToken(token)
	}
	return ErrPersonalAccessTokenNotFound
}

func (s *ControlAuthService) ValidatePersonalAccessToken(tokenValue, requestedOrgID string) (PATAccess, error) {
	record, err := s.store.PersonalAccessTokenByHash(tokenHash(tokenValue))
	if err != nil {
		return PATAccess{}, errors.New("invalid token")
	}
	now := s.now().UTC()
	if record.RevokedAt != nil || (record.ExpiresAt != nil && !record.ExpiresAt.After(now)) {
		return PATAccess{}, errors.New("invalid token")
	}
	user, err := s.store.UserByID(record.UserID)
	if err != nil {
		return PATAccess{}, err
	}
	access := PATAccess{Token: record, User: safeUser(user), ScopeType: record.ScopeType}
	switch record.ScopeType {
	case PATScopeTypeOrg:
		access.OrgID = record.OrgID
		access.Scopes = append([]string(nil), record.Scopes...)
	case PATScopeTypeUser:
		orgID := strings.TrimSpace(requestedOrgID)
		if orgID == "" {
			return PATAccess{}, errors.New("X-Nanoflare-Org-ID is required")
		}
		membership, err := s.store.OrganizationMembership(record.UserID, orgID)
		if err != nil {
			return PATAccess{}, err
		}
		access.OrgID = orgID
		access.Scopes = scopeIntersection(record.Scopes, membership.Scopes)
		if len(access.Scopes) == 0 {
			return PATAccess{}, errors.New("personal access token has no scopes for this organization")
		}
	default:
		return PATAccess{}, errors.New("invalid token")
	}
	used := now
	record.LastUsedAt = &used
	_ = s.store.UpdatePersonalAccessToken(record)
	return access, nil
}

func (s *ControlAuthService) SessionForPersonalAccessToken(tokenValue string) (AuthSession, error) {
	record, err := s.store.PersonalAccessTokenByHash(tokenHash(tokenValue))
	if err != nil {
		return AuthSession{}, errors.New("invalid token")
	}
	now := s.now().UTC()
	if record.RevokedAt != nil || (record.ExpiresAt != nil && !record.ExpiresAt.After(now)) {
		return AuthSession{}, errors.New("invalid token")
	}
	user, err := s.store.UserByID(record.UserID)
	if err != nil {
		return AuthSession{}, err
	}
	orgs, err := s.store.ListOrganizationsForUser(user.ID)
	if err != nil {
		return AuthSession{}, err
	}
	activeOrgID := firstOrgID(orgs)
	if record.ScopeType == PATScopeTypeOrg {
		activeOrgID = record.OrgID
		orgs = filterOrganizations(orgs, activeOrgID)
	}
	used := now
	record.LastUsedAt = &used
	_ = s.store.UpdatePersonalAccessToken(record)
	return AuthSession{Token: strings.TrimSpace(tokenValue), User: safeUser(user), Organizations: orgs, ActiveOrgID: activeOrgID}, nil
}

func AllowedControlScopes() []string {
	return []string{"workers:read", "workers:write", "deployments:write", "secrets:write", "kv:read", "kv:write", "db:read", "db:write", "objects:read", "objects:write", "orgs:read", "orgs:write", "members:read", "members:write", "members:owner"}
}

func normalizePATScopes(values []string) []string {
	seen := map[string]bool{}
	var scopes []string
	for _, value := range values {
		for _, scope := range strings.Fields(value) {
			if seen[scope] {
				continue
			}
			seen[scope] = true
			scopes = append(scopes, scope)
		}
	}
	sort.Strings(scopes)
	return scopes
}

func allKnownScopes(scopes []string) bool {
	return scopesSubset(scopes, AllowedControlScopes())
}

func scopesSubset(scopes, allowed []string) bool {
	for _, scope := range scopes {
		if !HasScope(allowed, scope) {
			return false
		}
	}
	return true
}

func scopeIntersection(left, right []string) []string {
	var scopes []string
	for _, scope := range left {
		if HasScope(right, scope) {
			scopes = append(scopes, scope)
		}
	}
	sort.Strings(scopes)
	return scopes
}

func filterOrganizations(orgs []Organization, orgID string) []Organization {
	for _, org := range orgs {
		if org.ID == orgID {
			return []Organization{org}
		}
	}
	return nil
}
