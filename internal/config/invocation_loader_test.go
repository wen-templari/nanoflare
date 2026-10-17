package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestInstrumentLoaderInvocationKeepsInputDetachedAndCredentialsOutOfCode(t *testing.T) {
	active := nanoflare.ActiveDeployment{
		App:        nanoflare.App{RuntimeToken: "private-runtime-token", SecretValues: map[string]string{"SECRET": "private-secret-value"}},
		Deployment: nanoflare.Deployment{Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2026-09-08", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default {fetch(){return new Response("ok")}};`}}, KVNamespaces: []nanoflare.KVBinding{{Binding: "KV", ID: "private-resource-id"}}},
	}
	input := SnapshotWorkerCodeForLoader(active, WorkerdOptions{})
	before, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	output := InstrumentLoaderInvocation(input, active)
	after, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("instrumentation changed the input snapshot")
	}
	wire, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"private-runtime-token", "private-secret-value", "private-resource-id"} {
		if strings.Contains(string(wire), value) {
			t.Fatalf("compiled code leaked credential or resource identity %q", value)
		}
	}
}

func TestInstrumentLoaderInvocationRejectsReservedModuleCollision(t *testing.T) {
	for _, path := range []string{loaderInvocationContextModule, "__nanoflare_invocation_entrypoint__.js"} {
		t.Run(path, func(t *testing.T) {
			active := nanoflare.ActiveDeployment{Deployment: nanoflare.Deployment{Format: "modules", Entrypoint: "worker.js", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default {};`}, {Path: path, Content: `export default "collision";`}}}}
			code := InstrumentLoaderInvocation(SnapshotWorkerCodeForLoader(active, WorkerdOptions{}), active)
			if _, err := json.Marshal(code); err == nil {
				t.Fatal("reserved module collision was silently replaced")
			}
		})
	}
}
