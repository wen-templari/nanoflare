//go:build wfppoc && (darwin || linux)

package wfppoc

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/egress"
	"github.com/clas/nanoflare/internal/nanoflare"
	nfruntime "github.com/clas/nanoflare/internal/runtime"
)

var executable = flag.String("workerd", "workerd", "workerd executable to probe")
var reportPath = flag.String("report", "", "optional JSON evidence output (relative to this package)")
var observations []observation
var version string

type observation struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Evidence any    `json:"evidence"`
}

func TestMain(m *testing.M) {
	flag.Parse()
	output, err := exec.Command(*executable, "--version").CombinedOutput()
	if err != nil {
		fmt.Fprintln(os.Stderr, "PoC requires workerd:", err)
		os.Exit(1)
	}
	version = strings.TrimSpace(string(output))
	code := m.Run()
	if *reportPath != "" {
		hashes := map[string]string{}
		for _, name := range []string{"dispatcher.js", "namespace.js", "customer.js", "poc_test.go"} {
			contents, readErr := os.ReadFile(name)
			if readErr != nil {
				fmt.Fprintln(os.Stderr, readErr)
				code = 1
				continue
			}
			hash := sha256.Sum256(contents)
			hashes[name] = hex.EncodeToString(hash[:])
		}
		data, err := json.MarshalIndent(map[string]any{
			"runtime": version, "host": runtime.GOOS + "/" + runtime.GOARCH,
			"fixture_sha256": hashes, "tested_at_utc": time.Now().UTC().Format(time.RFC3339),
			"suite_exit_code": code, "observations": observations,
			"not_verified": []string{"hardened VM/container boundary", "memory exhaustion isolation", "production load/capacity", "DNS rebinding and redirect enforcement", "weighted revision/resource grants", "Cloudflare live parity", "CPU accounting accuracy beyond the bounded interruption probe"},
		}, "", "  ")
		if err == nil {
			err = os.WriteFile(*reportPath, append(data, '\n'), 0o644)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
	}
	os.Exit(code)
}

func record(t *testing.T, name, status string, evidence any) {
	t.Helper()
	observations = append(observations, observation{name, status, evidence})
	t.Logf("%s: %s — %v", status, name, evidence)
}

type lockedBuffer struct {
	sync.Mutex
	data bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.data.Write(p)
}
func (b *lockedBuffer) String() string { b.Lock(); defer b.Unlock(); return b.data.String() }

type process struct {
	cmd      *exec.Cmd
	logs     *lockedBuffer
	done     chan error
	url      string
	stopOnce sync.Once
}

func start(t *testing.T, services, sockets string, experimental bool, sink io.Writer) *process {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workerd.capnp")
	source := fmt.Sprintf("using W = import \"/workerd/workerd.capnp\"; const config :W.Config = (services=[%s],sockets=[%s]);", services, sockets)
	return startConfig(t, path, source, experimental, sink)
}

func startConfig(t *testing.T, path, source string, experimental bool, sink io.Writer) *process {
	t.Helper()
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"serve"}
	if experimental {
		args = append(args, "--experimental")
	}
	args = append(args, path)
	p := &process{logs: &lockedBuffer{}, done: make(chan error, 1)}
	p.cmd = exec.Command(*executable, args...)
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// No application secrets inherited by the disposable runner.
	p.cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "TZ=UTC"}
	var logs io.Writer = p.logs
	if sink != nil {
		logs = io.MultiWriter(logs, sink)
	}
	p.cmd.Stdout, p.cmd.Stderr = logs, logs
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.done <- p.cmd.Wait(); close(p.done) }()
	t.Cleanup(func() { p.stop() })
	return p
}

func (p *process) stop() {
	p.stopOnce.Do(func() { _ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); <-p.done })
}

func port(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return p
}

func socket(p int, service string) string {
	return fmt.Sprintf("(name=\"http\",address=\"127.0.0.1:%d\",http=(),service=%q)", p, service)
}

