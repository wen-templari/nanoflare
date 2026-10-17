package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestWorkerCodeSnapshotCapturesCodeWithoutEnvironmentOrAliasing(t *testing.T) {
	active := nanoflare.ActiveDeployment{
		App: nanoflare.App{ID: "a", RuntimeToken: "private-runtime-token", SecretValues: map[string]string{"SECRET": "private-secret"}},
		Deployment: nanoflare.Deployment{
			ID: "v1", Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2026-09-08",
			CompatibilityFlags: []string{"nodejs_compat"},
			Files:              []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default { fetch() { return new Response("v1"); } };`}},
			Vars:               map[string]json.RawMessage{"VALUE": json.RawMessage(`"private-var"`)},
		},
	}
	code := SnapshotWorkerCode(active, WorkerdOptions{})
	before, err := json.Marshal(code)
	if err != nil {
		t.Fatal(err)
	}
	active.Deployment.Files[0].Content = `export default { fetch() { return new Response("v2"); } };`
	active.Deployment.CompatibilityFlags[0] = "no_nodejs_compat"
	active.App.SecretValues["SECRET"] = "changed"
	after, err := json.Marshal(code)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("code snapshot aliases mutable deployment fields")
	}
	for _, private := range []string{"private-runtime-token", "private-secret", "private-var"} {
		if strings.Contains(string(before), private) {
			t.Fatal("code snapshot unexpectedly includes environment material")
		}
	}
	if code.Modules[code.MainModule].JS == "" || code.Modules["worker.js"].JS == "" {
		t.Fatal("code snapshot is missing the wrapper or user module")
	}
}

func TestWorkerCodeSnapshotPreservesPrivateDNSRewrite(t *testing.T) {
	active := nanoflare.ActiveDeployment{Deployment: nanoflare.Deployment{
		Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2026-09-08",
		Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `import dns from "node:dns"; export default { fetch() { return dns.lookup("private.test"); } };`}},
	}}
	withDNS := SnapshotWorkerCode(active, WorkerdOptions{DNSAddr: "127.0.0.1:8083"})
	withoutDNS := SnapshotWorkerCode(active, WorkerdOptions{})
	if !strings.Contains(withDNS.Modules["worker.js"].JS, `"nanoflare-internal:dns"`) || withDNS.Modules["nanoflare-internal:dns"].JS == "" {
		t.Fatal("private DNS rewriting or shim was omitted")
	}
	if !strings.Contains(withoutDNS.Modules["worker.js"].JS, `"node:dns"`) || withoutDNS.Modules["nanoflare-internal:dns"].JS != "" {
		t.Fatal("private DNS rewriting was enabled without its backend")
	}
}

func TestWorkerCodeSnapshotRejectsInternalModuleCollision(t *testing.T) {
	for _, name := range []string{"__nanoflare_internal_entrypoint__.js", "nanoflare-internal:runtime-bindings"} {
		t.Run(name, func(t *testing.T) {
			active := nanoflare.ActiveDeployment{Deployment: nanoflare.Deployment{
				Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2026-09-08",
				Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {};"}, {Path: name, Content: "export default {};"}},
			}}
			code := SnapshotWorkerCode(active, WorkerdOptions{})
			if _, err := json.Marshal(code); err == nil || !strings.Contains(err.Error(), "duplicate compiled Worker module") {
				t.Fatalf("module collision was silently serialized: %v", err)
			}
		})
	}
}
