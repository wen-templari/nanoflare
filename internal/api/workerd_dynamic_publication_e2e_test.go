package api_test

import (
	"bufio"
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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runner"
	"github.com/clas/nanoflare/internal/runtime"
)

type dynamicPublicationLauncher struct {
	command  runtime.CommandLauncher
	launches atomic.Int32
	mu       sync.Mutex
	process  runtime.Process
}

func (l *dynamicPublicationLauncher) Launch(path string, active []nanoflare.ActiveDeployment) (runtime.Process, error) {
	process, err := l.command.Launch(path, active)
	if err == nil {
		l.mu.Lock()
		l.process = process
		l.mu.Unlock()
		l.launches.Add(1)
	}
	return process, err
}

type dynamicPublicationWriter struct {
	manager *runtime.DynamicManager
	remote  *runner.Client
}

func (w *dynamicPublicationWriter) Write(active []nanoflare.ActiveDeployment) error {
	if w.remote != nil {
		return w.remote.Write(active)
	}
	return w.manager.Write(active)
}
func (w *dynamicPublicationWriter) ValidatePublication(active []nanoflare.ActiveDeployment) error {
	if w.remote != nil {
		return w.remote.ValidatePublication(active)
	}
	return w.manager.ValidatePublication(active)
}

// Exercise production publication, admission, resource authority and the public
// Go gateway together. This opt-in gate requires the production native runtime.
func TestWorkerdDynamicPublicationEndToEnd(t *testing.T) {
	workerd := os.Getenv("NANOFLARE_TEST_WORKERD")
	if workerd == "" {
		t.Skip("set NANOFLARE_TEST_WORKERD to the production-pinned native workerd")
	}
	version, err := exec.Command(workerd, "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(version)) != "workerd 2026-09-11" {
		t.Fatalf("requires workerd 2026-09-11: %s (%v)", version, err)
	}
	for _, remote := range []bool{false, true} {
		name := "local"
		if remote {
			name = "remote"
		}
		t.Run(name, func(t *testing.T) { runDynamicPublicationEndToEnd(t, workerd, remote) })
	}
}

