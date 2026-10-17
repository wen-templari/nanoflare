package runner

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

type socketManager struct {
	*fakeManager
	headers  http.Header
	path     string
	released atomic.Int32
}

func (m *socketManager) Ensure(_ context.Context, active nanoflare.ActiveDeployment) (runtime.EnsuredWorker, error) {
	return runtime.EnsuredWorker{SocketPath: m.path, Headers: m.headers, Release: func() { m.released.Add(1) }}, nil
}

func TestRunnerForwardsToPrivateSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "nf-runner-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "worker.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	backend := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Nanoflare-Runner-Token") != "" {
			t.Error("runner token leaked to Worker")
		}
		if r.Header.Get("X-Nanoflare-Spoofed") != "" {
			t.Error("untrusted reserved header forwarded")
		}
		if r.Header.Get("X-Nanoflare-Execution") != "trusted-execution" {
			t.Error("runtime execution header not applied")
		}
		io.WriteString(w, r.Host+"|"+r.URL.EscapedPath()+"|"+r.URL.RawQuery+"|"+r.Header.Get("Authorization"))
	})}
	go backend.Serve(listener)
	defer backend.Close()
	manager := &socketManager{fakeManager: &fakeManager{}, path: path, headers: http.Header{"X-Nanoflare-Execution": []string{"trusted-execution"}}}
	server := httptest.NewServer(NewServer(manager, "secret"))
	defer server.Close()
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/workers/app/deployment/@scope%2Fpackage?q=1", nil)
	request.Host = "worker.example.com"
	request.Header.Set("Authorization", "Bearer user-token")
	request.Header.Set("X-Nanoflare-Spoofed", "spoofed")
	request.Header.Set("X-Nanoflare-Execution", "attacker-execution")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", response.StatusCode)
	}
	request.Header.Set("X-Nanoflare-Runner-Token", "secret")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != "worker.example.com|/@scope%2Fpackage|q=1|Bearer user-token" {
		t.Fatalf("response = %q: %v", body, err)
	}
	if manager.released.Load() != 1 {
		t.Fatal("request lease was not released")
	}
}
