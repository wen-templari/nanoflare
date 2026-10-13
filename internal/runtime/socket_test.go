package runtime

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestSharedSocketPoolDrainsRequests(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "nf-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	launcher := &fakeLauncher{healthy: true}
	m := NewManager(&fakeWriter{}, launcher, t.TempDir(), filepath.Join(t.TempDir(), "workerd.capnp"), testSocketDir(t), time.Second, time.Second)
	defer m.Close()
	items := deployments(9001)
	second := items[0]
	second.App.ID = "second"
	second.Deployment.ID = "second"
	items = append(items, second)
	if err := m.Write(items); err != nil {
		t.Fatal(err)
	}
	first, err := m.Ensure(context.Background(), items[0])
	if err != nil {
		t.Fatal(err)
	}
	other, err := m.Ensure(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	other.Release()
	if launcher.count() != 1 || first.SocketPath == other.SocketPath {
		t.Fatal("Workers must share a process with distinct Unix endpoints")
	}
	if err := m.Write(items); err != nil {
		t.Fatal(err)
	}
	if launcher.process(0).isStopped() {
		t.Fatal("stopped a generation with an active request")
	}
	next, err := m.Ensure(context.Background(), items[0])
	if err != nil {
		t.Fatal(err)
	}
	next.Release()
	if next.SocketPath == first.SocketPath {
		t.Fatal("replacement reused a live socket")
	}
	first.Release()
	first.Release()
	m.retiring.Wait()
	if !launcher.process(0).isStopped() {
		t.Fatal("released generation was not stopped")
	}
	if _, err := os.Stat(first.SocketPath); !os.IsNotExist(err) {
		t.Fatalf("old socket remains: %v", err)
	}
}

func TestManagerRequiresSocketDirectory(t *testing.T) {
	launcher := &fakeLauncher{healthy: true}
	dir := t.TempDir()
	manager := NewManager(&fakeWriter{}, launcher, dir, filepath.Join(dir, "workerd.capnp"), "", time.Second, time.Second)
	defer manager.Close()
	if err := manager.Write(deployments(9001)); err == nil {
		t.Fatal("missing socket directory silently accepted a legacy deployment port")
	}
	if launcher.count() != 0 {
		t.Fatal("process launched without a socket directory")
	}
}

func TestUnixTransportKeepsWorkerConnectionsSeparate(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "nf-http-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	ConfigureTransport(transport)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	for _, name := range []string{"first", "second"} {
		path := filepath.Join(dir, name+".sock")
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, name) })}
		go server.Serve(listener)
		defer server.Close()
		response, err := client.Get("http://" + SocketHost(path) + "/")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || string(body) != name {
			t.Fatalf("response = %q, %v", body, err)
		}
	}
}

func TestRealWorkerdSharedUnixSockets(t *testing.T) {
	executable, err := exec.LookPath("workerd")
	if err != nil {
		t.Skip("workerd unavailable")
	}
	dir, err := os.MkdirTemp("/tmp", "nf-real-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	writer := config.NewRuntimeWriter(filepath.Join(dir, "canonical.capnp"), &fakeWriter{})
	m := NewManager(writer, CommandLauncher{Executable: executable}, dir, filepath.Join(dir, "canonical.capnp"), testSocketDir(t), 10*time.Second, time.Second)
	defer m.Close()
	items := deployments(0)
	items[0].Deployment.CompatibilityDate = "2025-01-01"
	items[0].Deployment.Format = "modules"
	items[0].Deployment.Files = []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { fetch() { return new Response("first"); } }`}}
	second := items[0]
	second.App.ID = "second"
	second.Deployment.ID = "second"
	second.Deployment.Files = []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { fetch() { return new Response("second"); } }`}}
	items = append(items, second)
	if err := m.Write(items); err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	ConfigureTransport(transport)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	for i, item := range items {
		endpoint, err := m.Ensure(context.Background(), item)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Get("http://" + SocketHost(endpoint.SocketPath) + "/")
		if err != nil {
			endpoint.Release()
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		endpoint.Release()
		want := []string{"first", "second"}[i]
		if err != nil || string(body) != want {
			t.Fatalf("response = %q, want %s: %v", body, want, err)
		}
	}
}
