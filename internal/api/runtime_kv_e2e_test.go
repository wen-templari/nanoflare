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
	"strings"
	"testing"
	"time"

	"github.com/clas/platform/internal/api"
	"github.com/clas/platform/internal/config"
	"github.com/clas/platform/internal/platform"
)

func TestWorkerdNativeKVBindingEndToEnd(t *testing.T) {
	workerd, err := exec.LookPath("workerd")
	if err != nil {
		t.Skip("workerd is not installed")
	}
	store := platform.NewStore()
	app := platform.App{ID: "native-kv", Name: "Native KV", Hostname: "native.example.com", RuntimeToken: "runtime-secret", CreatedAt: time.Now().UTC()}
	if err := store.CreateApp(app); err != nil {
		t.Fatal(err)
	}
	service := platform.NewService(store, discardWriter{})
	runtimeServer := httptest.NewServer(api.NewRuntimeKVServer(service))
	defer runtimeServer.Close()

	port := availablePort(t)
	active := []platform.ActiveDeployment{{
		App: app,
		Deployment: platform.Deployment{
			ID:                "deployment",
			AppID:             app.ID,
			Files:             []platform.WorkerFile{{Path: "worker.js", Content: nativeKVWorker}},
			Entrypoint:        "worker.js",
			Format:            "modules",
			CompatibilityDate: "2025-12-10",
			Port:              port,
			CreatedAt:         time.Now().UTC(),
		},
	}}
	configPath := filepath.Join(t.TempDir(), "workerd.capnp")
	if err := os.WriteFile(configPath, []byte(config.WorkerdWithRuntimeAddr(active, strings.TrimPrefix(runtimeServer.URL, "http://"))), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.CommandContext(ctx, workerd, "serve", configPath)
	output, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = command.Wait()
	}()
	errorOutput := make(chan string, 1)
	go func() {
		value, _ := io.ReadAll(output)
		errorOutput <- string(value)
	}()

	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(url)
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("worker status = %d, body = %s", response.StatusCode, body)
			}
			if got, want := string(body), `{"text":"hello","json":{"ok":true},"missing":null}`; got != want {
				t.Fatalf("worker body = %s, want %s", got, want)
			}
			return
		}
		select {
		case value := <-errorOutput:
			t.Fatalf("workerd exited before becoming ready: %s", value)
		case <-time.After(25 * time.Millisecond):
		}
	}
	t.Fatal("workerd did not become ready")
}

func availablePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

type discardWriter struct{}

func (discardWriter) Write([]platform.ActiveDeployment) error {
	return nil
}

const nativeKVWorker = `export default {
  async fetch(request, env) {
    await env.KV.put("text", "hello");
    await env.KV.put("json", JSON.stringify({ ok: true }));
    const text = await env.KV.get("text");
    const json = await env.KV.get("json", "json");
    await env.KV.delete("text");
    const missing = await env.KV.get("text");
    return Response.json({ text, json, missing });
  },
};`
