package api

import (
	"github.com/clas/nanoflare/internal/nanoflare"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRuntimeAccessRequiresUserAndMatchingOrganization(t *testing.T) {
	repo := nanoflare.NewStore()
	auth := nanoflare.NewControlAuthService(repo, "identity-test-key")
	auth.SetTestHashCost(4)
	member, err := auth.Signup(nanoflare.SignupInput{Email: "member@example.com", Password: "password", OrganizationName: "Member"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := auth.Signup(nanoflare.SignupInput{Email: "foreign@example.com", Password: "password", OrganizationName: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	org, err := auth.CreateOrganization(member.User.ID, nanoflare.CreateOrganizationInput{Name: "Member"})
	if err != nil {
		t.Fatal(err)
	}
	app := nanoflare.App{ID: "app", OrgID: org.ID, RuntimeToken: "root-key"}
	if err = repo.CreateApp(app); err != nil {
		t.Fatal(err)
	}
	dep := nanoflare.Deployment{ID: "revision", AppID: app.ID}
	if err = repo.Activate(dep); err != nil {
		t.Fatal(err)
	}
	service := nanoflare.NewService(repo, discardWriter{})
	server := NewRuntimeAccessServer(service, auth)
	capability := nanoflare.RuntimeCapability(nanoflare.ActiveDeployment{App: app, Deployment: dep}, nanoflare.RuntimeAccess, "")
	for _, tc := range []struct {
		name, user, capability string
		status                 int
	}{
		{"member", member.Token, capability, 200}, {"foreign", foreign.Token, capability, 403},
		{"unsigned", "member@example.com", capability, 403}, {"missing", "", capability, 403},
		{"no runtime authority", member.Token, "bad", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "/internal/runtime/access", nil)
			request.Header.Set("Authorization", "Bearer "+tc.capability)
			if tc.user != "" {
				request.Header.Set("X-Nanoflare-User-JWT", tc.user)
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.status == 200 && !strings.Contains(response.Body.String(), "member@example.com") {
				t.Fatal(response.Body.String())
			}
		})
	}
}