func worker(name, source, bindings, outbound string) string {
	if outbound != "" {
		outbound = fmt.Sprintf(",globalOutbound=%q", outbound)
	}
	return fmt.Sprintf("(name=%q,worker=(compatibilityDate=\"2025-12-10\",modules=[(name=\"worker.js\",esModule=%q)],bindings=[%s]%s))", name, source, bindings, outbound)
}

func request(t *testing.T, base, path, identity string) (int, []byte) {
	t.Helper()
	r, err := http.NewRequest("GET", base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if identity != "" {
		r.Header.Set("Authorization", "Bearer "+identity)
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

func ready(t *testing.T, p *process, address string) {
	t.Helper()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		select {
		case <-p.done:
			t.Fatalf("workerd exited: %s", p.logs.String())
		default:
		}
		resp, err := client.Get(address + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
	}
	t.Fatalf("workerd not ready: %s", p.logs.String())
}

type registry struct {
	sync.Mutex
	revisions map[string]string
	source    string
	sources   map[string]string
}

func (r *registry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Header.Get("X-Poc-Registry-Token") != "fixture-only" {
		http.Error(w, "denied", 403)
		return
	}
	r.Lock()
	defer r.Unlock()
	revision, ok := r.revisions[req.URL.Path]
	if !ok {
		http.NotFound(w, req)
		return
	}
	source := r.source
	if override, ok := r.sources[req.URL.Path]; ok {
		source = override
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"revision": revision, "source": source})
}

type fixture struct {
	p        *process
	registry *registry
	server   *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	read := func(name string) string {
		b, e := os.ReadFile(name)
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	r := &registry{revisions: map[string]string{"/production/alpha": "v1", "/production/beta": "v1", "/staging/alpha": "staging-v1"}, source: read("customer.js"), sources: map[string]string{}}
	s := httptest.NewServer(r)
	t.Cleanup(s.Close)
	state := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "alpha-private-value") }))
	t.Cleanup(state.Close)
	services := fmt.Sprintf(`(name="registry",external=(address=%q,http=(injectRequestHeaders=[(name="X-Poc-Registry-Token",value="fixture-only")]))),
      (name="state",external=(address=%q)),
      (name="dispatcher",worker=(compatibilityDate="2025-12-10",modules=[(name="dispatcher.js",esModule=%q),(name="namespace.js",esModule=%q)],
        bindings=[(name="REGISTRY",service="registry"),(name="ALPHA_STATE",service="state"),(name="LOADER",workerLoader=())]))`,
		strings.TrimPrefix(s.URL, "http://"), strings.TrimPrefix(state.URL, "http://"), read("dispatcher.js"), read("namespace.js"))
	pn := port(t)
	p := start(t, services, socket(pn, "dispatcher"), true, nil)
	p.url = fmt.Sprintf("http://127.0.0.1:%d", pn)
	ready(t, p, p.url)
	return &fixture{p, r, s}
}

func jsonBody(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("invalid JSON: %s (%v)", data, err)
	}
	return result
}

