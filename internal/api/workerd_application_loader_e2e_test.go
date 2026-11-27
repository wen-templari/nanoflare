package api_test

import (
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runner"
	"github.com/clas/nanoflare/internal/runtime"
)

func TestWorkerdApplicationLoaderEndToEnd(t *testing.T) {
	workerd := os.Getenv("NANOFLARE_TEST_WORKERD")
	if workerd == "" {
		t.Skip("set NANOFLARE_TEST_WORKERD to the production-pinned native workerd")
	}
	for _, mode := range []string{"embedded", "remote"} {
		t.Run(mode, func(t *testing.T) {
			store := nanoflare.NewStore()
			if err := store.CreateOrganization(nanoflare.Organization{ID: "loaders", Name: "Loaders"}); err != nil {
				t.Fatal(err)
			}
			publication := &dynamicPublicationWriter{}
			service := nanoflare.NewServiceWithObjects(store, publication, newE2EObjectStore())
			control := api.NewRuntimeExecutionServer(service, "loader-test")
			defer control.Close()
			private := httptest.NewServer(control)
			defer private.Close()
			dir := t.TempDir()
			writer := config.NewRuntimeWriter(filepath.Join(dir, "workerd.capnp"), discardTraefik{})
			writer.SetNanoflareRuntimeAddr(strings.TrimPrefix(private.URL, "http://"))
			launcher := &dynamicPublicationLauncher{command: runtime.CommandLauncher{Executable: workerd, Experimental: true}}
			manager := runtime.NewDynamicManager(writer, launcher, dir, filepath.Join(dir, "workerd.capnp"), testSocketDir(t), 5*time.Second, time.Second)
			manager.SetRuntimeControlToken("loader-test")
			publication.manager = manager
			defer manager.Close()
			var ensurer api.RuntimeEnsurer = manager
			if mode == "remote" {
				server := httptest.NewServer(runner.NewServer(manager, "loader-test"))
				defer server.Close()
				publication.remote = runner.NewClient(server.URL, "loader-test", discardTraefik{})
				ensurer = publication.remote
			}
			create := func(name string) nanoflare.App {
				t.Helper()
				app, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "loaders", Name: name, Hostname: name + ".example.test"})
				if err != nil {
					t.Fatal(err)
				}
				return app
			}
			alpha, beta := create("alpha"), create("beta")
			// A valid custom section makes this module larger than the loader RPC budget.
			wasm := binary.AppendUvarint([]byte{0, 97, 115, 109, 1, 0, 0, 0, 0}, 600<<10)
			wasm = append(wasm, 1, 'x')
			wasm = append(wasm, make([]byte, (600<<10)-2)...)
			deploy := func(app nanoflare.App, label string) (nanoflare.Deployment, error) {
				return service.Deploy(app.ID, nanoflare.DeployInput{
					CompatibilityDate: "2026-09-08", Format: "modules", Entrypoint: "worker.js",
					WorkerLoaders: []nanoflare.WorkerLoaderBinding{{Binding: "LOADER"}, {Binding: "SECOND"}},
					Vars:          map[string]json.RawMessage{"LABEL": json.RawMessage(`"` + label + `"`)},
					Files: []nanoflare.WorkerFile{
						{Path: "worker.js", Content: loaderParent},
						{Path: "assets/fixture.wasm", Wasm: wasm},
					},
				})
			}
			if _, err := deploy(alpha, "disabled"); err == nil || !strings.Contains(err.Error(), "enable-worker-loaders") {
				t.Fatalf("opt-in bypassed: %v", err)
			}
			service.SetWorkerLoadersEnabled(true)
			firstAlpha, err := deploy(alpha, "alpha")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := deploy(beta, "beta"); err != nil {
				t.Fatal(err)
			}
			gateway := api.NewServerWithRuntime(service, nil, "", nil, nil, ensurer)
			check := func(app nanoflare.App, query, want string) {
				t.Helper()
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest("GET", "/internal/http/workers/"+app.ID+"/"+query, nil)
				gateway.ServeHTTP(recorder, request)
				if recorder.Code != http.StatusOK || recorder.Body.String() != want {
					t.Fatalf("%s %s: HTTP %d %q, want 200 %q", app.Name, query, recorder.Code, recorder.Body.String(), want)
				}
			}
			check(alpha, "", "alpha:1:1")
			check(alpha, "", "alpha:2:1")
			check(beta, "", "beta:1:1")
			check(alpha, "?second", "alpha:1:2")
			check(alpha, "?fresh", "alpha:1:2")
			check(alpha, "?fresh", "alpha:1:2")
			check(alpha, "?named", "named:alpha")
			check(alpha, "?wasm=fresh", "wasm:ok")
			check(alpha, "?wasm=cached", "wasm:ok")
			check(alpha, "?wasm=published", "wasm:ok")
			check(alpha, "?rpc=fresh", "opened:called:1:1:disposed")
			check(alpha, "?rpc=cached", "opened:called:1:1:disposed")
			check(alpha, "?rpc=cached", "opened:called:2:2:disposed")
			check(alpha, "?capabilities", "alpha:/probe:tail:limited")
			check(alpha, "?reuse-scoped-loader", "scope rejected")
			check(alpha, "?outbound", "blocked")
			if _, err := service.UpdateVarsForOrg("loaders", alpha.ID, nanoflare.UpdateVarsInput{
				ExpectedDeploymentID: firstAlpha.ID, IdempotencyKey: "loader-vars",
				Vars: map[string]json.RawMessage{"LABEL": json.RawMessage(`"updated"`)},
			}); err != nil {
				t.Fatal(err)
			}
			check(alpha, "", "updated:1:1")
			check(beta, "", "beta:2:1")
			check(alpha, "?parallel", "updated:1,updated:2:2")
			check(alpha, "?limit", "loader admission rejected")
			if os.Getenv("NANOFLARE_TEST_WORKERD_CPU_LIMITS") == "1" {
				t.Run("cpu-qualification", func(t *testing.T) {
					for _, tc := range []struct {
						probe, want    string
						minCPU, maxCPU float64
					}{
						{"wait", "finished:ok", 0, 10},
						{"js-control", "finished:ok", 0, 0},
						{"wasm-control", "finished:ok", 0, 0},
						{"js", "interrupted:exceededCpu", 10, 100},
						{"wasm", "interrupted:exceededCpu", 10, 100},
					} {
						w := httptest.NewRecorder()
						r := httptest.NewRequest("GET", "/internal/http/workers/"+alpha.ID+"/?cpu="+tc.probe, nil)
						gateway.ServeHTTP(w, r)
						var result struct {
							Result string  `json:"result"`
							CPU    float64 `json:"cpu"`
						}
						if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != http.StatusOK || result.Result != tc.want {
							t.Errorf("%s: HTTP %d %q, want 200 with result %q", tc.probe, w.Code, w.Body.String(), tc.want)
							continue
						}
						if tc.maxCPU != 0 && (result.CPU < tc.minCPU || result.CPU >= tc.maxCPU) {
							t.Errorf("%s: reported %.1f ms CPU, want [%.0f, %.0f) ms", tc.probe, result.CPU, tc.minCPU, tc.maxCPU)
						}
					}
				})
				check(beta, "", "beta:3:1")
			}
			if launcher.launches.Load() != 1 {
				t.Fatalf("publication replaced workerd: launches=%d", launcher.launches.Load())
			}
		})
	}
}

