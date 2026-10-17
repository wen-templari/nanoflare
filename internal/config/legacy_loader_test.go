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
