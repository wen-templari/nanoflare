package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/database"
	"github.com/clas/nanoflare/internal/egress"
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runner"
	"github.com/clas/nanoflare/internal/runtime"
)

type dispatchCountingLauncher struct {
	count      atomic.Int32
	executable string
	mu         sync.Mutex
	processes  []runtime.Process
	active     [][]nanoflare.ActiveDeployment
}

func (l *dispatchCountingLauncher) Launch(path string, active []nanoflare.ActiveDeployment) (runtime.Process, error) {
	process, err := (runtime.CommandLauncher{Executable: l.executable}).Launch(path, active)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.processes = append(l.processes, process)
	l.active = append(l.active, append([]nanoflare.ActiveDeployment{}, active...))
	l.mu.Unlock()
	l.count.Add(1)
	return process, nil
}

type dispatchTraefik struct{}

func (dispatchTraefik) WriteTraefik([]nanoflare.ActiveDeployment) error { return nil }

type dispatchProxyWriter struct{ http.ResponseWriter }

func (w dispatchProxyWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.ResponseWriter.(http.Flusher).Flush()
	return n, err
}

type dispatchObjectStore struct {
	*apiObjectStore
	mu sync.Mutex
}

func (o *dispatchObjectStore) Put(a, p, c string, b []byte) (nanoflare.ObjectInfo, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.apiObjectStore.Put(a, p, c, b)
}
func (o *dispatchObjectStore) Get(a, p string) (nanoflare.ObjectBody, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.apiObjectStore.Get(a, p)
}
func (o *dispatchObjectStore) Head(a, p string) (nanoflare.ObjectInfo, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.apiObjectStore.Head(a, p)
}
func (o *dispatchObjectStore) List(a, p string) ([]nanoflare.ObjectInfo, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.apiObjectStore.List(a, p)
}
func (o *dispatchObjectStore) Delete(a, p string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.apiObjectStore.Delete(a, p)
}