func TestDispatch(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		path, user string
		status     int
	}{
		{"/production/alpha/context", "", 401}, {"/production/alpha/context", "beta", 403},
		{"/missing/alpha/context", "alpha", 404}, {"/production/alpha/context", "alpha", 200},
		{"/production/beta/context", "beta", 200},
	} {
		status, body := request(t, f.p.url, tc.path, tc.user)
		if status != tc.status {
			t.Fatalf("%s: %d %s", tc.path, status, body)
		}
	}
	// Only the dispatcher has a socket. User code is registry content, never a listener.
	status, _ := request(t, f.server.URL, "/production/alpha", "")
	if status != 403 {
		t.Fatal("registry bypass")
	}
	status, body := request(t, f.p.url, "/staging/alpha/context", "alpha")
	if status != 200 || jsonBody(t, body)["version"] != "staging-v1" {
		t.Fatalf("namespace isolation: %s", body)
	}
	var wg sync.WaitGroup
	errors := make(chan string, 24)
	ids := make(chan string, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			user := []string{"alpha", "beta"}[i%2]
			r, _ := http.NewRequest("GET", f.p.url+"/production/"+user+"/context", nil)
			r.Header.Set("Authorization", "Bearer "+user)
			r.Header.Set("X-Poc-User", "forged-other-tenant")
			resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(r)
			if err != nil {
				errors <- err.Error()
				return
			}
			defer resp.Body.Close()
			var value struct {
				Props  struct{ UserID, RequestID string }
				Hidden struct{ Registry, Loader bool }
			}
			if err := json.NewDecoder(resp.Body).Decode(&value); err != nil {
				errors <- err.Error()
				return
			}
			if resp.StatusCode != 200 || value.Props.UserID != user || value.Hidden.Registry || value.Hidden.Loader {
				errors <- fmt.Sprintf("context leaked: %+v", value)
				return
			}
			ids <- value.Props.RequestID
		}(i)
	}
	wg.Wait()
	close(errors)
	close(ids)
	for err := range errors {
		t.Error(err)
	}
	seen := map[string]bool{}
	for id := range ids {
		if id == "" || seen[id] {
			t.Fatal("request identity collision")
		}
		seen[id] = true
	}
	record(t, "dispatch, authorization, namespace and concurrent native props", "verified", map[string]any{"requests": 24, "unique_contexts": len(seen), "public_sockets": 1, "backend": "experimental Worker Loader"})
	_, a := request(t, f.p.url, "/production/alpha/resource", "alpha")
	_, b := request(t, f.p.url, "/production/beta/resource", "beta")
	_, staging := request(t, f.p.url, "/staging/alpha/resource", "alpha")
	if string(a) != "alpha-private-value" || string(b) != "unbound" || string(staging) != "unbound" {
		t.Fatalf("resource boundary %s %s", a, b)
	}
	record(t, "explicit resource capability", "verified", "alpha can call STATE; beta has no STATE, REGISTRY, or LOADER binding; this is not a production KV grant test")
}

func TestPropsCapabilitiesAndOutbound(t *testing.T) {
	f := newFixture(t)
	status, body := request(t, f.p.url, "/production/alpha/capability", "alpha")
	if status != 200 || jsonBody(t, body)["identity"] != "alpha" {
		t.Fatalf("capability: %d %s", status, body)
	}
	_, body = request(t, f.p.url, "/production/alpha/reuse-capability", "alpha")
	value := jsonBody(t, body)
	if value["rejected"] != true {
		t.Fatalf("cross-request capability survived: %s", body)
	}
	record(t, "request-scoped RPC in native props", "verified", value)
	var contexts []any
	for range 2 {
		status, body = request(t, f.p.url, "/production/alpha/egress", "alpha")
		value = jsonBody(t, body)
		if status != 200 || value["denied"] != float64(403) {
			t.Fatalf("outbound: %d %s", status, body)
		}
		context := value["allowed"].(map[string]any)["context"].(map[string]any)
		if context["userId"] != "alpha" {
			t.Fatal("wrong outbound identity")
		}
		contexts = append(contexts, context["requestId"])
	}
	if contexts[0] == contexts[1] {
		t.Fatal("outbound context reused")
	}
	record(t, "request-bound outbound policy", "verified", map[string]any{"distinct_contexts": contexts, "denied_status": 403, "tradeoff": "unique loader key per outbound invocation; no isolate-reuse claim"})
	controlStatus, _ := request(t, f.server.URL, "/", "")
	if controlStatus != 403 {
		t.Fatal("loopback control listener is not responding")
	}
	_, body = request(t, f.p.url, "/production/alpha/network?target="+url.QueryEscape(f.server.URL), "alpha")
	value = jsonBody(t, body)
	if value["httpBlocked"] != true || value["tcpBlocked"] != true {
		t.Fatalf("network: %s", body)
	}
	record(t, "null globalOutbound rejects HTTP and TCP attempts", "verified", value)
	status, body = request(t, f.p.url, "/production/alpha/subrequests", "alpha")
	value = jsonBody(t, body)
	limitStatus := "needs_review"
	if status == 200 && value["completed"] == float64(5) {
		limitStatus = "blocker"
	}
	record(t, "subrequest limit enforcement", limitStatus, map[string]any{"requested_limit": 2, "response_status": status, "response": value, "limits_supplied_at": []string{"WorkerCode", "EntrypointOptions"}})
}

