package api_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

type socketEnsurer struct {
	path     string
	released chan struct{}
}

func (s socketEnsurer) Ensure(context.Context, nanoflare.ActiveDeployment) (runtime.EnsuredWorker, error) {
	return runtime.EnsuredWorker{SocketPath: s.path, Release: func() { close(s.released) }}, nil
}

func TestSocketGatewayPreservesUpgradeAndLease(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "nf-upgrade-")
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
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		buffered.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		buffered.Flush()
		data := make([]byte, 4)
		if _, err := io.ReadFull(buffered, data); err != nil {
			t.Error(err)
			return
		}
		conn.Write(data)
	})}
	go backend.Serve(listener)
	defer backend.Close()
	service := nanoflare.NewService(nanoflare.NewStore(), discardWriter{})
	app, err := service.CreateApp(nanoflare.CreateAppInput{Name: "Upgrade", Hostname: "upgrade.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Deploy(app.ID, nanoflare.DeployInput{Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { fetch() {} }`}}, Entrypoint: "worker.js", Format: "modules", CompatibilityDate: "2025-12-10"})
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	gateway := httptest.NewServer(api.NewServerWithRuntime(service, nil, "", nil, nil, socketEnsurer{path: path, released: released}))
	defer gateway.Close()
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(gateway.URL, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(conn, "GET /internal/http/workers/"+app.ID+"/ws HTTP/1.1\r\nHost: upgrade.example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	buffered := bufio.NewReader(conn)
	response, err := http.ReadResponse(buffered, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade status = %d", response.StatusCode)
	}
	select {
	case <-released:
		t.Fatal("lease released during upgrade")
	default:
	}
	conn.Write([]byte("ping"))
	data := make([]byte, 4)
	if _, err := io.ReadFull(buffered, data); err != nil || string(data) != "ping" {
		t.Fatalf("upgrade echo = %q: %v", data, err)
	}
	conn.Close()
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("upgrade lease was not released")
	}
}