func TestPrivateDynamicDispatchEndToEnd(t *testing.T) {
	for _, mode := range []string{"local", "remote"} {
		t.Run(mode, func(t *testing.T) { privateDynamicDispatchEndToEnd(t, mode == "remote") })
	}
}
func privateDynamicDispatchEndToEnd(t *testing.T, remote bool) {
	executable, err := exec.LookPath("workerd")
	if err != nil {
		t.Skip("workerd not installed")
	}
	repo := nanoflare.NewStore()
	auth := nanoflare.NewControlAuthService(repo, "dispatch-test-auth")
	auth.SetTestHashCost(4)
	session, err := auth.Signup(nanoflare.SignupInput{Email: "dispatch@example.com", Password: "password", OrganizationName: "Dispatch"})
	if err != nil {
		t.Fatal(err)
	}
	org := nanoflare.Organization{ID: "dispatch-org", Name: "Dispatch", UsageLevel: nanoflare.UsageLevelPaid}
	if err := repo.CreateOrganization(org); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertOrganizationMembership(nanoflare.OrganizationMembership{UserID: session.User.ID, OrgID: org.ID, Role: nanoflare.RoleOwner, Scopes: nanoflare.RoleScopes(nanoflare.RoleOwner)}); err != nil {
		t.Fatal(err)
	}
	outboundCanceled := make(chan struct{}, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Nanoflare-Invocation") != "" || strings.HasPrefix(r.Header.Get("Authorization"), "Bearer nfc1.") {
			t.Error("runtime authority leaked to origin")
		}
		if r.URL.Path == "/slow" {
			fmt.Fprint(w, "outbound-start\n")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				outboundCanceled <- struct{}{}
			case <-time.After(5 * time.Second):
			}
			return
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://169.254.169.254/metadata", 302)
			return
		}
		w.Header().Set("X-Customer-Marker", r.Header.Get("X-Marker"))
		fmt.Fprint(w, r.Header.Get("Authorization"))
	}))
	defer origin.Close()
	// All authorized outbound traffic must reach this recording corporate proxy.
	// It forwards only the synthetic pinned destination to our local origin.
	var proxied atomic.Int32
	proxyBase := http.DefaultTransport.(*http.Transport).Clone()
	proxyBase.Proxy = nil
	defer proxyBase.CloseIdleConnections()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Hostname() != "8.8.8.8" || (r.URL.Port() != "" && r.URL.Port() != "80") || r.Host != "8.8.8.8" {
			t.Errorf("unexpected proxy destination: %s host=%s", r.URL, r.Host)
			http.Error(w, "unexpected destination", 502)
			return
		}
		proxied.Add(1)
		forward := r.Clone(r.Context())
		forward.RequestURI = ""
		forward.URL.Host = origin.Listener.Addr().String()
		response, e := proxyBase.RoundTrip(forward)
		if e != nil {
			http.Error(w, "origin unavailable", 502)
			return
		}
		defer response.Body.Close()
		for key, values := range response.Header {
			w.Header()[key] = values
		}
		w.WriteHeader(response.StatusCode)
		w.(http.Flusher).Flush()
		io.Copy(dispatchProxyWriter{w}, response.Body)
	}))
	defer proxy.Close()
	outbound, err := egress.NewHTTPTransport(egress.Config{NetworkAllow: []string{"public"}, ProxyURL: proxy.URL})
	if err != nil {
		t.Fatal(err)
	}
	logOutput := runtime.NewOutputBuffer()
	var service *nanoflare.Service
	var server *Server
	runtimeHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/runtime/dispatch/") {
			NewRuntimeDispatchServer(service, server, outbound).SetLogOutput(logOutput).ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/internal/runtime/durations") {
			w.WriteHeader(204)
			return
		}
		http.NotFound(w, r)
	}))
	defer runtimeHTTP.Close()
	dir := t.TempDir()
	sockets, err := os.MkdirTemp("/tmp", "nf-dispatch-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockets)
	writer := config.NewRuntimeWriter(filepath.Join(dir, "workerd.capnp"), dispatchTraefik{})
	writer.SetNanoflareRuntimeAddr(strings.TrimPrefix(runtimeHTTP.URL, "http://"))
	launcher := &dispatchCountingLauncher{executable: executable}
	manager := runtime.NewManager(writer, launcher, dir, filepath.Join(dir, "workerd.capnp"), sockets, 5*time.Second, time.Second)
	defer manager.Close()

	var publisher nanoflare.ConfigWriter = manager
	var ensurer RuntimeEnsurer = manager
	if remote {
		runnerHTTP := httptest.NewServer(runner.NewServer(manager, "dispatch-runner-secret"))
		defer runnerHTTP.Close()
		client := runner.NewClient(runnerHTTP.URL, "dispatch-runner-secret", dispatchTraefik{})
		publisher = client
		ensurer = client
	}
	service = nanoflare.NewServiceWithObjects(repo, publisher, &dispatchObjectStore{apiObjectStore: newAPIObjectStore()})
	service.SetPlatformDispatchEnabled(true)
	durations := runtime.NewDurationTelemetry()
	service.SetDispatchObservationSink(func(event nanoflare.DispatchObservation) {
		durations.RecordBatch([]runtime.DurationTraceEvent{{ScriptName: event.WorkerID, EventTimestamp: float64(event.At.UnixMilli()), DurationMs: event.ElapsedMS, Outcome: event.Outcome}})
	})
	codec, err := nanoflare.NewSecretCodec("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	service.SetSecretCodec(codec)
	sqlite, err := database.NewSQLiteManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	service.SetDBExecutor(sqlite)
	server = NewServerWithRuntime(service, nil, "", nil, auth, ensurer)
	control := func(method, path string, input any, want int) []byte {
		t.Helper()
		var body []byte
		if input != nil {
			body, err = json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest(method, "/v1/organizations/"+org.ID+path, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+session.Token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	var ns nanoflare.DispatchNamespace
	json.Unmarshal(control("POST", "/dispatch-namespaces", map[string]any{"name": "production"}, 201), &ns)
	var customer nanoflare.App
	json.Unmarshal(control("POST", "/dispatch-namespaces/"+ns.ID+"/scripts", map[string]any{"script_name": "alpha", "execution": "dynamic"}, 201), &customer)
	kv, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{OrgID: org.ID, Name: "customer-kv"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := service.CreateDatabase(nanoflare.CreateDatabaseInput{OrgID: org.ID, Name: "customer-db"})
	if err != nil {
		t.Fatal(err)
	}
	bucket, err := service.CreateObjectStorageBucket(nanoflare.CreateObjectStorageBucketInput{OrgID: org.ID, Name: "customer-objects"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SetNamespaceGrants(org.ID, ns.ID, nanoflare.NamespaceGrantsInput{ExpectedRevision: 1, Grants: []nanoflare.NamespaceResourceGrant{{Kind: "kv", ResourceID: kv.ID}, {Kind: "db", ResourceID: db.ID}, {Kind: "objects", ResourceID: bucket.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	control("PUT", "/workers/"+customer.ID+"/secrets/TOKEN", nanoflare.PutSecretInput{Value: "pinned-secret"}, 204)

	var child nanoflare.App
	json.Unmarshal(control("POST", "/dispatch-namespaces/"+ns.ID+"/scripts", map[string]any{"script_name": "beta", "execution": "dynamic"}, 201), &child)
	childInput := nanoflare.DeployInput{IdempotencyKey: "beta-v1", Format: "modules", CompatibilityDate: "2025-12-10", Entrypoint: "worker.js", KVNamespaces: []nanoflare.KVBinding{{Binding: "STATE", ID: kv.ID}}, Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default {async fetch(request,env){await env.STATE.put("nested","works");return new Response("beta-v1");}}`}}}
	control("POST", "/workers/"+child.ID+"/deployments", childInput, 201)
	customerInput := nanoflare.DeployInput{IdempotencyKey: "alpha-v1", Format: "modules", CompatibilityDate: "2025-12-10", Entrypoint: "worker.js", Services: []nanoflare.ServiceBinding{{Binding: "CHILD", WorkerID: child.ID}}, KVNamespaces: []nanoflare.KVBinding{{Binding: "STATE", ID: kv.ID}}, Databases: []nanoflare.DatabaseBinding{{Binding: "DB", DatabaseID: db.ID}}, ObjectStorageBuckets: []nanoflare.ObjectStorageBucketBinding{{Binding: "BUCKET", BucketID: bucket.ID}}, Assets: []nanoflare.AssetFile{{Path: "snapshot.txt", Data: []byte("v1-asset")}}, Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { async fetch(request,env,ctx) {
 const version="v1",path=new URL(request.url).pathname;
 console.log("customer-log",ctx.props.marker,env.TOKEN);
 if(path.endsWith("/outbound-stream"))return fetch("http://8.8.8.8/slow");
 if(path.endsWith("/outbound")) {
  await scheduler.wait(Math.floor(Math.random()*10));
  const response=await fetch("http://8.8.8.8/",{headers:{"X-Marker":ctx.props.marker,Authorization:"Bearer customer-"+ctx.props.marker}});
  return Response.json({marker:response.headers.get("X-Customer-Marker"),credential:await response.text()});
 }
 if(path.endsWith("/outbound-denied"))return new Response(String((await fetch("http://127.0.0.1/")).status));
 if(path.endsWith("/outbound-redirect"))return new Response(String((await fetch("http://8.8.8.8/redirect")).status));
 if(path.endsWith("/tcp")){try{const {connect}=await import("cloudflare:sockets");const socket=connect("8.8.8.8:80");await socket.opened;return new Response("unexpected");}catch{return new Response("denied");}}
 if(path.endsWith("/nested"))return env.CHILD.fetch("https://child.internal/");
 if(request.headers.has("X-Nanoflare-Dispatch-Root")) throw new Error("ingress root leaked to customer");
 if(path.endsWith("/stream") || path.endsWith("/cancel")) return new Response(new ReadableStream({
  start(controller) {
   controller.enqueue(new TextEncoder().encode(version+"-start\n"));
   ctx.waitUntil((async()=>{
    await scheduler.wait(400);
    try {
     if(path.endsWith("/cancel")) await env.STATE.put("cancel-effect","must-not-be-written");
     else controller.enqueue(new TextEncoder().encode(await (await env.ASSETS.fetch("/snapshot.txt")).text()));
     controller.close();
    } catch(error) { try{controller.error(error)}catch{} }
   })());
  }
 }),{headers:{"content-type":"text/plain"}});
 await env.STATE.put("seen-"+ctx.props.marker,ctx.props.marker);
 const cachePolicy={};
 for(const [name,operation] of Object.entries({default:()=>caches.default,open:()=>caches.open("shared"),prototype:()=>Cache.prototype.match.call({},"https://cache.example/")})) {
  try { await operation(); cachePolicy[name]="unexpected"; } catch(error) { cachePolicy[name]=error.message.includes("Cache API is unavailable"); }
 }
 const answer=await env.DB.prepare("SELECT 42 AS answer").first("answer");
 await env.BUCKET.put("object-"+ctx.props.marker,"value-"+ctx.props.marker);
 const object=await (await env.BUCKET.get("object-"+ctx.props.marker)).text();
 return Response.json({answer,object,cachePolicy,version,marker:ctx.props.marker,rpc:await ctx.props.CONNECTOR.identify(),state:await env.STATE.get("seen-"+ctx.props.marker),secret:env.TOKEN});
} };`}}}
	// This handler explicitly serves its own asset paths alongside API routes.
	customerInput.AssetConfig.RunWorkerFirst = nanoflare.RunWorkerFirst{"/*"}
	var first nanoflare.Deployment
	json.Unmarshal(control("POST", "/workers/"+customer.ID+"/deployments", customerInput, 201), &first)
	if launcher.count.Load() != 0 {
		t.Fatal("dynamic customer created a static runtime pool")
	}
	// The dispatcher capability calls a platform-owned RPC service adapter.
	var controlWorker nanoflare.App
	json.Unmarshal(control("POST", "/workers", nanoflare.CreateAppInput{Name: "control", Hostname: "control.example.com"}, 201), &controlWorker)
	control("POST", "/workers/"+controlWorker.ID+"/deployments", nanoflare.DeployInput{Format: "modules", CompatibilityDate: "2025-12-10", Entrypoint: "worker.js", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `import {WorkerEntrypoint} from "cloudflare:workers";
export default class Control extends WorkerEntrypoint { identify(marker){return marker;} fetch(){return new Response("control");} }`}}}, 201)
	// Deploy the ordinary dispatcher through the compatible multipart endpoint.
	// Named RPC exports and namespace props must survive this upload path too.
	server.SetCloudflareCompatibilityEnabled(true)
	platformCode := `import {WorkerEntrypoint} from "cloudflare:workers";
export class Connector extends WorkerEntrypoint { identify(){return this.env.CONTROL.identify(this.ctx.props.marker);} }
export default { async fetch(request,env,ctx) {
 const script=new URL(request.url).pathname.split("/")[1];
 if(request.headers.get("authorization")!=="Bearer "+script) return new Response("denied",{status:403});
 const marker=request.headers.get("x-marker")||"first";
 try {
  const options=new URL(request.url).pathname.endsWith("/unsupported")?{limits:{cpuMs:1}}:{};
  return await env.DISPATCHER.get(script,{props:{marker,CONNECTOR:ctx.exports.Connector({props:{marker}})}},options).fetch(request);
 }catch(error){return Response.json({error:error.message},{status:error.status||500});}
} };`
	kind, body := cfMultipart(t, `{"main_module":"worker.js","compatibility_date":"2025-12-10","bindings":[{"name":"DISPATCHER","type":"dispatch_namespace","namespace":"production"},{"name":"CONTROL","type":"service","service":"control"}]}`, platformCode)
	request := httptest.NewRequest("PUT", "/client/v4/accounts/"+org.ID+"/workers/scripts/platform", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+session.Token)
	request.Header.Set("X-Nanoflare-Execution-Profile", "trusted_shared_workerd")
	request.Header.Set("Content-Type", kind)
	request.Header.Set("Idempotency-Key", "platform")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("ordinary dispatcher upload: %d %s", response.Code, response.Body.String())
	}
	platform, err := service.OrdinaryScriptForOrg(org.ID, "platform")
	if err != nil {
		t.Fatal(err)
	}
	public := httptest.NewServer(server)
	defer public.Close()
	invoke := func(marker string) map[string]any {
		t.Helper()
		r, _ := http.NewRequest("GET", public.URL+"/internal/http/workers/"+platform.ID+"/alpha", nil)
		r.Header.Set("Authorization", "Bearer alpha")
		r.Header.Set("X-Marker", marker)
		response, e := public.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != 200 {
			t.Fatalf("dispatch %d: %s", response.StatusCode, body)
		}
		var result map[string]any
		if e = json.Unmarshal(body, &result); e != nil {
			t.Fatalf("dispatch body %s: %v", body, e)
		}
		return result
	}
	result := invoke("one")
	if result["version"] != "v1" || result["marker"] != "one" || result["rpc"] != "one" || result["state"] != "one" || result["secret"] != "pinned-secret" {
		t.Fatalf("customer result: %#v", result)
	}
	if result["answer"] != float64(42) || result["object"] != "value-one" {
		t.Fatalf("resource facades: %#v", result)
	}
	cachePolicy, ok := result["cachePolicy"].(map[string]any)
	if !ok || cachePolicy["default"] != true || cachePolicy["open"] != true || cachePolicy["prototype"] != true {
		t.Fatalf("cache policy: %#v", result)
	}
	object, found, e := service.WorkerObjectGetForOrg(org.ID, customer.ID, bucket.ID, "object-one")
	if e != nil || !found || string(object.Body) != "value-one" {
		t.Fatalf("object path/ownership: found=%t err=%v", found, e)
	}

	nestedRequest, _ := http.NewRequest("GET", public.URL+"/internal/http/workers/"+platform.ID+"/alpha/nested", nil)
	nestedRequest.Header.Set("Authorization", "Bearer alpha")
	nestedResponse, e := public.Client().Do(nestedRequest)
	if e != nil {
		t.Fatal(e)
	}
	nestedBody, _ := io.ReadAll(nestedResponse.Body)
	nestedResponse.Body.Close()
	if nestedResponse.StatusCode != 200 || string(nestedBody) != "beta-v1" {
		t.Fatalf("nested service: %d %s", nestedResponse.StatusCode, nestedBody)
	}

	callPath := func(path, marker string) string {
		t.Helper()
		request, _ := http.NewRequest("GET", public.URL+"/internal/http/workers/"+platform.ID+"/alpha/"+path, nil)
		request.Header.Set("Authorization", "Bearer alpha")
		request.Header.Set("X-Marker", marker)
		response, err := public.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != 200 {
			t.Fatalf("%s: %d %s", path, response.StatusCode, body)
		}
		return string(body)
	}
	for _, path := range []string{"outbound-denied", "outbound-redirect"} {
		if body := callPath(path, "blocked"); body != "403" {
			t.Fatalf("%s: %s", path, body)
		}
	}
	if body := callPath("tcp", "blocked"); body != "denied" {
		t.Fatalf("raw TCP bypass: %s", body)
	}
	var outboundRequests sync.WaitGroup
	for i := 0; i < 12; i++ {
		outboundRequests.Add(1)
		go func(i int) {
			defer outboundRequests.Done()
			marker := fmt.Sprintf("egress-%d", i)
			var result map[string]string
			body := callPath("outbound", marker)
			if err := json.Unmarshal([]byte(body), &result); err != nil || result["marker"] != marker || result["credential"] != "Bearer customer-"+marker {
				t.Errorf("outbound context crossed requests: %s %v", body, err)
			}
		}(i)
	}
	outboundRequests.Wait()
	outboundStreamRequest, _ := http.NewRequest("GET", public.URL+"/internal/http/workers/"+platform.ID+"/alpha/outbound-stream", nil)
	outboundStreamRequest.Header.Set("Authorization", "Bearer alpha")
	outboundStream, err := public.Client().Do(outboundStreamRequest)
	if err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(outboundStream.Body).ReadString('\n'); err != nil || line != "outbound-start\n" {
		t.Fatalf("outbound stream: %q %v", line, err)
	}
	outboundStream.Body.Close()
	select {
	case <-outboundCanceled:
	case <-time.After(time.Second):
		t.Fatal("outbound survived ingress cancellation")
	}
	// Let the documented two-second background admission window close before
	// starting the separate 24-invocation resource-concurrency scenario.
	time.Sleep(2100 * time.Millisecond)

	logDeadline := time.Now().Add(time.Second)
	for {
		logs := logOutput.Output(customer.ID)
		found := false
		for _, line := range logs {
			if strings.Contains(line.Message, "customer-log one") {
				found = true
				if strings.Contains(line.Message, "pinned-secret") || line.NamespaceID != ns.ID || line.RequestChainID == "" || line.InvocationID == "" || line.DeploymentID != first.ID {
					t.Fatalf("incorrect log attribution/redaction: %#v", line)
				}
			}
		}
		if found {
			break
		}
		if time.Now().After(logDeadline) {
			t.Fatal("dynamic customer log was not collected")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(logOutput.Output(platform.ID)) != 0 {
		t.Fatal("customer log was attributed to dispatcher")
	}
	counts := service.DispatchCounters(customer.ID)
	if counts.Invocations == 0 || counts.Completed == 0 || counts.ElapsedTotalMS <= 0 {
		t.Fatalf("native dispatch observations missing: %#v", counts)
	}
	if stats := durations.Stats(customer.ID); !stats.Available || stats.DurationMsAvg <= 0 {
		t.Fatalf("native customer duration telemetry missing: %#v", stats)
	}
	if stats := durations.Stats(platform.ID); stats.Available {
		t.Fatal("customer duration was attributed to dispatcher")
	}
	started := launcher.count.Load()
	customerInput.IdempotencyKey = "alpha-v2"
	customerInput.Files[0].Content = strings.ReplaceAll(customerInput.Files[0].Content, `"v1"`, `"v2"`)
	control("POST", "/workers/"+customer.ID+"/deployments", customerInput, 201)
	if result = invoke("two"); result["version"] != "v2" || result["rpc"] != "two" {
		t.Fatalf("new revision: %#v", result)
	}
	if launcher.count.Load() != started {
		t.Fatal("customer revision restarted the dispatcher")
	}
	control("PUT", "/workers/"+customer.ID+"/deployment-traffic", map[string]any{"deployments": []nanoflare.DeploymentTraffic{{ID: first.ID, TrafficPercent: 100}}}, 200)
	if result = invoke("rollback"); result["version"] != "v1" || result["rpc"] != "rollback" {
		t.Fatalf("rollback: %#v", result)
	}
	if launcher.count.Load() != started {
		t.Fatal("rollback restarted the dispatcher")
	}

	// In-flight streams keep the original artifact after a newer revision activates.
	streamRequest, _ := http.NewRequest("GET", public.URL+"/internal/http/workers/"+platform.ID+"/alpha/stream", nil)
	streamRequest.Header.Set("Authorization", "Bearer alpha")
	stream, e := public.Client().Do(streamRequest)
	if e != nil {
		t.Fatal(e)
	}
	reader := bufio.NewReader(stream.Body)
	line, e := reader.ReadString('\n')
	if e != nil || line != "v1-start\n" {
		t.Fatalf("stream start %q: %v", line, e)
	}
	customerInput.IdempotencyKey = "alpha-v3"
	customerInput.Assets = []nanoflare.AssetFile{{Path: "snapshot.txt", Data: []byte("v2-asset")}}
	control("POST", "/workers/"+customer.ID+"/deployments", customerInput, 201)
	remaining, e := io.ReadAll(reader)
	stream.Body.Close()
	if e != nil || string(remaining) != "v1-asset" {
		t.Fatalf("stream mixed revisions: %q %v", remaining, e)
	}
	if launcher.count.Load() != started {
		t.Fatal("streaming update restarted dispatcher")
	}
	// Native props and RPC authority remain distinct across concurrent invocations.
	var requests sync.WaitGroup
	for i := 0; i < 24; i++ {
		requests.Add(1)
		go func(index int) {
			defer requests.Done()
			marker := fmt.Sprintf("concurrent-%d", index)
			r := invoke(marker)
			if r["marker"] != marker || r["rpc"] != marker || r["state"] != marker {
				t.Errorf("cross-request context: %#v", r)
			}
		}(i)
	}
	requests.Wait()
	// Cancel a live response; late resource effects must not survive the ingress.
	cancelRequest, _ := http.NewRequest("GET", public.URL+"/internal/http/workers/"+platform.ID+"/alpha/cancel", nil)
	cancelRequest.Header.Set("Authorization", "Bearer alpha")
	cancelled, e := public.Client().Do(cancelRequest)
	if e != nil {
		t.Fatal(e)
	}
	cancelReader := bufio.NewReader(cancelled.Body)
	if _, e = cancelReader.ReadString('\n'); e != nil {
		t.Fatal(e)
	}
	cancelled.Body.Close()
	time.Sleep(550 * time.Millisecond)
	if _, ok, e := service.WorkerKVGetForOrg(org.ID, customer.ID, kv.ID, "cancel-effect"); e != nil || ok {
		t.Fatalf("effect survived client cancellation: found=%t err=%v", ok, e)
	}

	const classModule = `import {WorkerEntrypoint} from "cloudflare:workers";
class Customer extends WorkerEntrypoint {
 async fetch(request){
  await this.env.STATE.put("page/a","a"); await this.env.STATE.put("page/b","b");
  const first=await this.env.STATE.list({prefix:"page/",limit:1});
  const second=await this.env.STATE.list({prefix:"page/",limit:1,cursor:first.cursor});
  if(first.list_complete || first.keys.length!==1 || first.keys[0].name!=="page/a" || !first.cursor || !second.list_complete || second.keys.length!==1 || second.keys[0].name!=="page/b") throw new Error("KV pagination failed");
  return Response.json({version:"class",marker:this.ctx.props.marker,rpc:await this.ctx.props.CONNECTOR.identify(),secret:this.env.TOKEN});
 }
}`
	for _, variant := range []struct {
		name  string
		files []nanoflare.WorkerFile
	}{
		{"inline", []nanoflare.WorkerFile{{Path: "worker.js", Content: strings.Replace(classModule, "class Customer", "export default class Customer", 1)}}},
		{"named", []nanoflare.WorkerFile{{Path: "worker.js", Content: classModule + "\nexport default Customer;"}}},
		{"alias", []nanoflare.WorkerFile{{Path: "worker.js", Content: classModule + "\nexport {Customer as default};"}}},
		{"reexport", []nanoflare.WorkerFile{{Path: "worker.js", Content: `export {default} from "./customer.js";`}, {Path: "customer.js", Content: classModule + "\nexport default Customer;"}}},
		{"object-comment", []nanoflare.WorkerFile{{Path: "worker.js", Content: `// export default class is only a comment.
export default {async fetch(request,env,ctx){return Response.json({version:"class",marker:ctx.props.marker,rpc:await ctx.props.CONNECTOR.identify(),secret:env.TOKEN});}};`}}},
	} {
		customerInput.IdempotencyKey = "alpha-export-" + variant.name
		customerInput.Files = variant.files
		control("POST", "/workers/"+customer.ID+"/deployments", customerInput, 201)
		if result = invoke("class-props"); result["version"] != "class" || result["rpc"] != "class-props" || result["secret"] != "pinned-secret" {
			t.Fatalf("native export %s: %#v", variant.name, result)
		}
	}
	if proxied.Load() == 0 {
		t.Fatal("dynamic outbound bypassed configured corporate proxy")
	}

	// Allow earlier response background windows to finish before more admissions.
	time.Sleep(1600 * time.Millisecond)
	unsupported, _ := http.NewRequest("GET", public.URL+"/internal/http/workers/"+platform.ID+"/alpha/unsupported", nil)
	unsupported.Header.Set("Authorization", "Bearer alpha")
	rejected, e := public.Client().Do(unsupported)
	if e != nil {
		t.Fatal(e)
	}
	rejection, _ := io.ReadAll(rejected.Body)
	rejected.Body.Close()
	if rejected.StatusCode != 422 || !strings.Contains(string(rejection), "not supported") {
		t.Fatalf("ignored custom limit: %d %s", rejected.StatusCode, rejection)
	}
	upgrade, _ := http.NewRequest("GET", public.URL+"/internal/http/workers/"+platform.ID+"/alpha", nil)
	upgrade.Header.Set("Authorization", "Bearer alpha")
	upgrade.Header.Set("Connection", "upgrade")
	upgrade.Header.Set("Upgrade", "websocket")
	rejected, e = public.Client().Do(upgrade)
	if e != nil {
		t.Fatal(e)
	}
	rejection, _ = io.ReadAll(rejected.Body)
	rejected.Body.Close()
	if rejected.StatusCode != 501 {
		t.Fatalf("unsupported WebSocket: %d %s", rejected.StatusCode, rejection)
	}
	customerInput.IdempotencyKey = "alpha-rpc-lifetime"
	customerInput.Files[0].Content = `let savedConnector,savedBroker;
 export default {async fetch(request,env,ctx){
  let blocked=null,brokerBlocked=null;
  if(savedConnector){try{await savedConnector.identify();blocked=false}catch{blocked=true}}
  if(savedBroker){try{await savedBroker.effect("kv","STATE",new Request("http://kv.internal/seen-one?urlencoded=true"));brokerBlocked=false}catch{brokerBlocked=true}}
  savedConnector=ctx.props.CONNECTOR;savedBroker=ctx.props.__NANOFLARE_BROKER;
  return Response.json({version:"lifetime",rpc:await ctx.props.CONNECTOR.identify(),blocked,brokerBlocked});
 }};`
	control("POST", "/workers/"+customer.ID+"/deployments", customerInput, 201)
	invoke("saved-capability")
	if result = invoke("fresh-capability"); result["blocked"] != true || result["brokerBlocked"] != true || result["rpc"] != "fresh-capability" {
		t.Fatalf("RPC capability crossed requests: %#v", result)
	}
	// Kill this test's dedicated runner process and verify that the same revision's
	// replacement cannot revive credentials or roots from the previous instance.
	launcher.mu.Lock()
	oldProcess := launcher.processes[len(launcher.processes)-1]
	var oldSource nanoflare.ActiveDeployment
	for _, source := range launcher.active[len(launcher.active)-1] {
		if source.App.ID == platform.ID {
			oldSource = source
		}
	}
	launcher.mu.Unlock()
	oldToken := nanoflare.RuntimeCapability(oldSource, nanoflare.RuntimeDispatch, ns.ID)
	staleRoot, _, e := service.BeginDispatchRoot(oldSource)
	if e != nil {
		t.Fatal(e)
	}
	stale, e := service.AcquireDispatchWithRoot(context.Background(), oldToken, ns.ID, "alpha", staleRoot)
	if e != nil {
		t.Fatal(e)
	}
	stopContext, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_ = oldProcess.Stop(stopContext)
	stop()
	recoveryDeadline := time.Now().Add(6 * time.Second)
	for {
		ready, e := manager.Ensure(context.Background(), oldSource)
		if e == nil && launcher.count.Load() > started {
			ready.Release()
			break
		}
		if e == nil {
			ready.Release()
		}
		if time.Now().After(recoveryDeadline) {
			t.Fatal("runner did not recover its dispatcher")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, e = service.AuthorizeRuntime(context.Background(), stale.Lease, nanoflare.RuntimeAssets, stale.Deployment.ID); !errors.Is(e, nanoflare.ErrRuntimeGrantDenied) {
		t.Fatalf("replacement revived old invocation: %v", e)
	}
	launcher.mu.Lock()
	var replacement nanoflare.ActiveDeployment
	for _, source := range launcher.active[len(launcher.active)-1] {
		if source.App.ID == platform.ID {
			replacement = source
		}
	}
	launcher.mu.Unlock()
	replacementToken := nanoflare.RuntimeCapability(replacement, nanoflare.RuntimeDispatch, ns.ID)
	if _, e = service.AcquireDispatchWithRoot(context.Background(), replacementToken, ns.ID, "alpha", staleRoot); !errors.Is(e, nanoflare.ErrInvocationExpired) {
		t.Fatalf("old ingress root crossed process instances: %v", e)
	}
	if result = invoke("after-restart"); result["version"] != "lifetime" || result["rpc"] != "after-restart" {
		t.Fatalf("runner recovery result: %#v", result)
	}
	service.EndDispatchRoot(staleRoot, true)
	private := httptest.NewRecorder()
	server.ServeHTTP(private, httptest.NewRequest("GET", "/internal/http/workers/"+customer.ID+"/", nil))
	if private.Code != 404 {
		t.Fatalf("private bypass: %d", private.Code)
	}
	// The adapter's atomic publication path uses the same native loader and
	// pinned runtime secret snapshot as ordinary dispatch and rollback.
	managed := nanoflare.ManagedDeployInput{Deployment: nanoflare.DeployInput{IdempotencyKey: "atomic-managed-v1", Format: "modules", CompatibilityDate: "2025-12-10", Entrypoint: "worker.js", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default {async fetch(request,env,ctx){return Response.json({version:"managed-v1",secret:env.TOKEN,rpc:await ctx.props.CONNECTOR.identify()});}}`}}}, Metadata: nanoflare.ScriptManagementMetadata{Bindings: []json.RawMessage{json.RawMessage(`{"name":"TOKEN","type":"secret_text"}`)}}, Secrets: map[string]string{"TOKEN": "atomic-secret-v1"}}
	beforeManaged := launcher.count.Load()
	managedFirst, err := service.ManagedDeployForOrg(org.ID, customer.ID, managed)
	if err != nil {
		t.Fatal(err)
	}
	if result = invoke("atomic-first"); result["version"] != "managed-v1" || result["secret"] != "atomic-secret-v1" || result["rpc"] != "atomic-first" {
		t.Fatalf("managed native snapshot: %#v", result)
	}
	managed.ExpectedRevision = 1
	managed.BaseDeploymentID = managedFirst.ID
	managed.Deployment.IdempotencyKey = "atomic-managed-v2"
	managed.Deployment.Files[0].Content = strings.ReplaceAll(managed.Deployment.Files[0].Content, "managed-v1", "managed-v2")
	managed.Secrets = map[string]string{"TOKEN": "atomic-secret-v2"}
	if _, err = service.ManagedDeployForOrg(org.ID, customer.ID, managed); err != nil {
		t.Fatal(err)
	}
	if result = invoke("atomic-second"); result["version"] != "managed-v2" || result["secret"] != "atomic-secret-v2" {
		t.Fatalf("managed native update: %#v", result)
	}
	if _, err = service.SetDeploymentTrafficForOrg(org.ID, customer.ID, []nanoflare.DeploymentTraffic{{ID: managedFirst.ID, TrafficPercent: 100}}); err != nil {
		t.Fatal(err)
	}
	if result = invoke("atomic-rollback"); result["version"] != "managed-v1" || result["secret"] != "atomic-secret-v1" {
		t.Fatalf("managed native rollback: %#v", result)
	}
	control("PUT", "/workers/"+customer.ID+"/secrets/TOKEN", nanoflare.PutSecretInput{Value: "native-secret-edit"}, 204)
	if result = invoke("native-secret"); result["version"] != "managed-v1" || result["secret"] != "native-secret-edit" {
		t.Fatalf("native secret edit after managed rollback: %#v", result)
	}
	nativeCode := nanoflare.DeployInput{IdempotencyKey: "native-after-managed", Format: "modules", CompatibilityDate: "2025-12-10", Entrypoint: "worker.js", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default {async fetch(request,env,ctx){return Response.json({version:"native-code",secret:env.TOKEN,flag:env.FLAG,rpc:await ctx.props.CONNECTOR.identify()});}}`}}}
	var nativeRevision nanoflare.Deployment
	json.Unmarshal(control("POST", "/workers/"+customer.ID+"/deployments", nativeCode, 201), &nativeRevision)
	if result = invoke("native-code"); result["version"] != "native-code" || result["secret"] != "native-secret-edit" {
		t.Fatalf("native deploy resurrected legacy secret: %#v", result)
	}
	control("PUT", "/workers/"+customer.ID+"/vars", nanoflare.UpdateVarsInput{ExpectedDeploymentID: nativeRevision.ID, IdempotencyKey: "native-vars-after-managed", Vars: map[string]json.RawMessage{"FLAG": json.RawMessage(`42`)}}, 200)
	if result = invoke("native-vars"); result["flag"] != float64(42) || result["secret"] != "native-secret-edit" {
		t.Fatalf("native vars after managed upload: %#v", result)
	}
	control("DELETE", "/workers/"+customer.ID+"/secrets/TOKEN", nil, 204)
	if result = invoke("native-delete-secret"); result["secret"] != nil || result["flag"] != float64(42) {
		t.Fatalf("native secret delete after managed upload: %#v", result)
	}
	server.SetCloudflareCompatibilityEnabled(true)
	wireMetadata := `{"main_module":"worker.js","compatibility_date":"2025-12-10","bindings":[{"name":"MESSAGE","type":"plain_text","text":"wire"},{"name":"TOKEN","type":"secret_text","text":"wire-secret"}]}`
	wireCode := `export default {async fetch(request,env,ctx){return Response.json({version:"wire",message:env.MESSAGE,secret:env.TOKEN,secret2:env.SECOND,rpc:await ctx.props.CONNECTOR.identify()});}}`
	wireUpload := func() {
		t.Helper()
		kind, body := cfMultipart(t, wireMetadata, wireCode)
		r := httptest.NewRequest("PUT", "/client/v4/accounts/"+org.ID+"/workers/dispatch/namespaces/"+ns.Name+"/scripts/alpha", bytes.NewReader(body))
		r.Header.Set("Content-Type", kind)
		r.Header.Set("Authorization", "Bearer "+session.Token)
		r.Header.Set("X-Nanoflare-Execution-Profile", "trusted_shared_workerd")
		r.Header.Set("Idempotency-Key", "wire-native")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("wire upload: %d %s", w.Code, w.Body.String())
		}
	}
	wireUpload()
	if result = invoke("wire-dispatch"); result["version"] != "wire" || result["message"] != "wire" || result["secret"] != "wire-secret" || result["rpc"] != "wire-dispatch" {
		t.Fatalf("compatibility wire request native execution: %#v", result)
	}
	if _, err = service.SetDeploymentTrafficForOrg(org.ID, customer.ID, []nanoflare.DeploymentTraffic{{ID: managedFirst.ID, TrafficPercent: 100}}); err != nil {
		t.Fatal(err)
	}
	wireUpload()
	if result = invoke("wire-retry-after-rollback"); result["version"] != "managed-v1" || result["secret"] != "atomic-secret-v1" {
		t.Fatalf("wire retry overwrote native rollback: %#v", result)
	}
	wireMutation := func(method, suffix, kind string, body []byte, key string) {
		t.Helper()
		r := httptest.NewRequest(method, "/client/v4/accounts/"+org.ID+"/workers/dispatch/namespaces/"+ns.Name+"/scripts/alpha"+suffix, bytes.NewReader(body))
		r.Header.Set("Content-Type", kind)
		r.Header.Set("Authorization", "Bearer "+session.Token)
		r.Header.Set("X-Nanoflare-Execution-Profile", "trusted_shared_workerd")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("wire mutation %s: %d %s", suffix, w.Code, w.Body.String())
		}
	}
	wireMutation("PUT", "/tags", "application/json", []byte(`["tagged"]`), "wire-tags")
	if result = invoke("wire-tags"); result["version"] != "managed-v1" {
		t.Fatalf("wire tags activated latest code: %#v", result)
	}
	wireMutation("PUT", "/secrets", "application/json", []byte(`{"name":"TOKEN","type":"secret_text","text":"wire-updated"}`), "wire-secret-update")
	if result = invoke("wire-secret-update"); result["version"] != "wire" || result["secret"] != "wire-updated" || result["message"] != "wire" {
		t.Fatalf("wire secret mutation snapshot: %#v", result)
	}
	settingsKind, settingsBody := cfSettingsMultipart(t, `{"bindings":[{"name":"MESSAGE","type":"plain_text","text":"patched"},{"name":"TOKEN","type":"inherit"}]}`)
	wireMutation("PATCH", "/settings", settingsKind, settingsBody, "wire-settings")
	if result = invoke("wire-settings"); result["version"] != "wire" || result["message"] != "patched" || result["secret"] != "wire-updated" {
		t.Fatalf("wire settings mutation snapshot: %#v", result)
	}
	wireMutation("PATCH", "/secrets-bulk", "application/json", []byte(`{"secrets":{"TOKEN":{"name":"TOKEN","type":"secret_text","text":"bulk-native"},"SECOND":{"name":"SECOND","type":"secret_text","text":"bulk-pair"},"MISSING":null}}`), "wire-secrets-bulk")
	if result = invoke("wire-secrets-bulk"); result["secret"] != "bulk-native" || result["secret2"] != "bulk-pair" || result["message"] != "patched" {
		t.Fatalf("bulk native secret snapshot: %#v", result)
	}
	wireMutation("DELETE", "/secrets/TOKEN", "", nil, "wire-secret-delete")
	if result = invoke("wire-secret-delete"); result["secret"] != nil || result["message"] != "patched" {
		t.Fatalf("wire secret removal snapshot: %#v", result)
	}
	contentKind, contentBody := cfMultipart(t, `{"main_module":"worker.js"}`, strings.ReplaceAll(wireCode, `version:"wire"`, `version:"content"`))
	wireMutation("PUT", "/content", contentKind, contentBody, "wire-content")
	if result = invoke("wire-content"); result["version"] != "content" || result["message"] != "patched" || result["secret"] != nil {
		t.Fatalf("wire content failed to preserve settings: %#v", result)
	}
	assetCall := func(path, token, kind string, body []byte, want int) nanoflare.AssetUploadResult {
		t.Helper()
		r := httptest.NewRequest("POST", path, bytes.NewReader(body))
		r.Header.Set("Content-Type", kind)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Nanoflare-Execution-Profile", "trusted_shared_workerd")
		out := httptest.NewRecorder()
		server.ServeHTTP(out, r)
		if out.Code != want {
			t.Fatalf("wire asset request: %d %s", out.Code, out.Body.String())
		}
		var envelope struct {
			Result nanoflare.AssetUploadResult `json:"result"`
		}
		if err := json.Unmarshal(out.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Result
	}
	assetBytes := []byte("served from completed upload")
	assetDigest := sha256.Sum256(assetBytes)
	assetHash := hex.EncodeToString(assetDigest[:16])
	assetManifest, _ := json.Marshal(map[string]any{"manifest": map[string]nanoflare.AssetUploadEntry{"/hello.txt": {Hash: assetHash, Size: int64(len(assetBytes))}}})
	assetBase := "/client/v4/accounts/" + org.ID + "/workers"
	assetSession := assetCall(assetBase+"/dispatch/namespaces/"+ns.Name+"/scripts/alpha/assets-upload-session", session.Token, "application/json", assetManifest, 200)
	assetKind, assetBody := cfAssetMultipart(t, map[string][]byte{assetHash: assetBytes})
	assetCompleted := assetCall(assetBase+"/assets/upload?base64=true", assetSession.JWT, assetKind, assetBody, 201)
	assetKind, assetBody = cfMultipart(t, `{"main_module":"worker.js","compatibility_date":"2025-12-10","assets":{"jwt":"`+assetCompleted.JWT+`","config":{"run_worker_first":true}},"bindings":[{"name":"ASSETS","type":"assets"}]}`, `export default {async fetch(request,env){return Response.json({version:"assets",asset:await (await env.ASSETS.fetch("https://assets.internal/hello.txt")).text()});}}`)
	wireMutation("PUT", "", assetKind, assetBody, "wire-assets")
	if result = invoke("wire-assets"); result["version"] != "assets" || result["asset"] != string(assetBytes) {
		t.Fatalf("completed asset native execution: %#v", result)
	}
	assetClaimsBytes, err := base64.RawURLEncoding.DecodeString(strings.Split(assetCompleted.JWT, ".")[1])
	if err != nil {
		t.Fatal(err)
	}
	var assetClaims struct {
		ID string `json:"sid"`
	}
	if err = json.Unmarshal(assetClaimsBytes, &assetClaims); err != nil {
		t.Fatal(err)
	}
	staging, err := repo.GetAssetUploadSession(assetClaims.ID)
	if err != nil {
		t.Fatal(err)
	}
	staging.ExpiresAt = time.Now().Add(-time.Second)
	staging.Revision++
	if err = repo.SaveAssetUploadSession(staging, staging.Revision-1); err != nil {
		t.Fatal(err)
	}
	if count, err := service.PruneExpiredAssetUploads(context.Background()); err != nil || count != 1 {
		t.Fatalf("staging cleanup count=%d err=%v", count, err)
	}
	if result = invoke("wire-assets-after-cleanup"); result["asset"] != string(assetBytes) {
		t.Fatalf("cleanup removed deployed asset bytes: %#v", result)
	}
	deleteRequest := httptest.NewRequest("DELETE", assetBase+"/dispatch/namespaces/"+ns.Name+"/scripts?tags=tagged:yes", nil)
	deleteRequest.Header.Set("Authorization", "Bearer "+session.Token)
	deleteRequest.Header.Set("X-Nanoflare-Execution-Profile", "trusted_shared_workerd")
	deleted := httptest.NewRecorder()
	server.ServeHTTP(deleted, deleteRequest)
	if deleted.Code != 200 {
		t.Fatalf("native bulk deletion: %d %s", deleted.Code, deleted.Body.String())
	}
	deniedRequest, _ := http.NewRequest("GET", public.URL+"/internal/http/workers/"+platform.ID+"/alpha", nil)
	deniedRequest.Header.Set("Authorization", "Bearer alpha")
	deniedResponse, err := public.Client().Do(deniedRequest)
	if err != nil {
		t.Fatal(err)
	}
	deniedBody, _ := io.ReadAll(deniedResponse.Body)
	deniedResponse.Body.Close()
	if deniedResponse.StatusCode != 404 {
		t.Fatalf("cached deleted Worker remained dispatchable: %d %s", deniedResponse.StatusCode, deniedBody)
	}
	if launcher.count.Load() != beforeManaged {
		t.Fatal("managed customer updates restarted dispatcher")
	}
}