func TestUpdateStreamAndCancel(t *testing.T) {
	f := newFixture(t)
	hash := sha256.Sum256(mustRead(t, "dispatcher.js"))
	pid := f.p.cmd.Process.Pid
	r, _ := http.NewRequest("GET", f.p.url+"/production/alpha/stream", nil)
	r.Header.Set("Authorization", "Bearer alpha")
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	first, err := reader.ReadString('\n')
	if err != nil || first != "v1:first\n" {
		t.Fatalf("first chunk %q %v", first, err)
	}
	f.registry.Lock()
	f.registry.revisions["/production/alpha"] = "v2"
	f.registry.sources["/production/alpha"] = strings.Replace(f.registry.source, "version: env.VERSION, props:", "version: env.VERSION, codeMarker: 'updated-source', props:", 1)
	f.registry.Unlock()
	_, body := request(t, f.p.url, "/production/alpha/context", "alpha")
	if jsonBody(t, body)["version"] != "v2" || jsonBody(t, body)["codeMarker"] != "updated-source" {
		t.Fatalf("update: %s", body)
	}
	last, err := reader.ReadString('\n')
	if err != nil || last != "v1:last\n" {
		t.Fatalf("old stream failed: %q %v", last, err)
	}
	if f.p.cmd.Process.Pid != pid || sha256.Sum256(mustRead(t, "dispatcher.js")) != hash {
		t.Fatal("dispatcher replaced")
	}
	record(t, "independent revision update and old stream drain", "verified", map[string]any{"dispatcher_restarted": false, "dispatcher_sha256": hex.EncodeToString(hash[:]), "old_stream": "v1:first + v1:last", "new_invocation": "v2", "new_code_marker": "updated-source"})
	ctx, cancel := context.WithCancel(context.Background())
	r, _ = http.NewRequestWithContext(ctx, "GET", f.p.url+"/production/alpha/cancel", nil)
	r.Header.Set("Authorization", "Bearer alpha")
	resp, err = (&http.Client{Timeout: 2 * time.Second}).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_, err = bufio.NewReader(resp.Body).ReadString('\n')
	cancel()
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	observed := waitFor(time.Second, func() bool { return strings.Contains(f.p.logs.String(), "poc-stream-cancelled") })
	status := "verified"
	if !observed {
		status = "not_verified"
	}
	record(t, "stream cancellation reaches customer stream", ""+status, map[string]any{"customer_cancel_callback_observed": observed})
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func waitFor(timeout time.Duration, check func() bool) bool {
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if check() {
			return true
		}
	}
	return false
}

func TestCPUInterruption(t *testing.T) {
	// Separate runtime for each experiment. Never run this against an existing server.
	for _, operation := range []string{"spin", "cpu-limit"} {
		t.Run(operation, func(t *testing.T) {
			f := newFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			r, _ := http.NewRequestWithContext(ctx, "GET", f.p.url+"/production/alpha/"+operation, nil)
			r.Header.Set("Authorization", "Bearer alpha")
			done := make(chan error, 1)
			go func() {
				resp, err := http.DefaultClient.Do(r)
				if resp != nil {
					resp.Body.Close()
				}
				done <- err
			}()
			if !waitFor(time.Second, func() bool { return strings.Contains(f.p.logs.String(), "poc-spin-started") }) {
				t.Fatalf("spin did not start: %s", f.p.logs.String())
			}
			requestErr := <-done
			if requestErr == nil {
				record(t, operation, "needs_review", "CPU probe returned; inspect runtime enforcement before treating as a pass")
				return
			}
			peerCtx, peerCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer peerCancel()
			peerReq, _ := http.NewRequestWithContext(peerCtx, "GET", f.p.url+"/production/beta/context", nil)
			peerReq.Header.Set("Authorization", "Bearer beta")
			peer, peerErr := http.DefaultClient.Do(peerReq)
			if peer != nil {
				peer.Body.Close()
			}
			select {
			case <-f.p.done:
				t.Fatal("runtime exited unexpectedly during bounded probe")
			default:
			}
			status := "blocker"
			if peerErr == nil {
				status = "needs_review"
			}
			record(t, "CPU interruption: "+operation, status, map[string]any{"client_timeout_ms": 150, "peer_timeout_ms": 250, "peer_responsive": peerErr == nil, "process_alive_after_client_abort": true, "loader_cpu_ms_requested": operation == "cpu-limit", "limits_supplied_at": []string{"WorkerCode", "EntrypointOptions"}, "bounded_by": "parent kills disposable process after observation"})
			f.p.stop()
		})
	}
}

