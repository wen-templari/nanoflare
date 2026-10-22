package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCloudflareCompatibilityGatewayScope(t *testing.T) {
	calls := 0
	gateway := NewCloudflareCompatibilityGateway(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer test" || r.Header.Get("X-Nanoflare-Execution-Profile") != "trusted_shared_workerd" {
			t.Fatal("credentials or consent changed")
		}
		w.WriteHeader(204)
	}), "org")
	for _, tc := range []struct {
		url    string
		status int
	}{
		{"https://api.cloudflare.com/client/v4/accounts/org/workers/dispatch/namespaces", 204},
		{"http://api.cloudflare.com/client/v4/accounts/org/workers/dispatch/namespaces", 403},
		{"https://api.cloudflare.com.evil/client/v4/accounts/org/workers/dispatch/namespaces", 403},
		{"https://api.cloudflare.com/client/v4/accounts/other/workers/dispatch/namespaces", 403},
		{"https://api.cloudflare.com/client/v4/accounts/org/workers/%2e%2e/other", 403},
		{"https://api.cloudflare.com/client/v4/user", 403},
	} {
		req := httptest.NewRequest("POST", tc.url, nil)
		req.Header.Set("Authorization", "Bearer test")
		response := httptest.NewRecorder()
		gateway.ServeHTTP(response, req)
		if response.Code != tc.status {
			t.Fatalf("%s: %d", tc.url, response.Code)
		}
		if req.Header.Get("X-Nanoflare-Execution-Profile") != "" {
			t.Fatal("mutated caller headers")
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
