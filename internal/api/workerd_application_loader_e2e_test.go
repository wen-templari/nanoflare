package api_test

import (
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
	binary := os.Getenv("NANOFLARE_TEST_WORKERD")
	if binary == "" {
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
			launcher := &dynamicPublicationLauncher{command: runtime.CommandLauncher{Executable: binary, Experimental: true}}
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
			deploy := func(app nanoflare.App, label string) (nanoflare.Deployment, error) {
				return service.Deploy(app.ID, nanoflare.DeployInput{
					CompatibilityDate: "2026-09-08", Format: "modules", Entrypoint: "worker.js",
					WorkerLoaders: []nanoflare.WorkerLoaderBinding{{Binding: "LOADER"}, {Binding: "SECOND"}},
					Vars:          map[string]json.RawMessage{"LABEL": json.RawMessage(`"` + label + `"`)},
					Files:         []nanoflare.WorkerFile{{Path: "worker.js", Content: loaderParent}},
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
			if launcher.launches.Load() != 1 {
				t.Fatalf("publication replaced workerd: launches=%d", launcher.launches.Load())
			}
		})
	}
}

const loaderParent = `
import { env as imported } from "cloudflare:workers";
let callbacks = 0;
const source = ` + "`" + `
import { WorkerEntrypoint } from "cloudflare:workers";
let count = 0;
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
}
` + "`" + `;
function code(label) {
  return {compatibilityDate:"2026-09-08", mainModule:"child.js", modules:{"child.js":source},
    env:{LABEL:label}, globalOutbound:null};
}
export default {async fetch(request, env) {
  const query = new URL(request.url).searchParams;
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
