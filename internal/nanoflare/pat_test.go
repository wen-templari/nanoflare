package nanoflare

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPersonalAccessTokenLifecycle(t *testing.T) {
	store := NewStore()
	auth := NewControlAuthService(store, "test-secret")
	auth.randomID = sequentialIDs("user-123", "access-1", "refresh-1", "org-123", "pat-123", "plain-pat")
	auth.hashCost = 4
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	auth.now = func() time.Time { return now }

	session, err := auth.Signup(SignupInput{Email: "person@example.com", Password: "secret", OrganizationName: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	org, err := auth.CreateOrganization(session.User.ID, CreateOrganizationInput{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}

	created, err := auth.CreatePersonalAccessToken(session.User.ID, CreatePersonalAccessTokenInput{
		Name:      "Deploy key",
		ScopeType: PATScopeTypeOrg,
		OrgID:     org.ID,
		Scopes:    []string{"workers:read", "deployments:write"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Token == "" || created.TokenHash == "" || strings.Contains(created.TokenHash, created.Token) {
		t.Fatalf("created token = %#v", created)
	}

	access, err := auth.ValidatePersonalAccessToken(created.Token, "ignored-org")
	if err != nil {
		t.Fatal(err)
	}
	if access.OrgID != org.ID || !HasScope(access.Scopes, "deployments:write") {
		t.Fatalf("access = %#v", access)
	}

	tokens, err := auth.PersonalAccessTokens(session.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].LastUsedAt == nil {
		t.Fatalf("tokens = %#v", tokens)
	}

	if err := auth.RevokePersonalAccessToken(session.User.ID, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.ValidatePersonalAccessToken(created.Token, org.ID); err == nil || !strings.Contains(err.Error(), "invalid token") {
		t.Fatalf("revoked token error = %v", err)
	}
}

func TestPersonalAccessTokenRejectsExcessScopes(t *testing.T) {
	store := NewStore()
	auth := NewControlAuthService(store, "test-secret")
	auth.randomID = sequentialIDs("user-123", "access-1", "refresh-1", "org-123")
	auth.hashCost = 4

	session, err := auth.Signup(SignupInput{Email: "person@example.com", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	org, err := auth.CreateOrganization(session.User.ID, CreateOrganizationInput{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertOrganizationMembership(OrganizationMembership{UserID: session.User.ID, OrgID: org.ID, Role: RoleViewer, Scopes: RoleScopes(RoleViewer), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	_, err = auth.CreatePersonalAccessToken(session.User.ID, CreatePersonalAccessTokenInput{
		Name:      "Too broad",
		ScopeType: PATScopeTypeOrg,
		OrgID:     org.ID,
		Scopes:    []string{"workers:write"},
	})
	if err == nil || !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("error = %v", err)
	}
}

func TestPersonalAccessTokenUserScopeUsesLiveMembership(t *testing.T) {
	store := NewStore()
	auth := NewControlAuthService(store, "test-secret")
	auth.randomID = sequentialIDs("user-123", "access-1", "refresh-1", "org-123", "pat-123", "plain-pat")
	auth.hashCost = 4

	session, err := auth.Signup(SignupInput{Email: "person@example.com", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	org, err := auth.CreateOrganization(session.User.ID, CreateOrganizationInput{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := auth.CreatePersonalAccessToken(session.User.ID, CreatePersonalAccessTokenInput{Name: "Multi org", ScopeType: PATScopeTypeUser, Scopes: []string{"workers:read", "workers:write"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertOrganizationMembership(OrganizationMembership{UserID: session.User.ID, OrgID: org.ID, Role: RoleViewer, Scopes: RoleScopes(RoleViewer), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	access, err := auth.ValidatePersonalAccessToken(created.Token, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	if HasScope(access.Scopes, "workers:write") || !HasScope(access.Scopes, "workers:read") {
		t.Fatalf("access scopes = %#v", access.Scopes)
	}
	if _, err := auth.ValidatePersonalAccessToken(created.Token, ""); err == nil || !errors.Is(err, ErrMembershipNotFound) && !strings.Contains(err.Error(), "X-Nanoflare-Org-ID") {
		t.Fatalf("missing org error = %v", err)
	}
}
