package nanoflare

import (
	"strings"
	"testing"
	"time"
)

func TestControlAuthRefreshRotatesRefreshToken(t *testing.T) {
	store := NewStore()
	auth := NewControlAuthService(store, "test-secret")
	auth.hashCost = 4
	auth.randomID = sequentialIDs("user-123", "access-1", "refresh-1", "access-2", "refresh-2")
	auth.now = func() time.Time { return time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC) }

	session, err := auth.Signup(SignupInput{Email: "person@example.com", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if session.Token == "" || session.RefreshToken == "" || session.ExpiresIn == 0 {
		t.Fatalf("session = %#v", session)
	}

	refreshed, err := auth.Refresh(session.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Token == "" || refreshed.RefreshToken == "" || refreshed.RefreshToken == session.RefreshToken {
		t.Fatalf("refreshed session = %#v, original refresh = %q", refreshed, session.RefreshToken)
	}

	if _, err := auth.Refresh(session.RefreshToken); err == nil || !strings.Contains(err.Error(), "invalid refresh token") {
		t.Fatalf("reused refresh error = %v", err)
	}
}
