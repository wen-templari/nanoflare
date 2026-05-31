package config

import (
	"strings"
	"testing"

	"github.com/clas/platform/internal/platform"
)

func TestWorkerdGeneratesSharedPoolConfig(t *testing.T) {
	config := Workerd([]platform.ActiveDeployment{{
		App: platform.App{ID: "hello-app"},
		Deployment: platform.Deployment{
			Files:             []platform.WorkerFile{{Path: "worker.js", Content: `addEventListener("fetch", () => {});`}},
			Entrypoint:        "worker.js",
			CompatibilityDate: "2026-05-31",
			Port:              9001,
		},
	}})
	for _, expected := range []string{
		`(name = "hello-app", worker = .workerHelloApp)`,
		`address = "*:9001"`,
		`serviceWorkerScript = "addEventListener(\"fetch\", () => {});"`,
		`compatibilityDate = "2026-05-31"`,
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("config does not contain %q:\n%s", expected, config)
		}
	}
}

func TestWorkerdGeneratesMultiFileModuleConfigWithEntrypointFirst(t *testing.T) {
	config := Workerd([]platform.ActiveDeployment{{
		App: platform.App{ID: "hello-app", Hostname: "hello.example.com"},
		Deployment: platform.Deployment{
			Files: []platform.WorkerFile{
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
	config := Traefik([]platform.ActiveDeployment{{
		App:        platform.App{ID: "hello-app", Hostname: "hello.example.com"},
		Deployment: platform.Deployment{Port: 9001},
	}}, "http://platformd:8080/internal/auth/verify", "host.docker.internal")
	for _, expected := range []string{
		`address: "http://platformd:8080/internal/auth/verify"`,
		`rule: "Host(` + "`" + `hello.example.com` + "`" + `)"`,
		`url: "http://host.docker.internal:9001/"`,
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("config does not contain %q:\n%s", expected, config)
		}
	}
}
