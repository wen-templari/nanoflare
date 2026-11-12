package nanoflare

import (
	"strings"
	"testing"
	"time"
)

func TestWorkerAccessTokenIsolationAndExpiry(t *testing.T) {
	auth := NewControlAuthService(NewStore(), "shared-secret")
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	auth.now = func() time.Time { return now }
	app := App{ID: "worker", OrgID: "org", Hostname: "app.example.com"}
	access, err := auth.IssueWorkerAccessToken(app, "agent:connection", WorkerAccessRequest{Method: "POST", Path: "/api/a%2Fb?x=1"})
	if err != nil {
		t.Fatal(err)
	}
	// A second replica with the same configured key can verify the token.
	replica := NewControlAuthService(NewStore(), "shared-secret")
	replica.now = auth.now
	claims, err := replica.ValidateWorkerAccessToken(access.Token)
	if err != nil || claims.WorkerID != app.ID || claims.OrgID != app.OrgID || claims.Path != "/api/a%2Fb?x=1" || claims.Actor != "agent:connection" || claims.Expires != now.Add(5*time.Minute).Unix() {
		t.Fatalf("claims=%+v err=%v", claims, err)
	}
	if _, err := auth.ValidateToken(access.Token); err == nil {
		t.Fatal("worker credential accepted as control session")
	}
	if _, err := NewControlAuthService(NewStore(), "other-secret").ValidateWorkerAccessToken(access.Token); err == nil {
		t.Fatal("accepted wrong signing key")
	}
	for _, token := range []string{"", "nfwa_bad.bad", access.Token + "tamper", strings.TrimPrefix(access.Token, "nfwa_")} {
		if _, err := auth.ValidateWorkerAccessToken(token); err == nil {
			t.Fatalf("accepted invalid token %q", token)
		}
	}
	now = access.ExpiresAt
	if _, err := auth.ValidateWorkerAccessToken(access.Token); err == nil {
		t.Fatal("accepted expired token")
	}
}

func TestWorkerAccessRequestRejectsExternalTargets(t *testing.T) {
	for _, path := range []string{"", "api/check", "https://other.example/check", "//other.example/check", "/check#fragment", "/check\r\nInjected: yes", "/a\\b", "/%zz"} {
		if err := ValidateWorkerAccessRequest(WorkerAccessRequest{Method: "GET", Path: path}); err == nil {
			t.Fatalf("accepted path %q", path)
		}
	}
	for _, method := range []string{"", "CONNECT", "TRACE", "GET\r\n"} {
		if err := ValidateWorkerAccessRequest(WorkerAccessRequest{Method: method, Path: "/"}); err == nil {
			t.Fatalf("accepted method %q", method)
		}
	}
}
