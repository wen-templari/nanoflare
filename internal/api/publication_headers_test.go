package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

func TestWorkerGatewayUsesOnlyTrustedRuntimeHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Nanoflare-Spoofed") != "" {
			t.Error("client supplied reserved header reached runtime")
		}
		if r.Header.Get("X-Nanoflare-Execution") != "trusted-execution" {
			t.Error("trusted execution identity not forwarded")
		}
		if r.Header.Get("X-Nanoflare-Runner-Token") != "trusted-runner" {
			t.Error("trusted runner credential not forwarded")
		}
		if r.Header.Get("Authorization") != "Bearer user" {
			t.Error("user authorization changed")
		}
		if r.Header.Get("X-Nanoflare-User-JWT") != "signed-assertion" || r.Header.Get("X-Nanoflare-User-Email") != "user@example.com" {
			t.Error("ForwardAuth identity headers lost")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	server := NewServer(nanoflare.NewService(nanoflare.NewStore(), nil))
	request := httptest.NewRequest(http.MethodGet, "http://worker.example.com/hello", nil)
	request.Header.Set("X-Nanoflare-Spoofed", "attacker")
	request.Header.Set("X-Nanoflare-Execution", "attacker")
	request.Header.Set("X-Nanoflare-Runner-Token", "attacker")
	request.Header.Set("Authorization", "Bearer user")
	request.Header.Set("X-Nanoflare-User-JWT", "signed-assertion")
	request.Header.Set("X-Nanoflare-User-Email", "user@example.com")
	response, err := server.workerResponse(request, runtime.EnsuredWorker{URL: upstream.URL, Token: "trusted-runner", Headers: http.Header{"X-Nanoflare-Execution": []string{"trusted-execution"}}}, "/hello", "")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if request.Header.Get("X-Nanoflare-Execution") != "attacker" {
		t.Fatal("gateway mutated original request")
	}
}
