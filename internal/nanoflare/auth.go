package nanoflare

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const defaultControlTokenTTL = 24 * time.Hour
const defaultInviteTTL = 7 * 24 * time.Hour

var ErrInvalidCredentials = errors.New("invalid email or password")
var ErrInviteNotFound = errors.New("invite not found")
var ErrInviteExpired = errors.New("invite has expired")
var ErrInviteUsed = errors.New("invite has already been accepted")
var ErrInviteRevoked = errors.New("invite has been revoked")
var ErrInviteEmailMismatch = errors.New("signed-in user email does not match invite")
var ErrLastOwner = errors.New("organization must keep at least one owner")

type SignupInput struct {
	Email            string `json:"email"`
	Password         string `json:"password"`
	OrganizationName string `json:"organization_name,omitempty"`
}

type LoginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type AuthSession struct {
	Token         string         `json:"token"`
	User          User           `json:"user"`
	Organizations []Organization `json:"organizations"`
	ActiveOrgID   string         `json:"active_org_id,omitempty"`
}

type CreateOrganizationInput struct {
	Name string `json:"name"`
}

type CreateInviteInput struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

type UpdateMembershipInput struct {
	Role string `json:"role"`
}

type InviteCreated struct {
	OrganizationInvite
	Token     string `json:"token"`
	InviteURL string `json:"invite_url"`
}

type AcceptInviteInput struct {
	Email    string `json:"email,omitempty"`
	Password string `json:"password,omitempty"`
}

type AcceptInviteResponse struct {
	Membership OrganizationMembership `json:"membership"`
	Session    *AuthSession           `json:"session,omitempty"`
}

type ControlAuthService struct {
	store     Repository
	secret    []byte
	now       func() time.Time
	tokenTTL  time.Duration
	randomID  func() (string, error)
	hashCost  int
	inviteTTL time.Duration
}

func NewControlAuthService(store Repository, secret string) *ControlAuthService {
	return &ControlAuthService{
		store:     store,
		secret:    []byte(strings.TrimSpace(secret)),
		now:       time.Now,
		tokenTTL:  defaultControlTokenTTL,
		randomID:  randomToken,
		hashCost:  bcrypt.DefaultCost,
		inviteTTL: defaultInviteTTL,
	}
}

func (s *ControlAuthService) Signup(input SignupInput) (AuthSession, error) {
	email, password, err := normalizeCredentials(input.Email, input.Password)
	if err != nil {
		return AuthSession{}, err
	}
	userID, err := s.randomID()
	if err != nil {
		return AuthSession{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.hashCost)
	if err != nil {
		return AuthSession{}, err
	}
	now := s.now().UTC()
	user := User{ID: userID, Email: email, PasswordHash: hash, CreatedAt: now}
	if err := s.store.CreateUser(user); err != nil {
		return AuthSession{}, err
	}
	return s.sessionForUser(user)
}

func (s *ControlAuthService) Login(input LoginInput) (AuthSession, error) {
	email, password, err := normalizeCredentials(input.Email, input.Password)
	if err != nil {
		return AuthSession{}, err
	}
	user, err := s.store.UserByEmail(email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return AuthSession{}, ErrInvalidCredentials
		}
		return AuthSession{}, err
	}
	if err := bcrypt.CompareHashAndPassword(user.PasswordHash, []byte(password)); err != nil {
		return AuthSession{}, ErrInvalidCredentials
	}
	return s.sessionForUser(user)
}

func (s *ControlAuthService) Me(token string) (AuthSession, error) {
	user, err := s.ValidateToken(token)
	if err != nil {
		return AuthSession{}, err
	}
	orgs, err := s.store.ListOrganizationsForUser(user.ID)
	if err != nil {
		return AuthSession{}, err
	}
	return AuthSession{User: safeUser(user), Organizations: orgs, ActiveOrgID: firstOrgID(orgs)}, nil
}

