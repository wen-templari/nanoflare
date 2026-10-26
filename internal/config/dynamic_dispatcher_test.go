package config

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtimeassets"
)

// This tests the generated production dispatcher rather than the exploratory
// host fixture: private socket authentication, validation, resource forwarding,
// nested service admission, and both sides' completion acknowledgments.
func TestWorkerdDynamicDispatcherContracts(t *testing.T) {
	binary := os.Getenv("NANOFLARE_TEST_WORKERD")
	if binary == "" {
		t.Skip("set NANOFLARE_TEST_WORKERD to the production-pinned binary")
	}
	version, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(version)) != "workerd 2026-09-11" {
		t.Fatalf("requires pinned runtime: %s (%v)", version, err)
	}
	customer := nanoflare.Deployment{AppID: "customer", ID: "customer-v1", Entrypoint: "worker.js", Format: "modules", CompatibilityDate: "2026-07-30", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `
export default { async fetch(request,env,ctx) {
 const cap=ctx.props.nested[0].capability;
 const path=new URL(request.url).pathname;
 try {
  let result;
  switch(path) {
   case "/method": result=await cap.ping(); break;
   case "/getter": result=await cap.kind; break;
   case "/props": result=await cap.readOther(); break;
   case "/nested": result=await cap.ai.run("test"); break;
   case "/nested-awaited": { const ai=await cap.ai; result=await ai.run("test"); break; }
   case "/nested-method": { const ai=await cap.getAI(); result=await ai.run("test"); break; }
   case "/throws": result=await cap.broken.run(); break;
  }
  return Response.json({result});
 } catch(error) {return Response.json({error:error.message},{status:502});}
} };`}}}
	programs := map[string]map[string]any{}
	for key, source := range map[string]string{
		"dispatcher": `import {RpcTarget,WorkerEntrypoint} from "cloudflare:workers";
class AI extends RpcTarget {run(value){return {response:"AI_OK",value};}}
export class Other extends WorkerEntrypoint {ping(){return "OTHER_OK";}}
export class Capability extends WorkerEntrypoint {
 ping(){return this.ctx.props.prefix+"ONG";} get kind(){return "capability";} get ai(){return new AI();}
 readOther(){return this.ctx.props.other.ping();}
 getAI(){return new AI();} get broken(){throw new Error("getter failed");}
}
export default {fetch(request,env,ctx){return env.DISPATCHER.get(new URL(request.url).searchParams.has("class")?"class":"customer",{props:{nested:[{capability:ctx.exports.Capability({props:{prefix:"P",other:ctx.exports.Other({props:{}})}})}]}}).fetch(request);}};`,
		"invalid": `export default {fetch(request: Request){}}`,
		"throws":  `throw new Error("private-initialization-detail"); export default {fetch(){}}`,
		"a": `import { env as importedEnv } from "cloudflare:workers";
const { KV, SERVICE } = importedEnv;
const get = KV.get;
export default { async fetch(request, env, ctx) {
 if (new URL(request.url).pathname === "/__nanoflare_validate") return new Response("application response");
 if (new URL(request.url).pathname === "/missing") return new Response(await env.SERVICE.missing());
 const value = await get("key");
 using session = await SERVICE.session();
 await new Promise(resolve => setTimeout(resolve, 30));
 const rpc = await session.read("ada");
 ctx.waitUntil(KV.put("background", "done"));
 const identity = await ctx.access.getIdentity();
 return Response.json({ value, rpc, identity, jwt: request.headers.get("X-Nanoflare-User-JWT"), email: request.headers.get("X-Nanoflare-User-Email"), headers: [...request.headers.keys()].filter(name => name.startsWith("x-nanoflare-")) });
} };`,
		"b": `import { WorkerEntrypoint, RpcTarget, env as importedEnv } from "cloudflare:workers";
const { KV } = importedEnv;
class Session extends RpcTarget {
 #kv; #version; #hidden; #ctx;
 constructor(kv,version,hidden,ctx) { super(); this.#kv=kv; this.#version=version; this.#hidden=hidden; this.#ctx=ctx; }
 async read(id) {
  this.#ctx.waitUntil((async () => { await new Promise(resolve => setTimeout(resolve, 80)); await this.#kv.put("cap-background", "done"); })());
  return {id, version:this.#version, hidden:this.#hidden, value:await this.#kv.get("session-key")};
 }
}
export default class extends WorkerEntrypoint { session() { return new Session(KV,this.env.VERSION,!this.ctx.props.__nanoflare_invocation,this.ctx); } }`,
	} {
		active := nanoflare.ActiveDeployment{Deployment: nanoflare.Deployment{Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2026-09-08", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: source}}}}
		if key == "dispatcher" {
			active.Deployment.DispatchNamespaces = []nanoflare.DispatchBinding{{Binding: "DISPATCHER", NamespaceID: "test"}}
		}
		if key == "a" {
			active.Deployment.KVNamespaces = []nanoflare.KVBinding{{Binding: "KV", ID: "kv"}}
			active.Deployment.Services = []nanoflare.ServiceBinding{{Binding: "SERVICE", Service: "b"}}
		}
		if key == "b" {
			active.Deployment.KVNamespaces = []nanoflare.KVBinding{{Binding: "KV", ID: "kv-b"}}
		}
		code := InstrumentLoaderInvocation(SnapshotWorkerCodeForLoader(active, WorkerdOptions{}), active)
		wire, err := json.Marshal(code)
		if err != nil {
			t.Fatal(err)
		}
		var program map[string]any
		if err := json.Unmarshal(wire, &program); err != nil {
			t.Fatal(err)
		}
		program["env"] = map[string]any{"VERSION": key, "__NANOFLARE_APP_ID": key}
		programs[key] = program
	}
	var mu sync.Mutex
	completions := map[string]int{}
	starts := map[string]int{}
	abandoned := map[string]int{}
	resourceCalls := 0
	background := false
	capBackground := false
	completeRetried := false
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Nanoflare-Broker-Token") != "broker-secret" {
			http.Error(w, "unauthorized broker", 401)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/start/"):
			if r.Header.Get("X-Nanoflare-Invocation") == "invocation-denied" {
				http.Error(w, "revoked", 403)
				return
			}
			mu.Lock()
			if r.Header.Get("X-Nanoflare-Invocation") == "invocation-b" && r.Header.Get("X-Nanoflare-Invocation-Epoch") == "2" && completions["invocation-b"] != 1 {
				mu.Unlock()
				http.Error(w, "epoch reopened before prior acknowledgement", 409)
				return
			}
			starts[r.Header.Get("X-Nanoflare-Invocation")]++
			mu.Unlock()
			w.WriteHeader(204)
		case strings.HasPrefix(r.URL.Path, "/abandon/"):
			mu.Lock()
			abandoned[r.Header.Get("X-Nanoflare-Invocation")]++
			mu.Unlock()
			w.WriteHeader(204)
		case strings.HasPrefix(r.URL.Path, "/source/"):
			program := programs[strings.TrimPrefix(r.URL.Path, "/source/")]
			if program == nil {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(program)
		case strings.HasPrefix(r.URL.Path, "/complete/"):
			mu.Lock()
			if r.Header.Get("X-Nanoflare-Invocation") == "invocation-b" && r.Header.Get("X-Nanoflare-Invocation-Epoch") == "2" && !completeRetried {
				completeRetried = true
				mu.Unlock()
				http.Error(w, "transient completion failure", 503)
				return
			}
			completions[r.Header.Get("X-Nanoflare-Invocation")]++
			mu.Unlock()
			w.WriteHeader(204)
		case r.URL.Path == "/service/a/SERVICE":
			if !strings.HasPrefix(r.Header.Get("X-Nanoflare-Invocation"), "invocation-") {
				http.Error(w, "bad service token", 403)
				return
			}
			childToken := "invocation-b"
			if r.Header.Get("X-Nanoflare-Invocation") == "invocation-missing-a" {
				childToken = "invocation-missing-b"
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"key": "b", "token": childToken})
		case r.URL.Path == "/export/dispatcher":
			if r.Header.Get("X-Nanoflare-Invocation") != "invocation-dispatcher" {
				http.Error(w, "bad export token", 403)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "invocation-export"})
		case r.URL.Path == "/resource/dispatcher/internal/runtime/dispatch/acquire":
			deployment := customer
			var input struct {
				Script string `json:"script"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if input.Script == "class" {
				deployment.ID = "class-v1"
				deployment.Files = []nanoflare.WorkerFile{{Path: "worker.js", Content: strings.Replace(customer.Files[0].Content, `export default { async fetch(request,env,ctx) {`, `import {WorkerEntrypoint} from "cloudflare:workers"; export default class extends WorkerEntrypoint { async fetch(request) { const env=this.env,ctx=this.ctx;`, 1)}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"deployment": deployment, "digest": "customer-digest", "lease": "customer-lease", "values": map[string]string{}, "wrapper": DynamicEntrypoint(deployment), "runtime_bindings": runtimeassets.BindingsModule, "dynamic_env": runtimeassets.DynamicEnvModule})
		case r.URL.Path == "/resource/dispatcher/internal/runtime/dispatch/finish":
			w.WriteHeader(204)
		case strings.HasPrefix(r.URL.Path, "/resource/"):
			if r.Header.Get("X-Nanoflare-Binding-Name") == "__NANOFLARE_ACCESS" {
				if r.Header.Get("X-Nanoflare-User-JWT") != "signed-assertion" {
					http.Error(w, "missing assertion", 401)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"email": "user@example.com"})
				return
			}
			if r.Header.Get("X-Nanoflare-Binding-Name") == "__NANOFLARE_DURATION_COLLECTOR" {
				w.WriteHeader(204)
				return
			}
			if r.URL.Path == "/resource/b/cap-background" {
				mu.Lock()
				valid := completions["invocation-a"] == 1 && completions["invocation-b"] == 1 && starts["invocation-b"] == 2
				mu.Unlock()
				if !valid || r.Header.Get("X-Nanoflare-Invocation") != "" {
					http.Error(w, "late capability background authority expired or not reopened", 403)
					return
				}
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				capBackground = string(body) == "done"
				mu.Unlock()
				w.WriteHeader(204)
				return
			}
			if r.URL.Path == "/resource/b/session-key" {
				mu.Lock()
				childCompleted := completions["invocation-b"] == 1
				parentCompleted := completions["invocation-a"] > 0
				mu.Unlock()
				if !childCompleted || parentCompleted || r.Header.Get("X-Nanoflare-Invocation") != "" || r.Header.Get("X-Nanoflare-Binding-Name") != "KV" {
					http.Error(w, "capability used outside live root or before child completed", 403)
					return
				}
				_, _ = io.WriteString(w, "capability-value")
				return
			}
			if r.Header.Get("X-Nanoflare-Invocation") != "" || r.Header.Get("X-Nanoflare-Binding-Name") != "KV" {
				http.Error(w, "bad resource authority", 403)
				return
			}
			mu.Lock()
			resourceCalls++
			mu.Unlock()
			if r.URL.Path == "/resource/a/key" {
				_, _ = io.WriteString(w, "private-value")
				return
			}
			if r.URL.Path == "/resource/a/background" {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				background = string(body) == "done"
				mu.Unlock()
				w.WriteHeader(204)
				return
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer broker.Close()
	dir := t.TempDir()
	socketDir, err := os.MkdirTemp("/tmp", "nf-dispatch-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "runtime.sock")
	configPath := filepath.Join(dir, "workerd.capnp")
	if err := os.WriteFile(configPath, []byte(DynamicDispatcher(WorkerdOptions{}, socket, strings.TrimPrefix(broker.URL, "http://"), "broker-secret", "dispatcher-secret")), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "serve", "--experimental", configPath)
	var logs bytes.Buffer
	command.Stdout = &logs
	command.Stderr = &logs
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatalf("dispatcher socket unavailable: %s", logs.String())
		}
	}
	call := func(path, auth, token string, executions ...string) *http.Response {
		t.Helper()
		request, _ := http.NewRequest("GET", "http://worker.test"+path, nil)
		request.Header.Set("X-Nanoflare-Dispatcher-Token", auth)
		execution := "a"
		if len(executions) > 0 {
			execution = executions[0]
		}
		request.Header.Set("X-Nanoflare-Execution", execution)
		request.Header.Set("X-Nanoflare-Invocation", token)
		request.Header.Set("X-Nanoflare-Forged", "must-strip")
		request.Header.Set("X-Nanoflare-User-JWT", "signed-assertion")
		request.Header.Set("X-Nanoflare-User-Email", "user@example.com")
		if token == "" {
			request.Header.Set("X-Nanoflare-Validate", "true")
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	unauthorized := call("/", "bad", "invocation-a")
	unauthorized.Body.Close()
	if unauthorized.StatusCode != 401 {
		t.Fatalf("unauthorized status=%d", unauthorized.StatusCode)
	}
	for _, execution := range []string{"invalid", "throws", "invalid", "throws", "missing"} {
		response := call("/__nanoflare_validate", "dispatcher-secret", "", execution)
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		failure := response.Header.Get("X-Nanoflare-Validation-Failure")
		if execution == "missing" {
			if response.StatusCode == 204 || failure != "" {
				t.Fatalf("broker failure classified as Worker failure: %d %s", response.StatusCode, body)
			}
		} else if response.StatusCode != 422 || failure != "worker_initialization" || strings.Contains(string(body), "private-initialization-detail") {
			t.Fatalf("Worker validation %s: %d %s %s", execution, response.StatusCode, failure, body)
		}
	}
	validation := call("/__nanoflare_validate", "dispatcher-secret", "")
	body, _ := io.ReadAll(validation.Body)
	validation.Body.Close()
	if validation.StatusCode != 204 {
		t.Fatalf("validation=%d %s", validation.StatusCode, body)
	}
	mu.Lock()
	callsDuringValidation := resourceCalls
	mu.Unlock()
	if callsDuringValidation != 0 {
		t.Fatal("validation executed user fetch")
	}
	application := call("/__nanoflare_validate", "dispatcher-secret", "invocation-path")
	body, _ = io.ReadAll(application.Body)
	application.Body.Close()
	if application.StatusCode != 200 || string(body) != "application response" {
		t.Fatalf("validation path intercepted application request: %d %s", application.StatusCode, body)
	}
	response := call("/", "dispatcher-secret", "invocation-a")
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("dispatcher=%d %s", response.StatusCode, body)
	}
	var result struct {
		Value string
		RPC   struct {
			ID, Version, Value string
			Hidden             bool
		}
		JWT, Email string
		Identity   struct{ Email string }
		Headers    []string
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Value != "private-value" || result.RPC.ID != "ada" || result.RPC.Version != "b" || result.RPC.Value != "capability-value" || !result.RPC.Hidden || strings.Join(result.Headers, ",") != "x-nanoflare-user-email,x-nanoflare-user-jwt" || result.JWT != "signed-assertion" || result.Email != "user@example.com" || result.Identity.Email != "user@example.com" {
		t.Fatalf("unexpected child result %s", body)
	}
	for deadline := time.Now().Add(4 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		mu.Lock()
		done := completions["invocation-a"] == 1 && completions["invocation-b"] == 2 && starts["invocation-a"] == 1 && starts["invocation-b"] == 2 && background && capBackground && completeRetried
		mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("missing invocation completions/background")
		}
	}
	// Exercise real WFP dispatch, export admission, and native RPC, including warm calls.
	for round := 0; round < 4; round++ {
		for _, probe := range []struct {
			path, want string
			status     int
		}{
			{"/method", `{"result":"PONG"}`, 200},
			{"/getter", `{"result":"capability"}`, 200},
			{"/props", `{"result":"OTHER_OK"}`, 200},
			{"/nested", `{"result":{"response":"AI_OK","value":"test"}}`, 200},
			{"/nested-awaited", `{"result":{"response":"AI_OK","value":"test"}}`, 200},
			{"/nested-method", `{"result":{"response":"AI_OK","value":"test"}}`, 200},
			{"/throws", `{"error":"getter failed"}`, 502},
		} {
			path := probe.path
			if round%2 == 1 {
				path += "?class=1"
			}
			response := call(path, "dispatcher-secret", "invocation-dispatcher", "dispatcher")
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != probe.status || string(body) != probe.want {
				t.Fatalf("round %d probe %s: status=%d body=%s", round, probe.path, response.StatusCode, body)
			}
		}
	}
	denied := call("/", "dispatcher-secret", "invocation-denied")
	denied.Body.Close()
	if denied.StatusCode != 500 {
		t.Fatalf("denied start status = %d", denied.StatusCode)
	}
	mu.Lock()
	callsAfterDenied := resourceCalls
	mu.Unlock()
	if callsAfterDenied != 2 {
		t.Fatalf("denied start executed resource calls: %d", callsAfterDenied)
	}
	missing := call("/missing", "dispatcher-secret", "invocation-missing-a")
	missing.Body.Close()
	if missing.StatusCode != 500 {
		t.Fatalf("missing RPC status = %d", missing.StatusCode)
	}
	for deadline := time.Now().Add(4 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		mu.Lock()
		acknowledged := abandoned["invocation-missing-b"] == 1 && starts["invocation-missing-b"] == 0
		mu.Unlock()
		if acknowledged {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("missing RPC admission was not abandoned without starting")
		}
	}
}
