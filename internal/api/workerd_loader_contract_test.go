package api_test

import (
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
	"syscall"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/database"
	"github.com/clas/nanoflare/internal/dnsresolver"
	"github.com/clas/nanoflare/internal/egress"
	"github.com/clas/nanoflare/internal/nanoflare"
	"golang.org/x/net/dns/dnsmessage"
)

// This is the pre-cutover contract gate, not a replacement runtime manager.
// Opt in explicitly so a missing runtime cannot silently qualify the change.
func TestWorkerdDynamicLoaderContracts(t *testing.T) {
	workerd := os.Getenv("NANOFLARE_TEST_WORKERD")
	if workerd == "" {
		t.Skip("set NANOFLARE_TEST_WORKERD to the production-pinned workerd binary")
	}
	version, err := exec.Command(workerd, "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(version)) != "workerd 2026-09-11" {
		t.Fatalf("contract gate requires workerd 2026-09-11: %s (%v)", version, err)
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatal("Node.js 22 or later is required for the real-runtime contract client")
	}
	store := nanoflare.NewStore()
	service := nanoflare.NewServiceWithObjects(store, discardWriter{}, newE2EObjectStore())
	app, err := service.CreateApp(nanoflare.CreateAppInput{Name: "loader-contract", Hostname: "loader-contract.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{Name: "loader-contract"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := service.CreateDatabase(nanoflare.CreateDatabaseInput{Name: "loader-contract"})
	if err != nil {
		t.Fatal(err)
	}
	sqlite, err := database.NewSQLiteManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	service.SetDBExecutor(sqlite)
	t.Cleanup(func() {
		if err := sqlite.Delete(db.ID); err != nil {
			t.Errorf("close contract database: %v", err)
		}
	})
	bucket, err := service.CreateObjectStorageBucket(nanoflare.CreateObjectStorageBucketInput{Name: "loader-contract"})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := service.Deploy(app.ID, nanoflare.DeployInput{
		Files:                []nanoflare.WorkerFile{{Path: "worker.js", Content: nativeKVWorker}},
		Entrypoint:           "worker.js",
		Format:               "modules",
		CompatibilityDate:    "2026-09-08",
		KVNamespaces:         []nanoflare.KVBinding{{Binding: "KV", ID: namespace.ID}},
		Databases:            []nanoflare.DatabaseBinding{{Binding: "DB", DatabaseID: db.ID}},
		ObjectStorageBuckets: []nanoflare.ObjectStorageBucketBinding{{Binding: "OBJECTS", BucketID: bucket.ID}},
		Assets: []nanoflare.AssetFile{
			{Path: "logo.svg", ContentType: "image/svg+xml", Data: []byte("<svg/>")},
			{Path: "index.html", ContentType: "text/html", Data: []byte("<h1>loader</h1>")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	kv := api.NewRuntimeKVServer(service)
	databaseServer := api.NewRuntimeDBServer(service)
	assets := api.NewRuntimeAssetServer(service)
	objects := api.NewServer(service)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/internal/runtime/db/"):
			databaseServer.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, "/internal/runtime/objects/"):
			objects.ServeHTTP(w, r)
		case r.Header.Get("X-Nanoflare-Deployment-ID") != "":
			assets.ServeHTTP(w, r)
		default:
			kv.ServeHTTP(w, r)
		}
	}))
	defer backend.Close()
	// A private name served only by this UDP resolver proves the generated shim
	// reaches the selected DNS profile rather than workerd's native public DNS.
	dnsSocket, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dnsSocket.Close()
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, peer, err := dnsSocket.ReadFrom(buffer)
			if err != nil {
				return
			}
			var query dnsmessage.Message
			if query.Unpack(buffer[:n]) != nil {
				continue
			}
			answer := dnsmessage.Message{Header: dnsmessage.Header{ID: query.ID, Response: true, RecursionAvailable: true}, Questions: query.Questions}
			for _, q := range query.Questions {
				if q.Name.String() == "private.loader.test." && q.Type == dnsmessage.TypeA {
					answer.Answers = append(answer.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: q.Type, Class: dnsmessage.ClassINET, TTL: 30}, Body: &dnsmessage.AResource{A: [4]byte{10, 42, 0, 7}}})
				}
			}
			if wire, err := answer.Pack(); err == nil {
				_, _ = dnsSocket.WriteTo(wire, peer)
			}
		}
	}()
	resolver, err := dnsresolver.New(dnsresolver.Config{Profiles: map[string]dnsresolver.Profile{"loader-private": {Servers: []string{dnsSocket.LocalAddr().String()}}}}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.Start(); err != nil {
		t.Fatal(err)
	}
	defer resolver.Close(context.Background())
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.String() != "http://public.loader.test/from-worker" {
			http.Error(w, "unexpected proxy URL", 400)
			return
		}
		_, _ = io.WriteString(w, "proxy-egress")
	}))
	defer proxy.Close()
	adapter, err := egress.New(egress.Config{ProxyURL: proxy.URL, NoProxy: []string{"127.0.0.1"}, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close(context.Background())
	echoAddr := startEchoServer(t)
	programs := map[string]any{}
	for name, source := range map[string]string{
		"kv": nativeKVWorker, "objects": nativeObjectsWorker, "assets": nativeAssetsWorker,
		"rpc": `import { WorkerEntrypoint } from "cloudflare:workers";
			export default class extends WorkerEntrypoint {
				getUser(id) { return { id, version: "compiled" }; }
				fetch() { return Response.json(this.getUser("http")); }
			}`,
		"logs": `export default { fetch() { console.log("loader-contract-output"); return Response.json({ logged: true }); } };`,
		"db": `export default { async fetch(request, env) {
			await env.DB.exec("CREATE TABLE IF NOT EXISTS contract_notes (id INTEGER PRIMARY KEY, value TEXT)");
			await env.DB.batch([env.DB.prepare("INSERT OR REPLACE INTO contract_notes VALUES (?, ?)").bind(1, "hello")]);
			return Response.json({ first: await env.DB.prepare("SELECT value FROM contract_notes WHERE id = ?").bind(1).first("value"), raw: await env.DB.prepare("SELECT id, value FROM contract_notes").raw() });
		} };`,
		"cron": `export default {
			async scheduled(event, env, ctx) { ctx.waitUntil(env.KV.put("cron", event.cron)); },
			async fetch(request, env) { return Response.json({ cron: await env.KV.get("cron") }); }
		};`,
	} {
		active := nanoflare.ActiveDeployment{App: app, Deployment: deployment}
		active.Deployment.Files = []nanoflare.WorkerFile{{Path: "worker.js", Content: source}}
		programs[name] = loaderContractProgram(t, active)
	}
	legacy := nanoflare.ActiveDeployment{App: app, Deployment: deployment}
	legacy.Deployment.Format = "service-worker"
	legacy.Deployment.Files = []nanoflare.WorkerFile{{Path: "worker.js", Content: `
const legacyKV = KV;
addEventListener("fetch", event => event.respondWith((async () => {
  event.waitUntil(legacyKV.put("legacy-background", "done"));
  await KV.put("legacy", "ok");
  return Response.json({ legacy: await KV.get("legacy"), cron: await KV.get("legacy-cron"), background: await KV.get("legacy-background") });
})()));
addEventListener("scheduled", event => event.waitUntil(KV.put("legacy-cron", event.cron)));`}}
	programs["legacy"] = config.SnapshotWorkerCodeForLoader(legacy, config.WorkerdOptions{})
	network := nanoflare.ActiveDeployment{App: app, Deployment: deployment}
	network.Deployment.CompatibilityFlags = []string{"nodejs_compat"}
	network.Deployment.Files = []nanoflare.WorkerFile{{Path: "worker.js", Content: `import dns from "node:dns"; import promises from "node:dns/promises";
export default { async fetch() {
  const callback = await new Promise((resolve, reject) => dns.lookup("private.loader.test", {family: 4}, (error, address) => error ? reject(error) : resolve(address)));
  return Response.json({ callback, promise: await promises.lookup("private.loader.test", {family: 4}), egress: await (await fetch("http://public.loader.test/from-worker")).text() });
} };`}}
	programs["network"] = config.SnapshotWorkerCode(network, config.WorkerdOptions{DNSAddr: resolver.Addr()})
	invocationPrograms := map[string]any{}
	for name, program := range programs {
		if name == "network" {
			continue
		}
		invocationPrograms[name] = config.InstrumentLoaderInvocation(program.(config.WorkerCodeSnapshot), nanoflare.ActiveDeployment{App: app, Deployment: deployment})
	}
	// Compile legacy again with its declaration list for request-local globals.
	invocationPrograms["legacy"] = config.InstrumentLoaderInvocation(config.SnapshotWorkerCodeForLoader(legacy, config.WorkerdOptions{}), legacy)
	invocation := nanoflare.ActiveDeployment{App: app, Deployment: deployment}
	invocation.Deployment.Services = []nanoflare.ServiceBinding{{Binding: "IDENTITY", Service: "identity"}}
	invocation.Deployment.Files = []nanoflare.WorkerFile{{Path: "worker.js", Content: `
export default { async fetch(request, env, ctx) {
  const label = new URL(request.url).pathname.slice(1);
  if (label === "fail") throw new Error("intentional invocation failure");
  if (label === "rpc") { using session = await env.IDENTITY.session(); return Response.json({ user: await env.IDENTITY.getUser("invocation"), nested: await session.greet(), http: await (await env.IDENTITY.fetch("https://identity.invalid/")).json() }); }
  const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
  const pending = sleep(10);
  ctx.waitUntil(pending);
  pending.finally(() => ctx.waitUntil(sleep(30).then(() => env.EVENTS.fetch("http://events.invalid/invocation-finally", {method: "POST", body: label}))));
  ctx.waitUntil((async () => { await sleep(20); ctx.waitUntil(sleep(30).then(() => env.EVENTS.fetch("http://events.invalid/invocation-nested", {method: "POST", body: label}))); })());
  await env.KV.put("invocation:" + label, label);
  return Response.json({ value: await env.KV.get("invocation:" + label), props: ctx.props, hidden: env.__NANOFLARE_RESOURCE_BROKER === undefined });
} };`}}
	invocationPrograms["isolation"] = config.InstrumentLoaderInvocation(config.SnapshotWorkerCodeForLoader(invocation, config.WorkerdOptions{}), invocation)
	invocation.Deployment.Files = []nanoflare.WorkerFile{{Path: "worker.js", Content: `import { WorkerEntrypoint, RpcTarget } from "cloudflare:workers";
class Session extends RpcTarget { greet() { return "scoped-session"; } }
export default class extends WorkerEntrypoint {
  session() { return new Session(); }
  async inspect(label) {
    await this.env.DB.exec("CREATE TABLE IF NOT EXISTS scoped_class (value TEXT)");
    if ((await (await this.env.ASSETS.fetch("/logo.svg")).text()) !== "<svg/>") throw new Error("class asset wrapper failed");
    await this.env.KV.put("class:" + label, label);
    return { value: await this.env.KV.get("class:" + label), props: this.ctx.props, hidden: this.env.__NANOFLARE_RESOURCE_BROKER === undefined };
  }
  fetch() { return Response.json({ ready: true }); }
}`}}
	invocationPrograms["class"] = config.InstrumentLoaderInvocation(config.SnapshotWorkerCodeForLoader(invocation, config.WorkerdOptions{}), invocation)
	fixture, err := json.Marshal(map[string]any{
		"backend": strings.TrimPrefix(backend.URL, "http://"), "token": app.RuntimeToken,
		"namespaceID": namespace.ID, "databaseID": db.ID, "bucketID": bucket.ID,
		"appID": app.ID, "deploymentID": deployment.ID, "programs": programs,
		"dnsAddr": resolver.Addr(), "egressAddr": adapter.Addr(), "echoAddr": echoAddr,
		"invocationPrograms": invocationPrograms,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(fixturePath, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "testdata/loader-contracts/run.mjs")
	command.Env = append(os.Environ(), "WORKERD_BIN="+workerd, "NANOFLARE_LOADER_FIXTURE="+fixturePath)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Kill the entire fixture group on timeout, not just its Node parent.
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = 2 * time.Second
	output, err := command.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatalf("dynamic Worker contract gate: %v", err)
	}
}

// Use the production compiler, including its wrappers and embedded adapters.
func loaderContractProgram(t *testing.T, active nanoflare.ActiveDeployment) config.WorkerCodeSnapshot {
	t.Helper()
	code := config.SnapshotWorkerCode(active, config.WorkerdOptions{})
	if code.Modules[code.MainModule].JS == "" || code.Modules["worker.js"].JS == "" || code.Modules["nanoflare-internal:runtime-bindings"].JS == "" {
		t.Fatal("compiled Worker modules are incomplete")
	}
	return code
}
