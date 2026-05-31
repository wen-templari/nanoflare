package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/clas/platform/internal/platform"
)

func TestWorkerdGeneratesSharedPoolConfig(t *testing.T) {
	config := Workerd([]platform.ActiveDeployment{{
		App: platform.App{ID: "hello-app"},
		Deployment: platform.Deployment{
			BundlePath:        "/srv/apps/hello-app/worker.js",
			CompatibilityDate: "2026-05-31",
			Port:              9001,
		},
	}})
	for _, expected := range []string{
		`(name = "hello-app", worker = .workerHelloApp)`,
		`address = "*:9001"`,
		`serviceWorkerScript = embed "/srv/apps/hello-app/worker.js"`,
		`compatibilityDate = "2026-05-31"`,
	} {
		if !strings.Contains(config, expected) {
			t.Fatalf("config does not contain %q:\n%s", expected, config)
		}
	}
}

func TestWriterMakesAbsoluteBundlePathRelativeToConfig(t *testing.T) {
	dir := t.TempDir()
	writer := NewWriter(
		filepath.Join(dir, "generated", "workerd.capnp"),
		filepath.Join(dir, "generated", "traefik.yml"),
		"http://platformd/internal/auth/verify",
		"127.0.0.1",
	)
	err := writer.Write([]platform.ActiveDeployment{{
		App: platform.App{ID: "hello-app", Hostname: "hello.example.com"},
		Deployment: platform.Deployment{
			BundlePath:        filepath.Join(dir, "bundles", "hello.js"),
			CompatibilityDate: "2025-12-10",
			Port:              9001,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "generated", "workerd.capnp"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), `serviceWorkerScript = embed "../bundles/hello.js"`) {
		t.Fatalf("workerd config did not contain relative embed path:\n%s", content)
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