const loaderParent = `
import { env as imported } from "cloudflare:workers";
import { WorkerEntrypoint } from "cloudflare:workers";
import publishedWasm from "./assets/fixture.wasm";
let callbacks = 0;
const traces = new Set();
const cpuOutcomes = new Map();
let scopedEntrypoint;

export class ChildOutbound extends WorkerEntrypoint {
  fetch(request) { return new Response(this.env.LABEL + ":" + new URL(request.url).pathname); }
}

export class ChildTail extends WorkerEntrypoint {
  tail(events) {
    for (const event of events) {
      if (this.ctx.props.cpuProbe) cpuOutcomes.set(this.ctx.props.cpuProbe, {outcome: event.outcome, cpu: event.cpuTime});
      for (const log of event.logs ?? []) traces.add(log.message[0]);
    }
  }
}
const source = ` + "`" + `
import { RpcTarget, WorkerEntrypoint } from "cloudflare:workers";
let count = 0;
let sessions = 0;
let disposals = 0;
export default {async fetch(request, env) {
  if (new URL(request.url).searchParams.has("outbound")) {
    try { await fetch("http://127.0.0.1:1"); } catch (error) {
      return new Response(String(error).includes("not permitted to access the internet") ? "blocked" : String(error));
    }
    return new Response("unexpected outbound success");
  }
  return new Response(env.LABEL + ":" + ++count);
}};
export class Named extends WorkerEntrypoint {
  fetch() { return new Response(this.ctx.props.prefix + ":" + this.env.LABEL); }
  async openSession({callback}) {
    const opened = ++sessions;
    const prefix = this.ctx.props.prefix;
    const retained = callback.dup();
    class Session extends RpcTarget {
      get id() { return opened; }
      echo() { return retained(prefix); }
      [Symbol.dispose]() { retained[Symbol.dispose](); disposals++; }
    }
    return new Session();
  }
  disposedSessions() { return disposals; }
}
` + "`" + `;
function code(label) {
  return {compatibilityDate:"2026-09-08", mainModule:"child.js", modules:{"child.js":source},
    env:{LABEL:label}, globalOutbound:null};
}

async function probeCPU(env, ctx, request, probe) {
  const descriptor = {compatibilityDate: "2026-09-08", mainModule: "busy.js", modules: {},
    tails: [ctx.exports.ChildTail({props: {cpuProbe: probe}})]};
  if (!probe.endsWith("-control")) descriptor.limits = {cpuMs: 10};
  if (probe.startsWith("wasm")) {
    // (i32 iterations) -> i32 linear-congruential checksum loop using iterations multiplications.
    descriptor.modules["busy.wasm"] = {wasm: Uint8Array.fromBase64("AGFzbQEAAAABBgFgAX8BfwMCAQAHBwEDcnVuAAAKKQEnAQF/QQchAQNAIAFBjczlAGxB3+a74wNqIQEgAEEBayIADQALIAEL").buffer};
    descriptor.modules["busy.js"] = 'import module from "busy.wasm"; export default {fetch() { return new Response(String(new WebAssembly.Instance(module).exports.run(250_000_000))); }}';
  } else if (probe === "wait") {
    descriptor.modules["busy.js"] = 'export default {async fetch() { await new Promise(resolve => setTimeout(resolve, 150)); return new Response("ok"); }}';
  } else {
    descriptor.modules["busy.js"] = 'export default {fetch() { let checksum = 7; for (let i = 0; i < 250_000_000; i++) checksum = (Math.imul(checksum, 1664525) + 1013904223) | 0; return new Response(String(checksum)); }}';
  }
  let result;
  try {
    const response = await env.LOADER.load(descriptor).getEntrypoint().fetch(request);
    await response.text();
    result = response.ok ? "finished" : "interrupted";
  } catch {
    result = "interrupted";
  }
  for (let attempts = 0; attempts < 40 && !cpuOutcomes.has(probe); attempts++) {
    await new Promise(resolve => setTimeout(resolve, 10));
  }
  const {outcome = "missing tail", cpu} = cpuOutcomes.get(probe) ?? {};
  cpuOutcomes.delete(probe);
  return Response.json({result: result + ":" + outcome, cpu});
}

export default {async fetch(request, env, ctx) {
  const query = new URL(request.url).searchParams;
  if (query.has("cpu")) return probeCPU(env, ctx, request, query.get("cpu"));
  if (query.has("capabilities")) {
    const source = 'export default {async fetch() { try { const response = await fetch("https://child-outbound.invalid/probe"); console.log("loader-tail-probe"); return response; } catch (error) { return new Response(String(error)); } }}';
    const child = env.LOADER.load({
      compatibilityDate: "2026-09-08", mainModule: "child.js", modules: {"child.js": source},
      globalOutbound: ctx.exports.ChildOutbound(), tails: [ctx.exports.ChildTail()],
      limits: {cpuMs: 100, subRequests: 1},
    });
    const entrypoint = child.getEntrypoint();
    scopedEntrypoint = entrypoint;
    const result = await (await entrypoint.fetch(request)).text();
    const denied = await (await entrypoint.fetch(request)).text();
    for (let attempts = 0; attempts < 40 && !traces.has("loader-tail-probe"); attempts++) {
      await new Promise(resolve => setTimeout(resolve, 10));
    }
    return new Response(result + (traces.delete("loader-tail-probe") ? ":tail" : ":missing tail") +
      (denied.includes("Worker Loader child subrequest limit exceeded") ? ":limited" : ":" + denied));
  }
  if (query.has("reuse-scoped-loader")) {
    try {
      await scopedEntrypoint.fetch(request);
    } catch (error) {
      if (error.message === "Worker Loader binding belongs to another invocation") {
        return new Response("scope rejected");
      }
      throw error;
    }
    return new Response("scoped loader reused");
  }
  if (query.has("wasm")) {
    const bytes = new Uint8Array([255, 0, 97, 115, 109, 1, 0, 0, 0, 255]);
    const view = bytes.subarray(1, -1);
    const kind = query.get("wasm");
    const wasm = kind === "published" ? publishedWasm : kind === "fresh" ? view : view.slice().buffer;
    const descriptor = {
      compatibilityDate: "2026-09-08", mainModule: "wasm-child.js",
      modules: {
        "wasm-child.js": 'import module from "wasm-child.wasm"; export default {fetch() { new WebAssembly.Instance(module); return new Response("wasm:ok"); }}',
        "wasm-child.wasm": {wasm},
      },
    };
    const child = kind === "cached" ? env.LOADER.get("wasm", () => descriptor) : env.LOADER.load(descriptor);
    return child.getEntrypoint().fetch(request);
  }
  if (query.has("rpc")) {
    const child = query.get("rpc") === "fresh" ? env.LOADER.load(code(env.LABEL)) :
      env.LOADER.get("rpc", () => code(env.LABEL));
    const entrypoint = child.getEntrypoint("Named", {props:{prefix:"opened"}});
    const session = await entrypoint.openSession({callback: value => value + ":called"});
    const result = (await session.echo()) + ":" + (await session.id);
    session[Symbol.dispose]();
    const disposals = await entrypoint.disposedSessions();
    entrypoint[Symbol.dispose]();
    try {
      await entrypoint.openSession({callback: value => value});
    } catch (error) {
      if (error.message === "Worker Loader entrypoint was disposed") {
        return new Response(result + ":" + disposals + ":disposed");
      }
      throw error;
    }
    return new Response("disposed entrypoint was callable");
  }
  if (query.has("fresh")) {
    const child = env.LOADER.load(code(env.LABEL)).getEntrypoint();
    const first = await (await child.fetch(request)).text();
    const second = await (await child.fetch(request)).text();
    return new Response(first + ":" + second.split(":")[1]);
  }
  const loader = query.has("second") ? env.SECOND : imported.LOADER;
  const worker = loader.get(query.has("parallel") ? "parallel" : "same", async () => {
    callbacks++;
    return code(env.LABEL);
  });
  if (query.has("parallel")) {
    const child = worker.getEntrypoint();
    const fetchChild = async () => (await child.fetch(request)).text();
    const values = await Promise.all([fetchChild(), fetchChild()]);
    return new Response(values.sort().join(",") + ":" + callbacks);
  }
  if (query.has("limit")) {
    const child = worker.getEntrypoint();
    try {
      for (let i = 0; i < 33; i++) await (await child.fetch(request)).text();
    } catch (error) {
      if (String(error).includes("Worker Loader admission rejected: 429")) {
        return new Response("loader admission rejected");
      }
      throw error;
    }
    return new Response("loader admission limit missing");
  }
  if (query.has("named")) return worker.getEntrypoint("Named", {props:{prefix:"named"}}).fetch(request);
  if (query.has("outbound")) return worker.getEntrypoint().fetch(request);
  const result = await worker.getEntrypoint().fetch(request);
  return new Response((await result.text()) + ":" + callbacks);
}};
`
