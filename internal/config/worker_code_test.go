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

func TestWorkerCodeSnapshotInstallsPrivateDNSHookWithoutRewritingSource(t *testing.T) {
	active := nanoflare.ActiveDeployment{Deployment: nanoflare.Deployment{
		Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2026-09-08",
		Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `import dns from "node:dns"; export default { fetch() { return dns.lookup("private.test"); } };`}},
	}}
	withDNS := SnapshotWorkerCode(active, WorkerdOptions{DNSAddr: "127.0.0.1:8083"})
	withoutDNS := SnapshotWorkerCode(active, WorkerdOptions{})
	if withDNS.Modules["worker.js"].JS != active.Deployment.Files[0].Content || withDNS.Modules["nanoflare-internal:doh-fetch"].JS == "" {
		t.Fatal("DNS hook was omitted or user source changed")
	}
	if !strings.HasPrefix(withDNS.Modules[withDNS.MainModule].JS, `import "nanoflare-internal:doh-fetch";`) {
		t.Fatal("hook must evaluate before user modules")
	}
	if withoutDNS.Modules["worker.js"].JS != active.Deployment.Files[0].Content || withoutDNS.Modules["nanoflare-internal:doh-fetch"].JS != "" {
		t.Fatal("DNS hook enabled without its backend")
	}
	active.Deployment.CompatibilityFlags = []string{"no_nodejs_compat"}
	if SnapshotWorkerCode(active, WorkerdOptions{DNSAddr: "127.0.0.1:8083"}).Modules["nanoflare-internal:doh-fetch"].JS != "" {
		t.Fatal("DNS hook enabled without Node compatibility")
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

func TestWasmCodeSnapshotStaticAndWireTypes(t *testing.T) {
	wasm := []byte{0, 97, 115, 109, 1, 0, 0, 0, 0, 4, 1, 'x', 255, 254}
	active := nanoflare.ActiveDeployment{App: nanoflare.App{ID: "wasm"}, Deployment: nanoflare.Deployment{ID: "r", Format: "modules", Entrypoint: "worker.js", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `import module from "./lib.wasm";export default {fetch(){return new Response(String(module instanceof WebAssembly.Module))}}`}, {Path: "lib.wasm", Wasm: wasm}}}}
	code := SnapshotWorkerCode(active, WorkerdOptions{})
	wasm[8] = 42
	if code.Modules["lib.wasm"].Wasm[8] != 0 {
		t.Fatal("compiled binary aliases deployment")
	}
	wire, err := json.Marshal(code)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Modules map[string]map[string]json.RawMessage `json:"modules"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Modules["lib.wasm"]) != 1 || decoded.Modules["lib.wasm"]["wasm"] == nil {
		t.Fatal("binary module lost its type", string(wire))
	}
	var out strings.Builder
	writeWorkerSource(&out, code)
	if !strings.Contains(out.String(), `name = "lib.wasm", wasm = 0x"0061736d0100000000040178fffe"`) {
		t.Fatal(out.String())
	}
	empty, err := json.Marshal(WorkerCodeModule{})
	if err != nil || string(empty) != `{"js":""}` {
		t.Fatal("empty JS union changed", string(empty), err)
	}
	if _, err := json.Marshal(WorkerCodeModule{JS: "mixed", Wasm: []byte{1}}); err == nil {
		t.Fatal("mixed module union accepted")
	}
}
