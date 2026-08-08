package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestWorkerdGeneratesSharedPoolConfig(t *testing.T) {
	config := Workerd([]nanoflare.ActiveDeployment{{
		App: nanoflare.App{ID: "hello-app", RuntimeToken: "secret"},
		Deployment: nanoflare.Deployment{
			Files:              []nanoflare.WorkerFile{{Path: "worker.js", Content: `addEventListener("fetch", () => {});`}},
			Entrypoint:         "worker.js",
			CompatibilityDate:  "2025-12-10",
			CompatibilityFlags: []string{"nodejs_compat"},
			KVNamespaces:       []nanoflare.KVBinding{{Binding: "KV", ID: "kvns-1"}},
			Port:               9001,
		},
	}})
	for _, expected := range []string{
		`(name = "internet", network = (allow = ["public", "10.0.0.0/8"], tlsOptions = (trustBrowserCas = true)))`,
		`(name = "hello-app", worker = .workerHelloApp)`,
		`(name = "nanoflare-duration-collector", external = (address = "127.0.0.1:8081"))`,
		`address = "*:9001"`,
		`(name = "kv-hello-app-0", external = (address = "127.0.0.1:8081"`,
		`(name = "X-Nanoflare-KV-Namespace-ID", value = "kvns-1")`,
		`(name = "KV", kvNamespace = "kv-hello-app-0")`,
		`(name = "ASSETS", service = "assets-hello-app")`,
		`(name = "X-Nanoflare-Binding", value = "assets")`,
		`__nanoflareRecordRuntimeDuration(startedAt, \"ok\")`,
		`scriptName: globalThis.__NANOFLARE_APP_ID`,
		`durationMs`,
		`(name = "__NANOFLARE_APP_ID", text = "hello-app")`,
		`(name = "__NANOFLARE_DEPLOYMENT_ID", text = "")`,
		`(name = "__NANOFLARE_DURATION_COLLECTOR", service = "nanoflare-duration-collector")`,
		`globalThis.__NANOFLARE_DURATION_COLLECTOR.fetch(\"http://nanoflare.internal/internal/runtime/durations\"`,
		`[[nanoflare-output app=`,
		`value = "Bearer secret"`,
		`compatibilityDate = "2025-12-10"`,
		`compatibilityFlags = ["nodejs_compat"]`,
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("config does not contain %q:\n%s", expected, config)
		}
	}
}

