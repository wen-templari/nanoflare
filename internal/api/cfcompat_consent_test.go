package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestCloudflareCompatibilityAdministratorConsent(t *testing.T) {
	repo := nanoflare.NewStore()
	auth := nanoflare.NewControlAuthService(repo, "consent-test")
	auth.SetTestHashCost(4)
	session, err := auth.Signup(nanoflare.SignupInput{Email: "consent@example.com", Password: "password"})
	if err != nil {
		t.Fatal(err)
	}
	org, err := auth.CreateOrganization(session.User.ID, nanoflare.CreateOrganizationInput{Name: "consent"})
	if err != nil {
		t.Fatal(err)
	}
	service := nanoflare.NewService(repo, discardWriter{})
	handler := NewServerWithControlAuth(service, nil, "", nil, auth)
	for _, tc := range []struct {
		name     string
		dispatch bool
		adapter  bool
		profile  string
		status   int
	}{
		{"disabled", false, false, "", 404},
		{"legacy-header-cannot-enable-adapter", true, false, "trusted_shared_workerd", 404},
		{"legacy-header-cannot-enable-dispatch", false, true, "trusted_shared_workerd", 404},
		{"administrator-consent", true, true, "", 200},
		{"legacy-header-accepted", true, true, "trusted_shared_workerd", 200},
		{"header-cannot-select-profile", true, true, "untrusted", 200},
		{"administrator-revoked-consent", false, true, "trusted_shared_workerd", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service.SetPlatformDispatchEnabled(tc.dispatch)
			handler.SetCloudflareCompatibilityEnabled(tc.adapter)
			r := httptest.NewRequest("POST", "/client/v4/accounts/"+org.ID+"/workers/dispatch/namespaces", strings.NewReader(`{"name":"`+tc.name+`"}`))
			r.Header.Set("Authorization", "Bearer "+session.Token)
			r.Header.Set("Content-Type", "application/json")
			if tc.profile != "" {
				r.Header.Set("X-Nanoflare-Execution-Profile", tc.profile)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if tc.status == 200 && w.Header().Get("X-Nanoflare-Execution-Profile") != "trusted_shared_workerd" {
				t.Fatal("response did not report the administrator's execution profile")
			}
		})
	}
	namespaces, err := service.DispatchNamespacesForOrg(org.ID)
	if err != nil || len(namespaces) != 3 {
		t.Fatalf("disabled requests changed state: namespaces=%v err=%v", namespaces, err)
	}
}
