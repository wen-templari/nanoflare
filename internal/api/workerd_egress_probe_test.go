package api_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/nanoflare"
)

// TestProbeWhatWorkerdSendsToEgressForConnect captures the exact first request
// line workerd sends to the globalOutbound egress service when a Worker calls
// connect() (raw TCP). This tells us whether the adapter could carry raw TCP
// (i.e. whether workerd issues an HTTP CONNECT to the proxy-style external).
func TestProbeWhatWorkerdSendsToEgressForConnect(t *testing.T) {
	workerd, err := exec.LookPath("workerd")
	if err != nil {
		t.Skip("workerd is not installed")
	}

	// Fake egress endpoint: accept a raw TCP conn and record the first line.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	firstLine := make(chan string, 4)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				firstLine <- line
			}(conn)
		}
	}()

	source := `import { connect } from "cloudflare:sockets";
export default {
  async fetch() {
    try {
      const s = connect({ hostname: "example.com", port: 1883 });
      const w = s.writable.getWriter();
      await w.write(new Uint8Array([1]));
      return new Response("wrote");
    } catch (e) {
      return new Response("ERR:" + ((e && e.message) || e), { status: 502 });
    }
  }
};`

	port := availablePort(t)
	active := []nanoflare.ActiveDeployment{{
		App: nanoflare.App{ID: "probe", Name: "Probe", Hostname: "probe.example.com", CreatedAt: time.Now().UTC()},
		Deployment: nanoflare.Deployment{
			ID: "deployment", AppID: "probe", Port: port, Entrypoint: "worker.js", Format: "modules", CompatibilityDate: "2025-12-10", CreatedAt: time.Now().UTC(),
			Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: source}},
		},
	}}
	configPath := filepath.Join(t.TempDir(), "workerd.capnp")
	if err := os.WriteFile(configPath, []byte(config.WorkerdWithOptions(active, config.WorkerdOptions{EgressAddr: listener.Addr().String()})), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.CommandContext(ctx, workerd, "serve", configPath)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = command.Wait() }()

	client := &net.Dialer{}
	for deadline := time.Now().Add(6 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		c, err := client.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
		c.Close()
		break
	}

	select {
	case line := <-firstLine:
		t.Logf("workerd -> egress first line for connect(): %q", line)
	case <-time.After(4 * time.Second):
		t.Logf("workerd sent nothing to egress for connect() within timeout")
	}
}