func TestWorkerdUsesCustomAssetBindingName(t *testing.T) {
	config := Workerd([]nanoflare.ActiveDeployment{{
		App: nanoflare.App{ID: "hello-app", RuntimeToken: "secret"},
		Deployment: nanoflare.Deployment{
			Files:             []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { fetch() {} };`}},
			Entrypoint:        "worker.js",
			Format:            "modules",
			CompatibilityDate: "2025-12-10",
			AssetConfig:       nanoflare.AssetConfig{Binding: "STATIC"},
			Port:              9001,
		},
	}})
	if !strings.Contains(config, `(name = "STATIC", service = "assets-hello-app")`) {
		t.Fatalf("config does not contain custom asset binding:\n%s", config)
	}
	if strings.Contains(config, `(name = "ASSETS", service = "assets-hello-app")`) {
		t.Fatalf("config unexpectedly contains default asset binding:\n%s", config)
	}
}

func TestWorkerdGeneratesServiceBindingForActiveWorkerInSameOrganization(t *testing.T) {
	generated := Workerd([]nanoflare.ActiveDeployment{
		{App: nanoflare.App{ID: "caller", OrgID: "org", Name: "caller"}, Deployment: nanoflare.Deployment{ID: "caller-v1", CompatibilityDate: "2025-12-10", Services: []nanoflare.ServiceBinding{{Binding: "AUTH", Service: "auth"}}}},
		{App: nanoflare.App{ID: "auth", OrgID: "org", Name: "auth"}, Deployment: nanoflare.Deployment{ID: "auth-v2", CompatibilityDate: "2025-12-10"}},
		{App: nanoflare.App{ID: "other-auth", OrgID: "other", Name: "auth"}, Deployment: nanoflare.Deployment{ID: "other-v1", CompatibilityDate: "2025-12-10"}},
	})
	if !strings.Contains(generated, `(name = "AUTH", service = "auth-auth-v2")`) {
		t.Fatalf("config does not contain same-organization service binding:\n%s", generated)
	}
}

func TestWorkerdPreservesWorkerEntrypointDefaultExportForRPC(t *testing.T) {
	generated := Workerd([]nanoflare.ActiveDeployment{{
		App: nanoflare.App{ID: "rpc", Name: "rpc"},
		Deployment: nanoflare.Deployment{
			ID:                "v1",
			Entrypoint:        "worker.js",
			Format:            "modules",
			CompatibilityDate: "2025-12-10",
			Files:             []nanoflare.WorkerFile{{Path: "worker.js", Content: `import { WorkerEntrypoint } from "cloudflare:workers"; export default class extends WorkerEntrypoint { add(a, b) { return a + b; } }`}},
		},
	}})
	if !strings.Contains(generated, `name = "__nanoflare_internal_entrypoint__.js"`) {
		t.Fatalf("RPC WorkerEntrypoint must include its instrumentation module:\n%s", generated)
	}
	if !strings.Contains(generated, `export default class extends WorkerEntrypoint`) {
		t.Fatalf("config does not preserve WorkerEntrypoint module:\n%s", generated)
	}
	for _, expected := range []string{
		`Object.getOwnPropertyNames(userWorker.prototype)`,
		`original.apply(this, args)`,
		`value: function (...args)`,
		`typeof result.then === \"function\"`,
		`recordRuntimeDuration(env, ctx, startedAt, \"ok\")`,
		`export default userWorker`,
	} {
		if !strings.Contains(generated, expected) {
			t.Fatalf("RPC instrumentation is missing %q:\n%s", expected, generated)
		}
	}
}

func TestWorkerdUsesCustomNetworkAllowList(t *testing.T) {
	config := WorkerdWithOptions(nil, WorkerdOptions{NetworkAllow: ParseNetworkAllow("public, local, 192.168.0.0/16")})
	if !strings.Contains(config, `(name = "internet", network = (allow = ["public", "local", "192.168.0.0/16"], tlsOptions = (trustBrowserCas = true)))`) {
		t.Fatalf("config does not contain custom network allow list:\n%s", config)
	}
}

func TestWorkerdUsesConfiguredEgress(t *testing.T) {
	active := []nanoflare.ActiveDeployment{{App: nanoflare.App{ID: "app"}, Deployment: nanoflare.Deployment{CompatibilityDate: "2025-12-10"}}}
	generated := WorkerdWithOptions(active, WorkerdOptions{EgressAddr: "127.0.0.1:8082"})
	for _, expected := range []string{
		`(name = "nanoflare-egress", external = (address = "127.0.0.1:8082", http = (style = proxy)))`,
		`globalOutbound = "nanoflare-egress"`,
	} {
		if !strings.Contains(generated, expected) {
			t.Fatalf("config does not contain %q:\n%s", expected, generated)
		}
	}
}

func TestWorkerdIncludesVarsAndSecretsBindings(t *testing.T) {
	config := Workerd([]nanoflare.ActiveDeployment{{
		App: nanoflare.App{ID: "hello-app", RuntimeToken: "secret", SecretValues: map[string]string{"DB_URL": "postgres://secret"}},
		Deployment: nanoflare.Deployment{
			Files:             []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { fetch() {} };`}},
			Entrypoint:        "worker.js",
			Format:            "modules",
			CompatibilityDate: "2025-12-10",
			Vars: map[string]json.RawMessage{
				"API_HOST":      json.RawMessage(`"example.com"`),
				"FEATURE_FLAGS": json.RawMessage(`{"beta":true}`),
			},
			Port: 9001,
		},
	}})
	for _, expected := range []string{
		`(name = "API_HOST", text = "example.com")`,
		`(name = "FEATURE_FLAGS", json = "{\"beta\":true}")`,
		`(name = "DB_URL", text = "postgres://secret")`,
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("config does not contain %q:\n%s", expected, config)
		}
	}
}

func TestWorkerdObjectBindingErrorsIncludeRuntimeDetails(t *testing.T) {
	config := Workerd([]nanoflare.ActiveDeployment{{
		App: nanoflare.App{ID: "hello-app", RuntimeToken: "secret"},
		Deployment: nanoflare.Deployment{
			Files:                []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { fetch() {} };`}},
			Entrypoint:           "worker.js",
			Format:               "modules",
			CompatibilityDate:    "2025-12-10",
			ObjectStorageBuckets: []nanoflare.ObjectStorageBucketBinding{{Binding: "OBJECTS", BucketID: "bucket-1"}},
			Port:                 9001,
		},
	}})
	for _, expected := range []string{
		`async function objectBindingError(bindingName, operation, response)`,
		`objectBindingError(bindingName, \"get\", response)`,
		`objectBindingError(bindingName, \"put\", response)`,
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("config does not contain %q:\n%s", expected, config)
		}
	}
}