func runDynamicPublicationEndToEnd(t *testing.T, workerd string, remote bool) {
	store := nanoflare.NewStore()
	if err := store.CreateOrganization(nanoflare.Organization{ID: "dynamic-org", Name: "Dynamic"}); err != nil {
		t.Fatal(err)
	}
	publication := &dynamicPublicationWriter{}
	service := nanoflare.NewServiceWithObjects(store, publication, newE2EObjectStore())
	codec, err := nanoflare.NewSecretCodec("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	service.SetSecretCodec(codec)
	control := api.NewRuntimeExecutionServer(service, "dynamic-control")
	var controlMu sync.RWMutex
	defer func() { control.Close() }()
	privateMux := http.NewServeMux()
	privateMux.HandleFunc("/internal/runtime/executions/", func(w http.ResponseWriter, r *http.Request) {
		controlMu.RLock()
		defer controlMu.RUnlock()
		control.ServeHTTP(w, r)
	})
	privateMux.HandleFunc("/internal/runtime/durations", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	resources := api.NewRuntimeKVServer(service)
	privateMux.Handle("/", resources)
	privateMux.Handle("/internal/runtime/resources/", http.StripPrefix("/internal/runtime/resources", resources))
	private := httptest.NewServer(privateMux)
	defer private.Close()
	dir := t.TempDir()
	writer := config.NewRuntimeWriter(filepath.Join(dir, "workerd.capnp"), discardTraefik{})
	writer.SetNanoflareRuntimeAddr(strings.TrimPrefix(private.URL, "http://"))
	launcher := &dynamicPublicationLauncher{command: runtime.CommandLauncher{Executable: workerd, Experimental: true}}
	newManager := func() *runtime.DynamicManager {
		manager := runtime.NewDynamicManager(writer, launcher, dir, filepath.Join(dir, "workerd.capnp"), testSocketDir(t), 5*time.Second, 5*time.Second)
		manager.SetRuntimeControlToken("dynamic-control")
		return manager
	}
	publication.manager = newManager()
	defer func() { publication.manager.Close() }()
	var ensurer api.RuntimeEnsurer = publication.manager
	if remote {
		runnerServer := httptest.NewServer(runner.NewServer(publication.manager, "dynamic-control"))
		defer runnerServer.Close()
		client := runner.NewClient(runnerServer.URL, "dynamic-control", discardTraefik{})
		publication.remote = client
		ensurer = client
	}

	alpha, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "dynamic-org", Name: "Alpha", Hostname: "alpha.dynamic.test"})
	if err != nil {
		t.Fatal(err)
	}
	beta, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "dynamic-org", Name: "Beta", Hostname: "beta.dynamic.test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.PutSecret(alpha.ID, "SECRET", "frozen-secret"); err != nil {
		t.Fatal(err)
	}
	kv, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{OrgID: "dynamic-org", Name: "retired-execution"})
	if err != nil {
		t.Fatal(err)
	}
	const code = `import { env as importedEnv } from "cloudflare:workers";
const { KV } = importedEnv;
let counter = 0;
export default {async fetch(request,env,ctx) {
 if (new URL(request.url).pathname === "/ws") {
  const pair = new WebSocketPair(); pair[1].accept();
  const listener = async event => {
   try {
    if (KV) {
     await KV.put("socket:" + event.data, env.LABEL + ":" + event.data);
     pair[1].send(await KV.get("socket:" + event.data));
    } else pair[1].send(env.LABEL + ":" + event.data);
   } catch (error) { pair[1].send("ERROR:" + error.message); }
  };
  // Exercise native property handlers, listener removal, and listener objects.
  const unused = () => pair[1].send("removed listener ran");
  pair[1].addEventListener("message", unused);
  pair[1].removeEventListener("message", unused);
  pair[1].onmessage = event => { if (event.data !== "again") return listener(event); };
  pair[1].addEventListener("message", {handleEvent(event) {
   if (event.data === "again") return listener(event);
  }});
  return new Response(null,{status:101,webSocket:pair[0]});
 }
 if (new URL(request.url).pathname === "/stream") {
  let sent = false;
  return new Response(new ReadableStream({async pull(controller) {
   if (sent) { controller.close(); return; } sent = true;
   await KV.put("stream", env.LABEL);
   controller.enqueue(new TextEncoder().encode(await KV.get("stream")));
  }}));
 }
 if (new URL(request.url).pathname === "/defer") {
  ctx.waitUntil((async () => {
   while (await KV.get("release") !== "yes") await new Promise(resolve => setTimeout(resolve,10));
   await KV.put("completed",env.LABEL+":"+env.SECRET);
  })());
 }
 return Response.json({counter:++counter,label:env.LABEL,secret:env.SECRET??null});
}};`
	deploy := func(app nanoflare.App, label string, bindings []nanoflare.KVBinding) (nanoflare.Deployment, error) {
		return service.Deploy(app.ID, nanoflare.DeployInput{Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: code}}, Entrypoint: "worker.js", Format: "modules", CompatibilityDate: "2026-09-11", Vars: map[string]json.RawMessage{"LABEL": json.RawMessage(fmt.Sprintf("%q", label))}, KVNamespaces: bindings})
	}
	_, err = deploy(alpha, "alpha-old", []nanoflare.KVBinding{{Binding: "KV", ID: kv.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deploy(beta, "beta", nil); err != nil {
		t.Fatal(err)
	}
	server := api.NewServerWithRuntime(service, nil, "", nil, nil, ensurer)
	type reply struct {
		Counter int     `json:"counter"`
		Label   string  `json:"label"`
		Secret  *string `json:"secret"`
	}
	request := func(app nanoflare.App, path string) reply {
		t.Helper()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/internal/http/workers/"+app.ID+path, nil)
		req.Header.Set("X-Nanoflare-Execution", "attacker")
		req.Header.Set("X-Nanoflare-Runner-Token", "attacker")
		req.Header.Set("X-Nanoflare-Validate", "true")
		server.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: HTTP %d %s", app.Name, recorder.Code, recorder.Body.String())
		}
		var result reply
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	grantFor := func(app nanoflare.App) string {
		t.Helper()
		active, err := store.ActiveDeploymentsForApp(app.ID)
		if err != nil || len(active) != 1 {
			t.Fatalf("active revision: %v %v", active, err)
		}
		worker, err := publication.manager.Ensure(context.Background(), active[0])
		if err != nil {
			t.Fatal(err)
		}
		defer worker.Release()
		return worker.Headers.Get("X-Nanoflare-Execution")
	}
	oldGrant := grantFor(alpha)
	gateway := httptest.NewServer(server)
	defer gateway.Close()
	stream := httptest.NewRecorder()
	server.ServeHTTP(stream, httptest.NewRequest(http.MethodGet, "/internal/http/workers/"+alpha.ID+"/stream", nil))
	if stream.Code != http.StatusOK || stream.Body.String() != "alpha-old" {
		t.Fatalf("imported KV in stream pull: HTTP %d %s", stream.Code, stream.Body.String())
	}
	oldSocket, oldReader := dynamicPublicationWebSocket(t, gateway.URL, alpha.ID)
	defer oldSocket.Close()
	secondOldSocket, secondOldReader := dynamicPublicationWebSocket(t, gateway.URL, alpha.ID)
	defer secondOldSocket.Close()
	old := request(alpha, "/defer")
	if old.Label != "alpha-old" || old.Secret == nil || *old.Secret != "frozen-secret" {
		t.Fatalf("old snapshot = %#v", old)
	}
	if got := request(alpha, "/__nanoflare_validate"); got.Label != "alpha-old" {
		t.Fatalf("validation path bypassed application: %#v", got)
	}
	warm := request(beta, "/")
	if next := request(beta, "/"); next.Counter != warm.Counter+1 {
		t.Fatalf("beta was not warm: %#v -> %#v", warm, next)
	} else {
		warm = next
	}
	second, err := deploy(alpha, "alpha-new", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := dynamicPublicationSocketMessage(t, oldSocket, oldReader, "retired"); got != "alpha-old:retired" {
		t.Fatalf("old socket changed revision: %q", got)
	}
	if got := dynamicPublicationSocketMessage(t, oldSocket, oldReader, "again"); got != "alpha-old:again" {
		t.Fatalf("old socket listener object lost its revision: %q", got)
	}
	newSocket, newReader := dynamicPublicationWebSocket(t, gateway.URL, alpha.ID)
	if got := dynamicPublicationSocketMessage(t, newSocket, newReader, "fresh"); got != "alpha-new:fresh" {
		t.Fatalf("new socket used old revision: %q", got)
	}
	newSocket.Close()
	oldSocket.Close()
	if got := dynamicPublicationSocketMessage(t, secondOldSocket, secondOldReader, "remaining"); got != "alpha-old:remaining" {
		t.Fatalf("closing one socket revoked another: %q", got)
	}
	secondOldSocket.Close()
	if fresh := request(alpha, "/"); fresh.Label != "alpha-new" {
		t.Fatalf("new alpha = %#v", fresh)
	}
	if next := request(beta, "/"); next.Counter != warm.Counter+1 {
		t.Fatalf("alpha publication reset beta: %#v -> %#v", warm, next)
	} else {
		warm = next
	}
	// The old revision has no fresh admission, but its waitUntil retains its old
	// environment and declared KV authority after the new revision drops KV.
	if err := store.KVPut("", kv.ID, "release", []byte("yes")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		value, found, err := store.KVGet("", kv.ID, "completed")
		if err != nil {
			t.Fatal(err)
		}
		if found {
			if string(value) != "alpha-old:frozen-secret" {
				t.Fatalf("retired execution observed mutable snapshot: %q", value)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retired waitUntil lost KV authority")
		}
		time.Sleep(10 * time.Millisecond)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		operation, err := service.Executions().AcquireResource(oldGrant, nanoflare.ExecutionKV, kv.ID)
		if err != nil {
			break
		}
		operation.Release()
		if time.Now().After(deadline) {
			t.Fatal("old grant remained live after final socket and background work drained")
		}
		time.Sleep(10 * time.Millisecond)
	}
	before, _ := store.PublicationStates()
	if _, err := service.Deploy(alpha.ID, nanoflare.DeployInput{Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default { broken syntax"}}, Entrypoint: "worker.js", Format: "modules", CompatibilityDate: "2026-09-11"}); err == nil {
		t.Fatal("invalid candidate accepted")
	}
	active, _ := store.ActiveDeploymentsForApp(alpha.ID)
	if len(active) != 1 || active[0].Deployment.ID != second.ID {
		t.Fatal("invalid candidate changed active routing")
	}
	after, _ := store.PublicationStates()
	for _, state := range after {
		for _, previous := range before {
			if state.AppID == previous.AppID && state.Generation != previous.Generation {
				t.Fatal("invalid validation advanced publication")
			}
		}
	}
	if fresh := request(alpha, "/"); fresh.Label != "alpha-new" {
		t.Fatalf("invalid candidate replaced alpha: %#v", fresh)
	}
	if next := request(beta, "/"); next.Counter != warm.Counter+1 {
		t.Fatal("invalid alpha candidate reset beta")
	} else {
		warm = next
	}
	result, err := service.UpdateVarsForOrg("dynamic-org", alpha.ID, nanoflare.UpdateVarsInput{ExpectedDeploymentID: second.ID, IdempotencyKey: "vars-snapshot", Vars: map[string]json.RawMessage{"LABEL": json.RawMessage(`"alpha-vars"`)}})
	if err != nil {
		t.Fatal(err)
	}
	if fresh := request(alpha, "/"); fresh.Label != "alpha-vars" || fresh.Secret == nil || *fresh.Secret != "frozen-secret" {
		t.Fatalf("vars snapshot = %#v", fresh)
	}
	if next := request(beta, "/"); next.Counter != warm.Counter+1 {
		t.Fatal("vars publication reset beta")
	} else {
		warm = next
	}
	if err := service.PutSecret(alpha.ID, "SECRET", "new-secret"); err != nil {
		t.Fatal(err)
	}
	if fresh := request(alpha, "/"); fresh.Label != "alpha-vars" || fresh.Secret == nil || *fresh.Secret != "new-secret" {
		t.Fatalf("secret snapshot = %#v", fresh)
	}
	if next := request(beta, "/"); next.Counter != warm.Counter+1 {
		t.Fatal("secret publication reset beta")
	} else {
		warm = next
	}
	if launcher.launches.Load() != 1 {
		t.Fatalf("application publication replaced workerd: launches=%d", launcher.launches.Load())
	}
	// Simulate a transaction committed just before runtime publication was lost.
	if err := store.SetActive(alpha.ID, result.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcilePublications(); err != nil {
		t.Fatal(err)
	}
	if fresh := request(alpha, "/"); fresh.Label != "alpha-vars" {
		t.Fatalf("recovery = %#v", fresh)
	}
	if next := request(beta, "/"); next.Counter != warm.Counter+1 {
		t.Fatal("reconciliation reset beta")
	}
	states, _ := store.PublicationStates()
	for _, state := range states {
		if state.Generation != state.PublishedGeneration {
			t.Fatalf("recovery unacknowledged: %#v", state)
		}
	}
	if launcher.launches.Load() != 1 {
		t.Fatal("reconciliation replaced runtime")
	}
	// The process supervisor reconstructs committed routing after a real workerd
	// crash; application publication itself never launches a replacement.
	launcher.mu.Lock()
	crashed := launcher.process
	launcher.mu.Unlock()
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := crashed.Stop(stopCtx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	deadline = time.Now().Add(5 * time.Second)
	var recovered reply
	for {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/internal/http/workers/"+alpha.ID+"/", nil))
		if recorder.Code == http.StatusOK {
			if err := json.Unmarshal(recorder.Body.Bytes(), &recovered); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("crash recovery: HTTP %d %s", recorder.Code, recorder.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if recovered.Label != "alpha-vars" || recovered.Secret == nil || *recovered.Secret != "new-secret" {
		t.Fatalf("crash replayed stale snapshot: %#v", recovered)
	}
	if fresh := request(beta, "/"); fresh.Label != "beta" {
		t.Fatalf("beta crash recovery = %#v", fresh)
	}
	if launcher.launches.Load() != 2 {
		t.Fatalf("crash recovery launches = %d", launcher.launches.Load())
	}
	// Replacing the private listener loses the runner session while workerd is
	// still alive. Heartbeat must rebuild the runtime with fresh grants.
	beforeSessionLoss := grantFor(alpha)
	controlMu.Lock()
	previousControl := control
	control = api.NewRuntimeExecutionServer(service, "dynamic-control")
	controlMu.Unlock()
	if err := previousControl.Close(); err != nil {
		t.Fatal(err)
	}
	if lease, err := service.Executions().Admit(beforeSessionLoss); err == nil {
		lease.Release()
		t.Fatal("closed control session retained admission authority")
	}
	deadline = time.Now().Add(25 * time.Second)
	for launcher.launches.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatal("runner did not recover after control session loss")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/internal/http/workers/"+alpha.ID+"/", nil))
		if recorder.Code == http.StatusOK {
			if err := json.Unmarshal(recorder.Body.Bytes(), &recovered); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session recovery: HTTP %d %s", recorder.Code, recorder.Body.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if recovered.Label != "alpha-vars" || recovered.Secret == nil || *recovered.Secret != "new-secret" {
		t.Fatalf("session recovery replayed stale snapshot: %#v", recovered)
	}
	if fresh := request(beta, "/"); fresh.Label != "beta" {
		t.Fatalf("beta session recovery = %#v", fresh)
	}
	if afterSessionLoss := grantFor(alpha); afterSessionLoss == beforeSessionLoss {
		t.Fatal("session recovery reused old resource grant")
	}
	if launcher.launches.Load() != 3 {
		t.Fatalf("session recovery launches = %d", launcher.launches.Load())
	}
}

func dynamicPublicationWebSocket(t *testing.T, gateway, appID string) (net.Conn, *bufio.Reader) {
	t.Helper()
	address := strings.TrimPrefix(gateway, "http://")
	conn, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "GET /internal/http/workers/%s/ws HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", appID, address)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		t.Fatalf("WebSocket upgrade: %s", response.Status)
	}
	return conn, reader
}
func dynamicPublicationSocketMessage(t *testing.T, conn net.Conn, reader *bufio.Reader, message string) string {
	t.Helper()
	mask := []byte{1, 2, 3, 4}
	frame := []byte{0x81, 0x80 | byte(len(message))}
	frame = append(frame, mask...)
	for i, value := range []byte(message) {
		frame = append(frame, value^mask[i%4])
	}
	if _, err := conn.Write(frame); err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		t.Fatal(err)
	}
	if header[0]&0xf != 1 || header[1]&0x7f >= 126 {
		t.Fatalf("unexpected WebSocket frame: %x", header)
	}
	payload := make([]byte, int(header[1]&0x7f))
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatal(err)
	}
	return string(payload)
}
