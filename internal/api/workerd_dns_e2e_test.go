package api_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/dnsresolver"
	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestWorkerdNodeDNSLookupUsesHostResolver(t *testing.T) {
	workerd, err := exec.LookPath("workerd")
	if err != nil {
		t.Skip("workerd is not installed")
	}
	resolver, err := dnsresolver.New(dnsresolver.Config{}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.Start(); err != nil {
		t.Fatal(err)
	}
	defer resolver.Close(context.Background())

	port := availablePort(t)
	active := []nanoflare.ActiveDeployment{{
		App: nanoflare.App{ID: "dns", RuntimeToken: "secret"},
		Deployment: nanoflare.Deployment{
			ID: "deployment", AppID: "dns", Port: port, Entrypoint: "worker.js", Format: "modules", CompatibilityDate: "2025-12-10",
			CompatibilityFlags: []string{"nodejs_compat"},
			Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `import dns from "node:dns";
export default { async fetch() {
  const promise = await dns.promises.lookup("localhost", { all: true, family: 4 });
  const callback = await new Promise((resolve, reject) => dns.lookup("localhost", { all: true, family: 4 }, (error, addresses) => error ? reject(error) : resolve(addresses)));
  return Response.json({ promise, callback });
} };`}},
		},
	}}
	configPath := filepath.Join(t.TempDir(), "workerd.capnp")
	generated := config.WorkerdWithOptions(active, config.WorkerdOptions{DNSAddr: resolver.Addr()})
	if !strings.Contains(generated, `nanoflare-internal:dns`) || !strings.Contains(generated, `X-Nanoflare-DNS-Profile`) {
		t.Fatalf("generated config does not contain DNS adapter: %s", generated)
	}
	if err := os.WriteFile(configPath, []byte(generated), 0o600); err != nil {
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
		if response.StatusCode == http.StatusOK && strings.Contains(string(body), `"promise":[`) && strings.Contains(string(body), `"callback":[`) && strings.Count(string(body), `"family":4`) >= 2 {
			return
		}
		t.Fatalf("worker response = %d %q", response.StatusCode, body)
	}
	output, _ := io.ReadAll(stderr)
	t.Fatalf("workerd did not become ready: %s", output)
}
