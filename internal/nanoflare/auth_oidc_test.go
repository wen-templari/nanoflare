package nanoflare

import (
	"errors"
	"testing"
)

func TestControlAuthLoginOIDCCreatesUserWithoutOrganization(t *testing.T) {
	store := NewStore()
	auth := NewControlAuthService(store, "test-secret")
	auth.randomID = sequentialIDs("user-123")

	session, err := auth.LoginOIDC(OIDCLoginInput{
		Issuer:  "https://issuer.example.com",
		Subject: "subject-123",
		Email:   "Person@Example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.Token == "" || session.User.Email != "person@example.com" {
		t.Fatalf("session = %#v", session)
	}
	if len(session.Organizations) != 0 || session.ActiveOrgID != "" {
		t.Fatalf("organizations = %#v active=%q", session.Organizations, session.ActiveOrgID)
	}
	if _, err := auth.Login(LoginInput{Email: "person@example.com", Password: "secret"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("password login error = %v", err)
	}
}

func TestControlAuthLoginOIDCLinksExistingEmailUser(t *testing.T) {
	store := NewStore()
	auth := NewControlAuthService(store, "test-secret")
	auth.hashCost = 4
	auth.randomID = sequentialIDs("user-123")
	created, err := auth.Signup(SignupInput{Email: "person@example.com", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	session, err := auth.LoginOIDC(OIDCLoginInput{
		Issuer:  "https://issuer.example.com",
		Subject: "subject-123",
		Email:   "PERSON@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.User.ID != created.User.ID {
		t.Fatalf("linked user = %q, want %q", session.User.ID, created.User.ID)
	}
}

func TestControlAuthLoginOIDCRepeatUsesIdentity(t *testing.T) {
	store := NewStore()
	auth := NewControlAuthService(store, "test-secret")
	auth.randomID = sequentialIDs("user-123")
	first, err := auth.LoginOIDC(OIDCLoginInput{
		Issuer:  "https://issuer.example.com/",
		Subject: "subject-123",
		Email:   "person@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	second, err := auth.LoginOIDC(OIDCLoginInput{
		Issuer:  "https://issuer.example.com",
		Subject: "subject-123",
		Email:   "changed@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.User.ID != first.User.ID || second.User.Email != "person@example.com" {
		t.Fatalf("second session = %#v first = %#v", second, first)
	}
}

func sequentialIDs(values ...string) func() (string, error) {
	index := 0
	return func() (string, error) {
		if index >= len(values) {
			return values[len(values)-1], nil
		}
		value := values[index]
		index++
		return value, nil
	}
}
