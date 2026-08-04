package api_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/egress"
	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestWorkerdGlobalOutboundUsesConfiguredEgressProxy(t *testing.T) {
	workerd, err := exec.LookPath("workerd")
	if err != nil {
		t.Skip("workerd is not installed")
	}
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "internal")
	}))
	defer internal.Close()
	_, internalPort, err := net.SplitHostPort(internal.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	internalURL := "http://localhost:" + internalPort + "/from-worker"
	proxyRequests := make(chan *http.Request, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		proxyRequests <- request.Clone(request.Context())
		_, _ = io.WriteString(w, "external")
	}))
	defer proxy.Close()
	adapter, err := egress.New(egress.Config{ProxyURL: proxy.URL, NoProxy: []string{"localhost"}, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = adapter.Close(ctx)
	}()

	port := availablePort(t)
	active := []nanoflare.ActiveDeployment{{
		App: nanoflare.App{ID: "egress", Name: "Egress", Hostname: "egress.example.com", CreatedAt: time.Now().UTC()},
		Deployment: nanoflare.Deployment{
			ID: "deployment", AppID: "egress", Port: port, Entrypoint: "worker.js", Format: "modules", CompatibilityDate: "2025-12-10", CreatedAt: time.Now().UTC(),
			Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: fmt.Sprintf(`export default { async fetch() { const external = await fetch(%q); const internal = await fetch(%q); return new Response(await external.text() + "|" + await internal.text()); } };`, "http://public.example/from-worker", internalURL)}},
		},
	}}
	configPath := filepath.Join(t.TempDir(), "workerd.capnp")
	if err := os.WriteFile(configPath, []byte(config.WorkerdWithOptions(active, config.WorkerdOptions{EgressAddr: adapter.Addr()})), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.CommandContext(ctx, workerd, "serve", configPath)
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = command.Wait() }()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || string(body) != "external|internal" {
			t.Fatalf("worker response = %d %q", response.StatusCode, body)
		}
		select {
		case request := <-proxyRequests:
			if got, want := request.URL.String(), "http://public.example/from-worker"; got != want {
				t.Fatalf("proxy URL = %q, want %q", got, want)
			}
			if got, want := request.Host, request.URL.Host; got != want {
				t.Fatalf("proxy Host = %q, want %q", got, want)
			}
			return
		case <-time.After(time.Second):
			t.Fatal("configured proxy received no Worker request")
		}
	}
	output, _ := io.ReadAll(stderr)
	t.Fatalf("workerd did not become ready: %s", output)
}