func TestWorkerdGeneratesSingleFileModuleWorker(t *testing.T) {
	config := Workerd([]nanoflare.ActiveDeployment{{
		App: nanoflare.App{ID: "hello-app", RuntimeToken: "secret"},
		Deployment: nanoflare.Deployment{
			Files:             []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { fetch() {} };`}},
			Entrypoint:        "worker.js",
			Format:            "modules",
			CompatibilityDate: "2025-12-10",
			Port:              9001,
		},
	}})
	if !strings.Contains(config, `(name = "worker.js", esModule = "export default { fetch() {} };")`) {
		t.Fatalf("config does not contain single-file module:\n%s", config)
	}
	if strings.Contains(config, "serviceWorkerScript") {
		t.Fatalf("config unexpectedly contains service worker source:\n%s", config)
	}
}

func TestWorkerdReplacesNonBreakingSpacesInWorkerSource(t *testing.T) {
	const source = `export default { fetch() { if (false) { return new Response("never runs"); } return new Response("ok"); } };`
	config := Workerd([]nanoflare.ActiveDeployment{{
		App: nanoflare.App{ID: "hello-app", RuntimeToken: "secret"},
		Deployment: nanoflare.Deployment{
			Files:             []nanoflare.WorkerFile{{Path: "worker.js", Content: source}},
			Entrypoint:        "worker.js",
			Format:            "modules",
			CompatibilityDate: "2025-12-10",
			Port:              9001,
		},
	}})
	if strings.ContainsRune(config, '\u00a0') {
		t.Fatalf("workerd config retains a non-breaking space:\n%s", config)
	}
	if !strings.Contains(config, `never runs`) {
		t.Fatalf("workerd config did not preserve the worker source with a normal space:\n%s", config)
	}
}

func TestWorkerdModuleWrapperHandlesScheduledRoute(t *testing.T) {
	config := Workerd([]nanoflare.ActiveDeployment{{
		App: nanoflare.App{ID: "hello-app", RuntimeToken: "secret"},
		Deployment: nanoflare.Deployment{
			Files:             []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { scheduled() {} };`}},
			Entrypoint:        "worker.js",
			Format:            "modules",
			CompatibilityDate: "2025-12-10",
			Triggers:          nanoflare.TriggerConfig{Crons: []string{"*/5 * * * *"}},
			Port:              9001,
		},
	}})
	for _, expected := range []string{
		`/cdn-cgi/handler/scheduled`,
		`userWorker.scheduled(scheduledController(request), wrapEnv(env), ctx)`,
		`withOutputIdentity(env, async () =>`,
		`new Response(null, { status: 204 })`,
		`url.searchParams.get(\"cron\")`,
		`url.searchParams.get(\"time\")`,
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("config does not contain %q:\n%s", expected, config)
		}
	}
}

func TestWorkerdServiceWorkerWrapperHandlesScheduledListeners(t *testing.T) {
	config := Workerd([]nanoflare.ActiveDeployment{{
		App: nanoflare.App{ID: "hello-app", RuntimeToken: "secret"},
		Deployment: nanoflare.Deployment{
			Files:             []nanoflare.WorkerFile{{Path: "worker.js", Content: `addEventListener("scheduled", event => event.waitUntil(Promise.resolve()));`}},
			Entrypoint:        "worker.js",
			Format:            "service-worker",
			CompatibilityDate: "2025-12-10",
			Triggers:          nanoflare.TriggerConfig{Crons: []string{"*/5 * * * *"}},
			Port:              9001,
		},
	}})
	for _, expected := range []string{
		`const __nanoflareScheduledListeners = [];`,
		`type === \"scheduled\"`,
		`__nanoflareRunScheduledListeners(event.request)`,
		`function __nanoflareWithOutputIdentity(callback)`,
		`[[nanoflare-output app=`,
		`/cdn-cgi/handler/scheduled`,
		`new Response(null, { status: 204 })`,
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("config does not contain %q:\n%s", expected, config)
		}
	}
}