func (s *ControlAuthService) ValidateToken(token string) (User, error) {
	if len(s.secret) == 0 {
		return User{}, errors.New("control auth secret is not configured")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return User{}, errors.New("invalid token")
	}
	signingInput := parts[0] + "." + parts[1]
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return User{}, errors.New("invalid token")
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(signingInput))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return User{}, errors.New("invalid token")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return User{}, errors.New("invalid token")
	}
	var claims struct {
		Subject string `json:"sub"`
		Email   string `json:"email"`
		Expires int64  `json:"exp"`
	}
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return User{}, errors.New("invalid token")
	}
	if claims.Subject == "" || claims.Email == "" || claims.Expires <= s.now().UTC().Unix() {
		return User{}, errors.New("invalid token")
	}
	return User{ID: claims.Subject, Email: claims.Email}, nil
}

func (s *ControlAuthService) Membership(userID, orgID string) (OrganizationMembership, error) {
	return s.store.OrganizationMembership(userID, orgID)
}

func (s *ControlAuthService) UserCount() (int, error) {
	return s.store.UserCount()
}

func (s *ControlAuthService) CreateOrganization(userID string, input CreateOrganizationInput) (Organization, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Organization{}, errors.New("name is required")
	}
	orgID, err := s.randomID()
	if err != nil {
		return Organization{}, err
	}
	now := s.now().UTC()
	org := Organization{ID: orgID, Name: name, CreatedAt: now}
	if err := s.store.CreateOrganization(org); err != nil {
		return Organization{}, err
	}
	membership := OrganizationMembership{UserID: userID, OrgID: org.ID, Role: RoleOwner, Scopes: RoleScopes(RoleOwner), CreatedAt: now}
	if err := s.store.UpsertOrganizationMembership(membership); err != nil {
		return Organization{}, err
	}
	org.Role = membership.Role
	org.Scopes = membership.Scopes
	return org, nil
}

func (s *ControlAuthService) CreateInvite(inviter User, orgID string, input CreateInviteInput, baseURL string) (InviteCreated, error) {
	email := strings.ToLower(strings.TrimSpace(input.Email))
	if email == "" || !strings.Contains(email, "@") {
		return InviteCreated{}, errors.New("email must be valid")
	}
	role := NormalizeRole(input.Role)
	if role == "" {
		return InviteCreated{}, errors.New("role is required")
	}
	token, err := s.randomID()
	if err != nil {
		return InviteCreated{}, err
	}
	id, err := s.randomID()
	if err != nil {
		return InviteCreated{}, err
	}
	now := s.now().UTC()
	invite := OrganizationInvite{
		ID: id, TokenHash: tokenHash(token), OrgID: strings.TrimSpace(orgID), Email: email, Role: role,
		Scopes: RoleScopes(role), InviterID: inviter.ID, ExpiresAt: now.Add(s.inviteTTL), CreatedAt: now,
	}
	if err := s.store.CreateOrganizationInvite(invite); err != nil {
		return InviteCreated{}, err
	}
	return InviteCreated{OrganizationInvite: invite, Token: token, InviteURL: strings.TrimRight(baseURL, "/") + "/invites/" + token}, nil
}

func (s *ControlAuthService) Members(orgID string) ([]OrganizationMembership, error) {
	return s.store.ListOrganizationMembers(strings.TrimSpace(orgID))
}

func (s *ControlAuthService) Invites(orgID string) ([]OrganizationInvite, error) {
	return s.store.OrganizationInvitesByOrg(strings.TrimSpace(orgID))
}

func (s *ControlAuthService) RevokeInvite(orgID, inviteID string) error {
	invite, err := s.store.OrganizationInviteByID(strings.TrimSpace(orgID), strings.TrimSpace(inviteID))
	if err != nil {
		return err
	}
	if invite.AcceptedAt != nil {
		return ErrInviteUsed
	}
	if invite.RevokedAt != nil {
		return ErrInviteRevoked
	}
	now := s.now().UTC()
	invite.RevokedAt = &now
	return s.store.UpdateOrganizationInvite(invite)
}

