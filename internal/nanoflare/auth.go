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

var ErrInvalidCredentials = errors.New("invalid email or password")

type SignupInput struct {
	Email            string `json:"email"`
	Password         string `json:"password"`
	OrganizationName string `json:"organization_name"`
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

type ControlAuthService struct {
	store    Repository
	secret   []byte
	now      func() time.Time
	tokenTTL time.Duration
	randomID func() (string, error)
	hashCost int
}

func NewControlAuthService(store Repository, secret string) *ControlAuthService {
	return &ControlAuthService{
		store:    store,
		secret:   []byte(strings.TrimSpace(secret)),
		now:      time.Now,
		tokenTTL: defaultControlTokenTTL,
		randomID: randomToken,
		hashCost: bcrypt.DefaultCost,
	}
}

func (s *ControlAuthService) Signup(input SignupInput) (AuthSession, error) {
	if count, err := s.store.UserCount(); err != nil {
		return AuthSession{}, err
	} else if count > 0 {
		return AuthSession{}, errors.New("setup is already complete")
	}
	email, password, err := normalizeCredentials(input.Email, input.Password)
	if err != nil {
		return AuthSession{}, err
	}
	orgName := strings.TrimSpace(input.OrganizationName)
	if orgName == "" {
		return AuthSession{}, errors.New("organization_name is required")
	}
	userID, err := s.randomID()
	if err != nil {
		return AuthSession{}, err
	}
	orgID, err := s.randomID()
	if err != nil {
		return AuthSession{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.hashCost)
	if err != nil {
		return AuthSession{}, err
	}
	now := s.now().UTC()
	user := User{ID: userID, Email: email, PasswordHash: hash, CreatedAt: now}
	org := Organization{ID: orgID, Name: orgName, CreatedAt: now}
	if err := s.store.CreateUser(user); err != nil {
		return AuthSession{}, err
	}
	if err := s.store.CreateOrganization(org); err != nil {
		return AuthSession{}, err
	}
	if err := s.store.AddUserToOrganization(user.ID, org.ID); err != nil {
		return AuthSession{}, err
	}
	return s.sessionForUser(user, []Organization{org})
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
	orgs, err := s.store.ListOrganizationsForUser(user.ID)
	if err != nil {
		return AuthSession{}, err
	}
	return s.sessionForUser(user, orgs)
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

func (s *ControlAuthService) UserBelongsToOrganization(userID, orgID string) (bool, error) {
	return s.store.UserBelongsToOrganization(userID, orgID)
}

func (s *ControlAuthService) UserCount() (int, error) {
	return s.store.UserCount()
}

func (s *ControlAuthService) sessionForUser(user User, orgs []Organization) (AuthSession, error) {
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
