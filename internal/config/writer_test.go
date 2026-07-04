package config

import (
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestWorkerdGeneratesSharedPoolConfig(t *testing.T) {
	config := Workerd([]nanoflare.ActiveDeployment{{
		App: nanoflare.App{ID: "hello-app", RuntimeToken: "secret"},
		Deployment: nanoflare.Deployment{
			Files:             []nanoflare.WorkerFile{{Path: "worker.js", Content: `addEventListener("fetch", () => {});`}},
			Entrypoint:        "worker.js",
			CompatibilityDate: "2025-12-10",
			Port:              9001,
		},
	}})
	for _, expected := range []string{
		`(name = "hello-app", worker = .workerHelloApp)`,
		`address = "*:9001"`,
		`globalThis.OBJECTS = __nanoflareWrapObjectsBinding(globalThis.OBJECTS);`,
		`(name = "KV", kvNamespace = "kv-hello-app")`,
		`(name = "ASSETS", service = "assets-hello-app")`,
		`(name = "OBJECTS", service = "objects-hello-app")`,
		`(name = "X-Nanoflare-Binding", value = "assets")`,
		`value = "Bearer secret"`,
		`compatibilityDate = "2025-12-10"`,
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
	if !strings.Contains(config, `(name = "OBJECTS", service = "objects-hello-app")`) {
		t.Fatalf("config does not contain objects binding:\n%s", config)
	}
	if strings.Contains(config, `(name = "ASSETS", service = "assets-hello-app")`) {
		t.Fatalf("config unexpectedly contains default asset binding:\n%s", config)
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
		`prefix: "/internal/http/apps/hello-app/9001"`,
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
