package nanoflare

import (
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	PartnerConnectionStatusActive  = "active"
	PartnerConnectionStatusRevoked = "revoked"
)

var (
	ErrPartnerIntegrationNotFound = errors.New("partner integration not found")
	ErrPartnerIntegrationExists   = errors.New("partner integration already exists")
	ErrPartnerConnectionNotFound  = errors.New("partner connection not found")
)

type PartnerIntegration struct {
	ID            string    `json:"id"`
	OwnerOrgID    string    `json:"owner_org_id"`
	Name          string    `json:"name"`
	AllowedScopes []string  `json:"allowed_scopes"`
	SecretHash    []byte    `json:"-"`
	Disabled      bool      `json:"disabled,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type PartnerConnection struct {
	ID                string     `json:"id"`
	IntegrationID     string     `json:"integration_id"`
	ExternalAccountID string     `json:"external_account_id"`
	OrgID             string     `json:"org_id"`
	Status            string     `json:"status"`
	CreatedAt         time.Time  `json:"created_at"`
	RevokedAt         *time.Time `json:"revoked_at,omitempty"`
}

type CreatePartnerIntegrationInput struct {
	OwnerOrgID    string   `json:"-"`
	Name          string   `json:"name"`
	AllowedScopes []string `json:"allowed_scopes"`
}
type PartnerIntegrationCreated struct {
	PartnerIntegration
	Secret string `json:"secret"`
}
type ProvisionPartnerConnectionInput struct {
	ExternalAccountID string   `json:"external_account_id"`
	OrganizationName  string   `json:"organization_name"`
	RequestedScopes   []string `json:"requested_scopes"`
}
type PartnerConnectionProvisioned struct {
	Connection   PartnerConnection `json:"connection"`
	Organization Organization      `json:"organization"`
	Created      bool              `json:"created"`
}

type PartnerService struct {
	store    Repository
	now      func() time.Time
	randomID func() (string, error)
	hashCost int
}

func NewPartnerService(store Repository) *PartnerService {
	return &PartnerService{store: store, now: time.Now, randomID: randomToken, hashCost: bcrypt.DefaultCost}
}

// AuthenticateIntegration validates the one-time-issued client secret for a
// partner integration without creating or changing a connection.
func (s *PartnerService) AuthenticateIntegration(integrationID, secret string) error {
	_, err := s.authenticate(integrationID, secret)
	return err
}

func (s *PartnerService) CreateIntegration(input CreatePartnerIntegrationInput) (PartnerIntegrationCreated, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return PartnerIntegrationCreated{}, errors.New("name is required")
	}
	scopes, err := normalizeOAuthScopes(input.AllowedScopes)
	if err != nil {
		return PartnerIntegrationCreated{}, err
	}
	if strings.TrimSpace(input.OwnerOrgID) == "" {
		return PartnerIntegrationCreated{}, errors.New("owner_org_id is required")
	}
	if _, err := s.store.GetOrganization(input.OwnerOrgID); err != nil {
		return PartnerIntegrationCreated{}, err
	}
	id, err := s.randomID()
	if err != nil {
		return PartnerIntegrationCreated{}, err
	}
	secret, err := s.randomID()
	if err != nil {
		return PartnerIntegrationCreated{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(secret), s.hashCost)
	if err != nil {
		return PartnerIntegrationCreated{}, err
	}
	now := s.now().UTC()
	integration := PartnerIntegration{ID: id, OwnerOrgID: strings.TrimSpace(input.OwnerOrgID), Name: name, AllowedScopes: scopes, SecretHash: hash, CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreatePartnerIntegration(integration); err != nil {
		return PartnerIntegrationCreated{}, err
	}
	return PartnerIntegrationCreated{PartnerIntegration: integration, Secret: secret}, nil
}

func (s *PartnerService) Integrations(ownerOrgID string) ([]PartnerIntegration, error) {
	return s.store.PartnerIntegrationsByOwnerOrg(strings.TrimSpace(ownerOrgID))
}
func (s *PartnerService) Integration(ownerOrgID, id string) (PartnerIntegration, error) {
	integration, err := s.store.PartnerIntegration(strings.TrimSpace(id))
	if err != nil || integration.OwnerOrgID != strings.TrimSpace(ownerOrgID) {
		return PartnerIntegration{}, ErrPartnerIntegrationNotFound
	}
	return integration, nil
}
func (s *PartnerService) Connections(integrationID string) ([]PartnerConnection, error) {
	return s.store.PartnerConnectionsByIntegration(strings.TrimSpace(integrationID))
}
func (s *PartnerService) Connection(id string) (PartnerConnection, error) {
	return s.store.PartnerConnection(strings.TrimSpace(id))
}
func (s *PartnerService) Organization(id string) (Organization, error) {
	return s.store.GetOrganization(strings.TrimSpace(id))
}
func (s *PartnerService) authenticate(id, secret string) (PartnerIntegration, error) {
	integration, err := s.store.PartnerIntegration(strings.TrimSpace(id))
	if err != nil || integration.Disabled || bcrypt.CompareHashAndPassword(integration.SecretHash, []byte(strings.TrimSpace(secret))) != nil {
		return PartnerIntegration{}, ErrPartnerIntegrationNotFound
	}
	return integration, nil
}

func (s *PartnerService) Provision(integrationID, secret string, input ProvisionPartnerConnectionInput) (PartnerConnectionProvisioned, error) {
	integration, err := s.authenticate(integrationID, secret)
	if err != nil {
		return PartnerConnectionProvisioned{}, err
	}
	externalID, name := strings.TrimSpace(input.ExternalAccountID), strings.TrimSpace(input.OrganizationName)
	if externalID == "" {
		return PartnerConnectionProvisioned{}, errors.New("external_account_id is required")
	}
	if name == "" {
		return PartnerConnectionProvisioned{}, errors.New("organization_name is required")
	}
	scopes, err := normalizeOAuthScopes(input.RequestedScopes)
	if err != nil {
		return PartnerConnectionProvisioned{}, err
	}
	for _, scope := range scopes {
		if !stringInSlice(scope, integration.AllowedScopes) {
			return PartnerConnectionProvisioned{}, ErrOAuthInvalidScope
		}
	}
	orgID, err := s.randomID()
	if err != nil {
		return PartnerConnectionProvisioned{}, err
	}
	connectionID, err := s.randomID()
	if err != nil {
		return PartnerConnectionProvisioned{}, err
	}
	now := s.now().UTC()
	org, connection, created, err := s.store.ProvisionPartnerConnection(Organization{ID: orgID, Name: name, UsageLevel: UsageLevelDefault, PartnerIntegrationID: integration.ID, ExternalAccountID: externalID, CreatedAt: now}, PartnerConnection{ID: connectionID, IntegrationID: integration.ID, ExternalAccountID: externalID, OrgID: orgID, Status: PartnerConnectionStatusActive, CreatedAt: now})
	if err != nil {
		return PartnerConnectionProvisioned{}, err
	}
	if connection.Status == PartnerConnectionStatusRevoked {
		if err := s.store.RestorePartnerConnection(connection.ID); err != nil {
			return PartnerConnectionProvisioned{}, err
		}
		connection.Status, connection.RevokedAt = PartnerConnectionStatusActive, nil
	}
	return PartnerConnectionProvisioned{Connection: connection, Organization: org, Created: created}, nil
}

func (s *PartnerService) RotateIntegrationSecret(ownerOrgID, id string) (PartnerIntegrationCreated, error) {
	integration, err := s.Integration(ownerOrgID, id)
	if err != nil {
		return PartnerIntegrationCreated{}, err
	}
	secret, err := s.randomID()
	if err != nil {
		return PartnerIntegrationCreated{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(secret), s.hashCost)
	if err != nil {
		return PartnerIntegrationCreated{}, err
	}
	integration.SecretHash, integration.UpdatedAt = hash, s.now().UTC()
	if err := s.store.UpdatePartnerIntegration(integration); err != nil {
		return PartnerIntegrationCreated{}, err
	}
	return PartnerIntegrationCreated{PartnerIntegration: integration, Secret: secret}, nil
}

func (s *PartnerService) DisableIntegration(ownerOrgID, id string) error {
	integration, err := s.Integration(ownerOrgID, id)
	if err != nil {
		return err
	}
	integration.Disabled, integration.UpdatedAt = true, s.now().UTC()
	return s.store.UpdatePartnerIntegration(integration)
}

func (s *PartnerService) Revoke(integrationID, secret, connectionID string) error {
	connection, err := s.store.PartnerConnection(connectionID)
	if err != nil {
		return err
	}
	if connection.IntegrationID != strings.TrimSpace(integrationID) {
		return ErrPartnerConnectionNotFound
	}
	if _, err := s.authenticate(integrationID, secret); err != nil {
		return err
	}
	now := s.now().UTC()
	if err := s.store.RevokePartnerConnection(connectionID, now); err != nil {
		return err
	}
	return s.store.RevokePartnerConnectionTokens(connectionID, now)
}
