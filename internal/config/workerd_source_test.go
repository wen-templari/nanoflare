package config

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkerdPreservesNonBreakingSpaceValues(t *testing.T) {
	binary := os.Getenv("NANOFLARE_TEST_WORKERD")
	if binary == "" {
		t.Skip("set NANOFLARE_TEST_WORKERD to pinned workerd")
	}
	// Test actual characters and escaped source independently; tagged templates
	// distinguish raw source text from cooked string values.
	const source = "export default {fetch(){return Response.json(['a\u00a0b', /\u00a0/.test('a\u00a0b'), String.raw`\u00a0`, String.raw`\\u00a0`, '\u200b', '\ufeff', '\u2028', '\u2029', '汉字'])}}"
	for _, date := range []string{"2025-12-10", "2026-09-11"} {
		t.Run(date, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "nf-doh-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(dir) })
			socket := filepath.Join(dir, "http.sock")
			config := fmt.Sprintf(`using Workerd = import "/workerd/workerd.capnp";
const config :Workerd.Config = (
 services=[(name="test",worker=(compatibilityDate=%s,modules=[(name="worker.js",esModule=%s)]))],
 sockets=[(name="http",address=%s,http=(),service="test")]);`, quote(date), capnpQuote(source), quote("unix:"+socket))
			file := filepath.Join(dir, "workerd.capnp")
			if err := os.WriteFile(file, []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			logFile, err := os.Create(filepath.Join(dir, "runtime.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer logFile.Close()
			command := exec.Command(binary, "serve", file)
			command.Stdout = logFile
			command.Stderr = logFile
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
			transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			}}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: time.Second}
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
				response, err := client.Get("http://worker.test/")
				if err != nil {
					continue
				}
				body, _ := io.ReadAll(response.Body)
				response.Body.Close()
				const want = "[\"a\u00a0b\",true,\"\u00a0\",\"\\\\u00a0\",\"\u200b\",\"\ufeff\",\"\u2028\",\"\u2029\",\"汉字\"]"
				if response.StatusCode != 200 || string(body) != want {
					t.Fatalf("source changed: %d %s, want %s", response.StatusCode, body, want)
				}
				return
			}
			logs, _ := os.ReadFile(logFile.Name())
			t.Fatalf("runtime did not start: %s", logs)
		})
	}
}
