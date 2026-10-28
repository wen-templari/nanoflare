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

func TestDNSCompilerNamespaceAndHostCompatibility(t *testing.T) {
	for _, tc := range []struct {
		date          string
		flags         []string
		enabled, bare bool
	}{
		{"2026-09-11", []string{"nodejs_compat"}, true, true},
		{"2025-12-10", nil, false, false},
		{"2026-09-11", []string{"no_nodejs_compat"}, false, false},
		{"2024-01-01", []string{"nodejs_compat"}, true, false},
	} {
		t.Run(tc.date+strings.Join(tc.flags, ","), func(t *testing.T) {
			d := nanoflare.Deployment{Format: "modules", Entrypoint: "dist/worker.js", CompatibilityDate: tc.date, CompatibilityFlags: tc.flags,
				DispatchNamespaces: []nanoflare.DispatchBinding{{Binding: "NS", NamespaceID: "ns"}},
				Files:              []nanoflare.WorkerFile{{Path: "dist/worker.js", Content: `import dns from "node:dns"; import promises from "dns/promises"; export default {};`}, {Path: "lib.wasm", Wasm: []byte{0, 97, 115, 109}}},
			}
			ns, err := NamespaceWorkerModules(d)
			if err != nil {
				t.Fatal(err)
			}
			host := SnapshotWorkerCode(nanoflare.ActiveDeployment{Deployment: d}, WorkerdOptions{DNSAddr: "127.0.0.1:1234"}).Modules
			for name, modules := range map[string]map[string]WorkerCodeModule{"namespace": ns, "host": host} {
				if strings.Contains(modules["dist/worker.js"].JS, `"../nanoflare-internal:dns"`) != tc.enabled {
					t.Fatalf("%s DNS compatibility: %s", name, modules["dist/worker.js"].JS)
				}
				if strings.Contains(modules["dist/worker.js"].JS, `"../nanoflare-internal:dns/promises"`) != tc.bare {
					t.Fatalf("%s bare import compatibility: %s", name, modules["dist/worker.js"].JS)
				}
				if (modules["nanoflare-internal:dns"].JS != "") != tc.enabled {
					t.Fatalf("%s shim compatibility", name)
				}
				if len(modules["lib.wasm"].Wasm) != 4 {
					t.Fatal("lost Wasm module")
				}
			}
			if !strings.Contains(d.Files[0].Content, `"node:dns"`) {
				t.Fatal("mutated deployment")
			}
			if tc.enabled && (!strings.Contains(ns["nanoflare-internal:dns"].JS, "invocationDNS()") || strings.Contains(ns["nanoflare-internal:dns"].JS, "env.__NANOFLARE_DNS")) {
				t.Fatal("namespace shim uses cached environment")
			}
		})
	}
}

func TestNamespaceCompilerRejectsReservedAndDuplicateModules(t *testing.T) {
	for _, name := range []string{"nanoflare-internal:dns", "nanoflare-internal:dns/promises", "nanoflare-internal:dynamic-env.js", "__nanoflare_dynamic_entrypoint__.js", "worker.js"} {
		d := nanoflare.Deployment{CompatibilityDate: "2026-09-11", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}, {Path: name, Content: "export default {}"}}}
		if _, err := NamespaceWorkerModules(d); err == nil {
			t.Fatalf("accepted collision %q", name)
		}
	}
}

func TestNamespaceCompilerPreservesNonBreakingSpace(t *testing.T) {
	const source = "export const value = 'a\u00a0b'; export const pattern = /\u00a0/; export const raw = String.raw`\u00a0`;"
	for _, date := range []string{"2025-12-10", "2026-09-11"} {
		d := nanoflare.Deployment{CompatibilityDate: date, Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: source}}}
		modules, err := NamespaceWorkerModules(d)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(modules["worker.js"].JS, "a b") {
			t.Fatal("changed string value")
		}
		if date == "2025-12-10" && modules["worker.js"].JS != source {
			t.Fatal("changed source without Node adaptation")
		}
	}
}
