package config

import (
	"context"
	"encoding/json"
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

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestWorkerdDoHRecordNormalizationAndImportableEnvFallback(t *testing.T) {
	binary := os.Getenv("NANOFLARE_TEST_WORKERD")
	if binary == "" {
		t.Skip("set NANOFLARE_TEST_WORKERD to pinned workerd")
	}
	for _, disallow := range []bool{false, true} {
		t.Run(fmt.Sprint(disallow), func(t *testing.T) {
			handler := func(w http.ResponseWriter, r *http.Request) {
				name := r.URL.Query().Get("name")
				code := 0
				kind := map[string]int{"TXT": 16, "CNAME": 5, "PTR": 12, "A": 1}[r.URL.Query().Get("type")]
				var answers []map[string]any
				switch name {
				case "text.invalid":
					answers = []map[string]any{{"name": name, "type": kind, "TTL": 30, "data": `"a""b""a"b\c"`}}
				case "alias.invalid":
					answers = []map[string]any{{"name": name, "type": kind, "TTL": 30, "data": "next.invalid."}}
				case "_http._tcp.example.invalid":
					answers = []map[string]any{{"name": name, "type": kind, "TTL": 30, "data": "instance.invalid."}}
				case "missing.invalid":
					code = 3
				case "noalias.invalid":
				case "public.invalid":
					answers = []map[string]any{{"name": name, "type": kind, "TTL": 30, "data": "192.0.2.1"}}
				}
				w.Header().Set("Content-Type", "application/dns-json")
				json.NewEncoder(w).Encode(map[string]any{"Status": code, "Question": []map[string]any{{"name": name, "type": kind}}, "Answer": answers})
			}
			dns := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if disallow {
					t.Error("missing importable env still used private binding")
				}
				handler(w, r)
			}))
			defer dns.Close()
			egress := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !disallow {
					t.Error("record query escaped to native egress")
				}
				handler(w, r)
			}))
			defer egress.Close()
			dir, err := os.MkdirTemp("", "nf-doh-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(dir) })
			socket := filepath.Join(dir, "http.sock")
			flags := []string{"nodejs_compat"}
			if disallow {
				flags = append(flags, "disallow_importable_env")
			}
			active := []nanoflare.ActiveDeployment{{App: nanoflare.App{ID: "test"}, SocketPath: socket, Deployment: nanoflare.Deployment{ID: "test-v1", AppID: "test", Entrypoint: "worker.js", Format: "modules", CompatibilityDate: "2026-09-11", CompatibilityFlags: flags, Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `import dns from 'node:dns/promises';
export default {async fetch(){
 const result={};
 for(const [name,run] of Object.entries({txt:()=>dns.resolveTxt('text.invalid'),cname:()=>dns.resolveCname('alias.invalid'),ptr:()=>dns.resolvePtr('_http._tcp.example.invalid'),nodata:()=>dns.resolveCname('noalias.invalid'),missing:()=>dns.resolveCname('missing.invalid'),public:()=>dns.lookup('public.invalid',{family:4})})) {
  try { result[name]=await run(); } catch(e) {result[name]=e.code;}
 }
 return Response.json(result);
}};`}}}}}
			source := WorkerdWithOptions(active, WorkerdOptions{DNSAddr: strings.TrimPrefix(dns.URL, "http://"), EgressAddr: strings.TrimPrefix(egress.URL, "http://")})
			// A plain external service stands in for native outbound transport so the
			// fallback test remains deterministic and does not contact public DNS.
			source = strings.ReplaceAll(source, "http = (style = proxy)", "http = ()")
			file := filepath.Join(dir, "workerd.capnp")
			if err := os.WriteFile(file, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			log, err := os.Create(filepath.Join(dir, "workerd.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			command := exec.Command(binary, "serve", file)
			command.Stderr = log
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { command.Process.Kill(); command.Wait() }()
			transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			}}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
				response, err := client.Get("http://test/")
				if err != nil {
					continue
				}
				body, _ := io.ReadAll(response.Body)
				response.Body.Close()
				// The pinned runtime maps a missing Answer to ENOTFOUND even for
				// a successful NODATA response; preserve this observed limit.
				const want = `{"txt":[["a","b","a\"b\\c"]],"cname":["next.invalid"],"ptr":["instance.invalid"],"nodata":"ENOTFOUND","missing":"ENOTFOUND","public":{"address":"192.0.2.1","family":4}}`
				if response.StatusCode != 200 || string(body) != want {
					t.Fatalf("response=%d %s, want %s", response.StatusCode, body, want)
				}
				return
			}
			logs, _ := os.ReadFile(log.Name())
			t.Fatalf("workerd did not start: %s", logs)
		})
	}
}