func (s *ControlAuthService) UpdateMembership(orgID, userID string, input UpdateMembershipInput) (OrganizationMembership, error) {
	role := NormalizeRole(input.Role)
	if role == "" {
		return OrganizationMembership{}, errors.New("role is required")
	}
	existing, err := s.store.OrganizationMembership(strings.TrimSpace(userID), strings.TrimSpace(orgID))
	if err != nil {
		return OrganizationMembership{}, err
	}
	if existing.Role == RoleOwner && role != RoleOwner {
		count, err := s.store.OwnerCount(orgID)
		if err != nil {
			return OrganizationMembership{}, err
		}
		if count <= 1 {
			return OrganizationMembership{}, ErrLastOwner
		}
	}
	existing.Role = role
	existing.Scopes = RoleScopes(role)
	if err := s.store.UpsertOrganizationMembership(existing); err != nil {
		return OrganizationMembership{}, err
	}
	return s.store.OrganizationMembership(userID, orgID)
}

func (s *ControlAuthService) DeleteMembership(orgID, userID string) error {
	existing, err := s.store.OrganizationMembership(strings.TrimSpace(userID), strings.TrimSpace(orgID))
	if err != nil {
		return err
	}
	if existing.Role == RoleOwner {
		count, err := s.store.OwnerCount(orgID)
		if err != nil {
			return err
		}
		if count <= 1 {
			return ErrLastOwner
		}
	}
	return s.store.DeleteOrganizationMembership(userID, orgID)
}

func (s *ControlAuthService) Invite(token string) (OrganizationInvite, error) {
	invite, err := s.store.OrganizationInviteByTokenHash(tokenHash(token))
	if err != nil {
		return OrganizationInvite{}, err
	}
	return invite, nil
}

func (s *ControlAuthService) AcceptInvite(token string, user User) (OrganizationMembership, error) {
	invite, err := s.store.OrganizationInviteByTokenHash(tokenHash(token))
	if err != nil {
		return OrganizationMembership{}, err
	}
	now := s.now().UTC()
	if invite.RevokedAt != nil {
		return OrganizationMembership{}, ErrInviteRevoked
	}
	if invite.AcceptedAt != nil {
		return OrganizationMembership{}, ErrInviteUsed
	}
	if !invite.ExpiresAt.After(now) {
		return OrganizationMembership{}, ErrInviteExpired
	}
	if !strings.EqualFold(user.Email, invite.Email) {
		return OrganizationMembership{}, ErrInviteEmailMismatch
	}
	membership := OrganizationMembership{UserID: user.ID, OrgID: invite.OrgID, Role: invite.Role, Scopes: invite.Scopes, CreatedAt: now}
	if err := s.store.UpsertOrganizationMembership(membership); err != nil {
		return OrganizationMembership{}, err
	}
	accepted := now
	invite.AcceptedAt = &accepted
	if err := s.store.UpdateOrganizationInvite(invite); err != nil {
		return OrganizationMembership{}, err
	}
	return membership, nil
}

func (s *ControlAuthService) sessionForUser(user User) (AuthSession, error) {
	orgs, err := s.store.ListOrganizationsForUser(user.ID)
	if err != nil {
		return AuthSession{}, err
	}
	token, err := s.issueToken(user)
	if err != nil {
		return AuthSession{}, err
	}
	return AuthSession{Token: token, User: safeUser(user), Organizations: orgs, ActiveOrgID: firstOrgID(orgs)}, nil
}

func (s *ControlAuthService) issueToken(user User) (string, error) {
	if len(s.secret) == 0 {
		return "", errors.New("control auth secret is not configured")
	}
	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]any{
		"sub":   user.ID,
		"email": user.Email,
		"exp":   s.now().UTC().Add(s.tokenTTL).Unix(),
	})
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func normalizeCredentials(email, password string) (string, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	password = strings.TrimSpace(password)
	if email == "" {
		return "", "", errors.New("email is required")
	}
	if !strings.Contains(email, "@") {
		return "", "", errors.New("email must be valid")
	}
	if password == "" {
		return "", "", errors.New("password is required")
	}
	return email, password, nil
}

func safeUser(user User) User {
	user.PasswordHash = nil
	return user
}

func firstOrgID(orgs []Organization) string {
	if len(orgs) == 0 {
		return ""
	}
	return orgs[0].ID
}