func TestCurrentTelemetryForgery(t *testing.T) {
	stats := nfruntime.NewDurationTelemetry()
	store := nanoflare.NewStore()
	if err := store.CreateKVNamespace(nanoflare.KVNamespace{ID: "victim-kv", OrgID: "victim-org", Name: "private"}); err != nil {
		t.Fatal(err)
	}
	if err := store.KVPut("", "victim-kv", "secret", []byte("synthetic-victim-data")); err != nil {
		t.Fatal(err)
	}
	service := nanoflare.NewService(store, nil)
	// Match the production private listener's relevant routes using its real handlers.
	mux := http.NewServeMux()
	mux.Handle("/internal/runtime/durations", api.NewRuntimeDurationServer(service, stats))
	mux.Handle("/", api.NewRuntimeKVServer(service))
	collector := httptest.NewServer(mux)
	defer collector.Close()
	output := nfruntime.NewOutputBuffer()
	mux.Handle("/internal/runtime/logs", api.NewRuntimeLogServer(service, output))
	pn := port(t)
	items := []nanoflare.ActiveDeployment{{App: nanoflare.App{ID: "attacker", Name: "Attacker", RuntimeToken: "fixture-only"}, Deployment: nanoflare.Deployment{
		ID: "attacker-v1", AppID: "attacker", Port: pn, Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2025-12-10",
		Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default {async fetch(r,env){
          if(new URL(r.url).pathname==="/health")return new Response("ready");
          if(new URL(r.url).pathname==="/steal-kv") return env.__NANOFLARE_DURATION_COLLECTOR.fetch("http://internal/secret",{headers:{"X-Nanoflare-KV-Namespace-ID":"victim-kv"}});
          console.log("[[nanoflare-output app=victim deployment=victim-v1]] forged-log");
          await env.__NANOFLARE_DURATION_COLLECTOR.fetch("http://internal/internal/runtime/durations",{method:"POST",body:JSON.stringify([{scriptName:"victim",durationMs:123}])});
          return new Response("sent");}};`}},
	}}}
	if err := store.CreateApp(items[0].App); err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(items[0].Deployment); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "current.capnp")
	p := startConfig(t, path, config.WorkerdWithRuntimeAddr(items, strings.TrimPrefix(collector.URL, "http://")), false, output)
	p.url = fmt.Sprintf("http://127.0.0.1:%d", pn)
	ready(t, p, p.url)
	status, body := request(t, p.url, "/forge", "")
	if status != 200 {
		t.Fatalf("forge failed: %s", body)
	}
	if !waitFor(time.Second, func() bool { return stats.Stats("attacker").Available && len(output.Output("attacker")) > 0 }) {
		t.Fatalf("authenticated telemetry did not arrive: %s", p.logs.String())
	}
	if stats.Stats("victim").Available || len(output.Output("victim")) != 0 {
		t.Fatal("forged victim identity accepted")
	}
	record(t, "current Nanoflare telemetry identity forgery", "verified", map[string]any{"victim_duration_accepted": false, "victim_log_entries": 0, "attacker_telemetry_received": true})
	status, body = request(t, p.url, "/steal-kv", "")
	if status != http.StatusForbidden {
		t.Fatalf("KV bridge was not denied: %d %s", status, body)
	}
	record(t, "current telemetry capability confined to its handler", "verified", map[string]any{"unbound_victim_kv_read": false, "status": status})
}

func TestCurrentEgressPolicy(t *testing.T) {
	var hits int
	var mu sync.Mutex
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		io.WriteString(w, "private-fixture")
	}))
	defer target.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "proxy denies all", 403) }))
	defer proxy.Close()
	adapter, err := egress.New(egress.Config{Addr: "127.0.0.1:0", ProxyURL: proxy.URL, NoProxy: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = adapter.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = adapter.Close(ctx)
	}()
	for _, useProxy := range []bool{false, true} {
		pn := port(t)
		source := fmt.Sprintf(`export default {async fetch(r){if(new URL(r.url).pathname==="/health")return new Response("ready");try{return new Response(await (await fetch(%q)).text())}catch(e){return new Response("blocked",{status:403})}}};`, target.URL)
		items := []nanoflare.ActiveDeployment{{App: nanoflare.App{ID: "probe"}, Deployment: nanoflare.Deployment{ID: "v1", Port: pn, Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: source}}}}}
		options := config.WorkerdOptions{NetworkAllow: []string{"public"}}
		if useProxy {
			options.EgressAddr = adapter.Addr()
		}
		p := startConfig(t, filepath.Join(t.TempDir(), "egress.capnp"), config.WorkerdWithOptions(items, options), false, nil)
		p.url = fmt.Sprintf("http://127.0.0.1:%d", pn)
		ready(t, p, p.url)
		status, body := request(t, p.url, "/probe", "")
		if !useProxy && status != 403 {
			t.Fatalf("public-only unexpectedly reached loopback: %d %s", status, body)
		}
		if useProxy && (status != 200 || string(body) != "private-fixture") {
			t.Fatalf("adapter behavior changed: %d %s", status, body)
		}
		p.stop()
	}
	mu.Lock()
	count := hits
	mu.Unlock()
	if count != 1 {
		t.Fatalf("target hit count %d", count)
	}
	record(t, "current corporate egress adapter bypasses native network allowlist", "blocker", map[string]any{"native_public_only_status": 403, "with_adapter_no_proxy_status": 200, "loopback_target_hits": count, "interpretation": "documented corporate routing behavior; unsuitable as untrusted egress enforcement"})
}

func TestLoaderScaleSample(t *testing.T) {
	f := newFixture(t)
	var samples []map[string]any
	for _, count := range []int{1, 10, 50} {
		// New revisions force distinct cached customer isolates without restarting dispatcher.
		started := time.Now()
		for i := 0; i < count; i++ {
			f.registry.Lock()
			f.registry.revisions["/production/alpha"] = fmt.Sprintf("sample-%d-%d", count, i)
			f.registry.Unlock()
			status, body := request(t, f.p.url, "/production/alpha/version", "alpha")
			if status != 200 {
				t.Fatalf("load failed: %s", body)
			}
		}
		elapsed := time.Since(started).Milliseconds()
		samples = append(samples, map[string]any{"new_revisions": count, "elapsed_ms": elapsed, "process_tree_rss_kib": processTreeRSS(t, f.p.cmd.Process.Pid)})
	}
	record(t, "small sequential loader sample", "measured", samples)
}

// Include the native runtime child as well as npm's Node launcher.
func processTreeRSS(t *testing.T, root int) int {
	t.Helper()
	data, err := exec.Command("ps", "-axo", "pid=,ppid=,rss=").Output()
	if err != nil {
		t.Fatal(err)
	}
	type entry struct{ pid, parent, rss int }
	var entries []entry
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 3 {
			continue
		}
		pid, _ := strconv.Atoi(parts[0])
		parent, _ := strconv.Atoi(parts[1])
		rss, _ := strconv.Atoi(parts[2])
		entries = append(entries, entry{pid, parent, rss})
	}
	owned := map[int]bool{root: true}
	for changed := true; changed; {
		changed = false
		for _, e := range entries {
			if owned[e.parent] && !owned[e.pid] {
				owned[e.pid] = true
				changed = true
			}
		}
	}
	total := 0
	for _, e := range entries {
		if owned[e.pid] {
			total += e.rss
		}
	}
	if total == 0 {
		t.Fatal("runtime RSS unavailable")
	}
	return total
}
