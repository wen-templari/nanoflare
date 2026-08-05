package api_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/pem"
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

// startWorker writes the generated config, launches workerd, waits for readiness,
// and returns the body of GET / plus its status. A per-request timeout keeps a
// hung Worker from blocking the whole test.
func startWorker(t *testing.T, workerd, source string, opts config.WorkerdOptions, requestTimeout time.Duration) (int, string) {
	t.Helper()
	port := availablePort(t)
	active := []nanoflare.ActiveDeployment{{
		App: nanoflare.App{ID: "repro", Name: "Repro", Hostname: "repro.example.com", CreatedAt: time.Now().UTC()},
		Deployment: nanoflare.Deployment{
			ID: "deployment", AppID: "repro", Port: port, Entrypoint: "worker.js", Format: "modules", CompatibilityDate: "2025-12-10", CreatedAt: time.Now().UTC(),
			Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: source}},
		},
	}}
	configPath := filepath.Join(t.TempDir(), "workerd.capnp")
	if err := os.WriteFile(configPath, []byte(config.WorkerdWithOptions(active, opts)), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, workerd, "serve", configPath)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = command.Wait() })

	client := &http.Client{Timeout: requestTimeout}
	var lastErr error
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		return response.StatusCode, string(body)
	}
	t.Fatalf("worker never responded (last error: %v): %s", lastErr, stderr.String())
	return 0, ""
}

// startEchoServer accepts one 4-byte payload per connection and replies "pong".
func startEchoServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 4)
				if _, err := io.ReadFull(conn, buf); err != nil {
					return
				}
				_, _ = conn.Write([]byte("pong"))
			}()
		}
	}()
	return listener.Addr().String()
}

// startConnectProxy is a minimal HTTP CONNECT proxy used to exercise the
// adapter's non-NO_PROXY tunnel path.
func startConnectProxy(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(client net.Conn) {
				defer client.Close()
				reader := bufio.NewReader(client)
				request, err := http.ReadRequest(reader)
				if err != nil || request.Method != http.MethodConnect {
					_, _ = io.WriteString(client, "HTTP/1.1 400 Bad Request\r\n\r\n")
					return
				}
				upstream, err := net.Dial("tcp", request.Host)
				if err != nil {
					_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
					return
				}
				defer upstream.Close()
				_, _ = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n")
				go func() { _, _ = io.Copy(upstream, reader); _ = upstream.Close() }()
				_, _ = io.Copy(client, upstream)
			}(conn)
		}
	}()
	return listener.Addr().String()
}

func rawTCPWorkerSource(t *testing.T, echoAddr string) string {
	t.Helper()
	host, port, err := net.SplitHostPort(echoAddr)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`import { connect } from "cloudflare:sockets";
export default {
  async fetch() {
    try {
      const socket = connect({ hostname: %q, port: %s });
      const writer = socket.writable.getWriter();
      await writer.write(new Uint8Array([112, 105, 110, 103]));
      const reader = socket.readable.getReader();
      const { value } = await reader.read();
      try { await socket.close(); } catch (e) {}
      return new Response("OK:" + new TextDecoder().decode(value));
    } catch (e) {
      return new Response("ERR:" + ((e && e.message) || e), { status: 502 });
    }
  }
};`, host, port)
}

// Regression: enabling egress must not break a Worker's raw TCP connect()
// (MQTT/PostgreSQL on the real deployment). NO_PROXY targets are dialed
// directly by the adapter; others tunnel through the corporate proxy.
func TestEgressRawTCPConnectStillWorks(t *testing.T) {
	workerd, err := exec.LookPath("workerd")
	if err != nil {
		t.Skip("workerd is not installed")
	}
	echoAddr := startEchoServer(t)
	source := rawTCPWorkerSource(t, echoAddr)
	allow := []string{"public", "127.0.0.0/8"}

	// Baseline: egress OFF -> connect() reaches the echo server.
	status, body := startWorker(t, workerd, source, config.WorkerdOptions{NetworkAllow: allow}, 5*time.Second)
	t.Logf("egress OFF:            status=%d body=%q", status, body)
	if status != http.StatusOK || body != "OK:pong" {
		t.Fatalf("baseline (egress off) failed: status=%d body=%q", status, body)
	}

	// Egress ON, target in NO_PROXY -> adapter dials the target directly.
	direct, err := egress.New(egress.Config{ProxyURL: "http://127.0.0.1:9", NoProxy: []string{"127.0.0.1"}, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := direct.Start(); err != nil {
		t.Fatal(err)
	}
	defer closeAdapter(t, direct)
	status, body = startWorker(t, workerd, source, config.WorkerdOptions{NetworkAllow: allow, EgressAddr: direct.Addr()}, 5*time.Second)
	t.Logf("egress ON (no_proxy):  status=%d body=%q", status, body)
	if status != http.StatusOK || body != "OK:pong" {
		t.Fatalf("raw TCP via NO_PROXY direct dial failed: status=%d body=%q", status, body)
	}

	// Egress ON, target NOT in NO_PROXY -> adapter tunnels via the proxy.
	proxyAddr := startConnectProxy(t)
	viaProxy, err := egress.New(egress.Config{ProxyURL: "http://" + proxyAddr, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := viaProxy.Start(); err != nil {
		t.Fatal(err)
	}
	defer closeAdapter(t, viaProxy)
	status, body = startWorker(t, workerd, source, config.WorkerdOptions{NetworkAllow: allow, EgressAddr: viaProxy.Addr()}, 5*time.Second)
	t.Logf("egress ON (via proxy): status=%d body=%q", status, body)
	if status != http.StatusOK || body != "OK:pong" {
		t.Fatalf("raw TCP via proxy CONNECT failed: status=%d body=%q", status, body)
	}
}

func closeAdapter(t *testing.T, adapter *egress.Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = adapter.Close(ctx)
}

// Repro 2: a global fetch() to an HTTP/2 origin whose body has no Content-Length
// is relayed by the adapter over a keep-alive HTTP/1.1 connection to workerd
// without a length delimiter, so the Worker fetch never completes.
func TestReproEgressHangsOnHTTP2Upstream(t *testing.T) {
	workerd, err := exec.LookPath("workerd")
	if err != nil {
		t.Skip("workerd is not installed")
	}

	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "hello ")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush() // stream => no Content-Length on the wire
		}
		_, _ = io.WriteString(w, "world")
	}))
	upstream.EnableHTTP2 = true
	upstream.TLS = &tls.Config{}
	upstream.StartTLS()
	defer upstream.Close()

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: upstream.Certificate().Raw})
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	host, _, err := net.SplitHostPort(upstream.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	adapter, err := egress.New(egress.Config{ProxyURL: "http://127.0.0.1:9", CAFiles: []string{caFile}, NoProxy: []string{host}, Addr: "127.0.0.1:0"})
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

	source := fmt.Sprintf(`export default {
  async fetch() {
    const r = await fetch(%q);
    return new Response("BODY:" + (await r.text()));
  }
};`, upstream.URL+"/")

	status, body := startWorker(t, workerd, source, config.WorkerdOptions{EgressAddr: adapter.Addr()}, 4*time.Second)
	t.Logf("http2 upstream via egress: status=%d body=%q", status, body)
	if body != "BODY:hello world" {
		t.Fatalf("REPRODUCED: Worker fetch through egress did not complete for HTTP/2 upstream: status=%d body=%q", status, body)
	}
}
