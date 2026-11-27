package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestWorkerCodeSnapshotForLoaderAdaptsLegacyWithoutChangingStaticCode(t *testing.T) {
	active := nanoflare.ActiveDeployment{Deployment: nanoflare.Deployment{
		Format: "service-worker", CompatibilityDate: "2026-09-08",
		Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `addEventListener("fetch", event => event.respondWith(new Response("legacy")));`}},
	}}
	static := SnapshotWorkerCode(active, WorkerdOptions{})
	loader := SnapshotWorkerCodeForLoader(active, WorkerdOptions{})
	if strings.Contains(static.Modules[static.MainModule].JS, "export default") {
		t.Fatal("loader adapter changed static service-worker representation")
	}
	if !strings.Contains(loader.Modules[loader.MainModule].JS, "export default") {
		t.Fatal("legacy loader code has no module entrypoint")
	}
	if !reflect.DeepEqual(static, SnapshotWorkerCode(active, WorkerdOptions{})) {
		t.Fatal("loader compilation mutated static inputs")
	}
	active.Deployment.Format = "modules"
	active.Deployment.Entrypoint = "worker.js"
	if !reflect.DeepEqual(SnapshotWorkerCode(active, WorkerdOptions{}), SnapshotWorkerCodeForLoader(active, WorkerdOptions{})) {
		t.Fatal("module Workers should not receive the legacy adapter")
	}
}

func TestWorkerCodeSnapshotForLoaderInstallsEgressBeforeUserCode(t *testing.T) {
	const hook = "nanoflare-internal:egress-fetch"
	active := nanoflare.ActiveDeployment{Deployment: nanoflare.Deployment{
		Format:     "modules",
		Entrypoint: "worker.js",
		Files:      []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {};"}},
	}}
	withoutEgress := SnapshotWorkerCodeForLoader(active, WorkerdOptions{})
	if _, ok := withoutEgress.Modules[hook]; ok {
		t.Fatal("included the egress hook without a configured egress address")
	}
	withEgress := SnapshotWorkerCodeForLoader(active, WorkerdOptions{EgressAddr: "127.0.0.1:8082"})
	want := `import "nanoflare-internal:egress-fetch";` + "\n" + withoutEgress.Modules[withoutEgress.MainModule].JS
	if withEgress.Modules[withEgress.MainModule].JS != want {
		t.Fatal("egress hook must be imported before the Worker entrypoint")
	}
	source := withEgress.Modules[hook].JS
	for _, expected := range []string{
		`import { currentInvocationEnv } from "nanoflare-internal:invocation-context";`,
		`function installEgressFetch(outbound)`,
		`installEgressFetch(() => currentInvocationEnv().__NANOFLARE_HTTP_OUTBOUND);`,
	} {
		if !strings.Contains(source, expected) {
			t.Fatalf("egress hook is missing %q:\n%s", expected, source)
		}
	}
	if withEgress.Modules[loaderInvocationContextModule].JS == "" {
		t.Fatal("egress hook is missing its invocation context module")
	}
}