func TestWorkerdGeneratesMultiFileModuleConfigWithEntrypointFirst(t *testing.T) {
	config := Workerd([]nanoflare.ActiveDeployment{{
		App: nanoflare.App{ID: "hello-app", Hostname: "hello.example.com"},
		Deployment: nanoflare.Deployment{
			Files: []nanoflare.WorkerFile{
				{Path: "message.js", Content: `export const message = "hello";`},
				{Path: "worker.js", Content: `import { message } from "./message.js"; export default { fetch() { return new Response(message); } };`},
			},
			Entrypoint:        "worker.js",
			CompatibilityDate: "2025-12-10",
			Port:              9001,
		},
	}})
	entrypoint := strings.Index(config, `(name = "worker.js", esModule = `)
	imported := strings.Index(config, `(name = "message.js", esModule = `)
	if entrypoint == -1 || imported == -1 || entrypoint > imported {
		t.Fatalf("workerd modules did not put the entrypoint first:\n%s", config)
	}
}

func TestTraefikGeneratesForwardAuthRouter(t *testing.T) {
	config := Traefik([]nanoflare.ActiveDeployment{{
		App:        nanoflare.App{ID: "hello-app", Hostname: "hello.example.com"},
		Deployment: nanoflare.Deployment{Port: 9001},
	}}, "http://nanoflared:8080/internal/auth/verify", "", "host.docker.internal")
	for _, expected := range []string{
		`address: "http://nanoflared:8080/internal/auth/verify"`,
		`- X-Nanoflare-User-JWT`,
		`- X-Nanoflare-User-Email`,
		`rule: "Host(` + "`" + `hello.example.com` + "`" + `)"`,
		`- web`,
		`- websecure`,
		`- hello_app-prefix`,
		`prefix: "/internal/http/workers/hello-app"`,
		`url: "http://nanoflared:8080"`,
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("config does not contain %q:\n%s", expected, config)
		}
	}
}

func TestTraefikGeneratesProtectedRouteRouters(t *testing.T) {
	config := Traefik([]nanoflare.ActiveDeployment{{
		App: nanoflare.App{
			ID:       "hello-app",
			Hostname: "hello.example.com",
			Auth:     nanoflare.AuthConfig{ProtectedRoutes: []string{"/admin/*", "/reports"}},
		},
		Deployment: nanoflare.Deployment{Port: 9001},
	}}, "http://nanoflared:8080/internal/auth/verify", "", "host.docker.internal")
	for _, expected := range []string{
		`hello_app-auth-0`,
		`rule: "Host(` + "`" + `hello.example.com` + "`" + `) && PathPrefix(` + "`" + `/admin/` + "`" + `)"`,
		`hello_app-auth-1`,
		`rule: "Host(` + "`" + `hello.example.com` + "`" + `) && Path(` + "`" + `/reports` + "`" + `)"`,
		`priority: 200`,
		`priority: 190`,
		`middlewares:`,
		`- nanoflare-auth`,
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("config does not contain %q:\n%s", expected, config)
		}
	}
}

func TestTraefikGeneratesRootProtectedRouteRouter(t *testing.T) {
	config := Traefik([]nanoflare.ActiveDeployment{{
		App: nanoflare.App{
			ID:       "hello-app",
			Hostname: "hello.example.com",
			Auth:     nanoflare.AuthConfig{ProtectedRoutes: []string{"/"}},
		},
		Deployment: nanoflare.Deployment{Port: 9001},
	}}, "http://nanoflared:8080/internal/auth/verify", "", "host.docker.internal")
	for _, expected := range []string{
		`hello_app-auth-0`,
		`rule: "Host(` + "`" + `hello.example.com` + "`" + `) && PathPrefix(` + "`" + `/` + "`" + `)"`,
		`priority: 190`,
		`- nanoflare-auth`,
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("config does not contain %q:\n%s", expected, config)
		}
	}
}

func TestTraefikGeneratesControlPlaneAuthRouter(t *testing.T) {
	config := Traefik([]nanoflare.ActiveDeployment{{
		App:        nanoflare.App{ID: "hello-app", Hostname: "hello.example.com"},
		Deployment: nanoflare.Deployment{Port: 9001},
	}}, "http://nanoflared:8080/internal/auth/verify", "nanoflare.local.nbtca.space", "host.docker.internal")
	for _, expected := range []string{
		`nanoflare_auth_callback`,
		`rule: "Host(` + "`" + `nanoflare.local.nbtca.space` + "`" + `) && PathPrefix(` + "`" + `/internal/auth/` + "`" + `)"`,
		`service: nanoflare_auth_callback`,
		`url: "http://nanoflared:8080"`,
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("config does not contain %q:\n%s", expected, config)
		}
	}
	if strings.Contains(config, `nanoflare_auth_callback-prefix`) {
		t.Fatalf("callback router unexpectedly contains prefix middleware:\n%s", config)
	}
}
