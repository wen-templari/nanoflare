package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtimeassets"
)

// WorkerCodeModule uses an explicit type because internal module names need
// not have a .js suffix. Both static workerd and WorkerLoader support ES modules.
type WorkerCodeModule struct {
	JS   string `json:"js"`
	Wasm []byte `json:"wasm,omitempty"`
}

// MarshalJSON preserves the typed module union, including empty JavaScript.
// Binary Wasm uses JSON base64 for transport; the loader restores ArrayBuffer.
func (module WorkerCodeModule) MarshalJSON() ([]byte, error) {
	if module.Wasm != nil {
		if module.JS != "" {
			return nil, fmt.Errorf("compiled module mixes JavaScript and Wasm")
		}
		return json.Marshal(struct {
			Wasm []byte `json:"wasm"`
		}{module.Wasm})
	}
	return json.Marshal(struct {
		JS string `json:"js"`
	}{module.JS})
}

// MarshalJSON must not turn a statically invalid duplicate module list into a
// valid map which silently replaces Nanoflare's instrumentation or adapters.
func (code WorkerCodeSnapshot) MarshalJSON() ([]byte, error) {
	seen := make(map[string]bool, len(code.moduleOrder))
	for _, name := range code.moduleOrder {
		if seen[name] {
			return nil, fmt.Errorf("duplicate compiled Worker module %q", name)
		}
		seen[name] = true
	}
	type wireCode WorkerCodeSnapshot
	return json.Marshal(wireCode(code))
}

// WorkerCodeSnapshot is a detached description of compiled code, not an
// execution capability. It deliberately contains no credentials or bindings.
// A future execution identity must additionally include its effective environment.
type WorkerCodeSnapshot struct {
	CompatibilityDate  string                      `json:"compatibilityDate"`
	CompatibilityFlags []string                    `json:"compatibilityFlags"`
	MainModule         string                      `json:"mainModule"`
	Modules            map[string]WorkerCodeModule `json:"modules"`
	moduleOrder        []string
}

// SnapshotWorkerCode is the shared compiler for static config and dynamic
// loader admission. Its returned slices/maps do not alias the deployment.
func SnapshotWorkerCode(item nanoflare.ActiveDeployment, options WorkerdOptions) WorkerCodeSnapshot {
	deployment := item.Deployment
	nodeCompatibility := effectiveNodeCompatibility(deployment)
	dnsEnabled := strings.TrimSpace(options.DNSAddr) != "" && nodeCompatibility.runtime
	const mainModule = "__nanoflare_internal_entrypoint__.js"
	snapshot := WorkerCodeSnapshot{
		CompatibilityDate:  deployment.CompatibilityDate,
		CompatibilityFlags: runtimeCompatibilityFlags(deployment),
		MainModule:         mainModule,
		Modules:            make(map[string]WorkerCodeModule, len(deployment.Files)+4),
	}
	add := func(name, source string) {
		// Retain duplicates in the static representation so workerd continues
		// to reject them. The JSON representation rejects them above as well.
		snapshot.moduleOrder = append(snapshot.moduleOrder, name)
		snapshot.Modules[name] = WorkerCodeModule{JS: source}
	}
	addWasm := func(file nanoflare.WorkerFile) bool {
		if file.Wasm == nil {
			return false
		}
		snapshot.moduleOrder = append(snapshot.moduleOrder, file.Path)
		snapshot.Modules[file.Path] = WorkerCodeModule{Wasm: bytes.Clone(file.Wasm)}
		return true
	}
	if len(deployment.DispatchNamespaces) > 0 {
		source := entrypointWrapper(deployment.Entrypoint, assetBindingName(deployment.AssetConfig), deployment.Databases, deployment.ObjectStorageBuckets)
		source = `import { wrapDispatchEnv, DispatchBroker } from "nanoflare-internal:dispatch";
export { DispatchBroker };
` + source
		source = strings.ReplaceAll(source, "wrapRuntimeEnv(env, assetBindingName, dbBindingNames, objectBindingNames)", `wrapDispatchEnv(wrapRuntimeEnv(env, assetBindingName, dbBindingNames, objectBindingNames),ctx,request.headers.get("X-Nanoflare-Dispatch-Root"))`)
		add(mainModule, source)
		add("nanoflare-internal:dispatch", runtimeassets.DispatchModule)
		add("nanoflare-internal:runtime-bindings", runtimeassets.BindingsModule)
		for _, file := range entrypointFirst(deployment.Files, deployment.Entrypoint) {
			if addWasm(file) {
				continue
			}
			add(file.Path, workerdSafeSource(file.Content))
		}
	} else if deploymentFormat(deployment) == "service-worker" {
		source := deployment.Files[0].Content
		if dnsEnabled {
			source = rewriteDNSImports(source, nodeCompatibility.v2)
		}
		add(mainModule, serviceWorkerWrapper(workerdSafeSource(source), deployment.Databases, deployment.ObjectStorageBuckets))
		add("nanoflare-internal:runtime-bindings", runtimeassets.BindingsModule)
	} else {
		if workerEntrypointModule(deployment) {
			add(mainModule, workerEntrypointInstrumentationWrapper(deployment.Entrypoint, assetBindingName(deployment.AssetConfig), deployment.Databases, deployment.ObjectStorageBuckets))
		} else {
			add(mainModule, entrypointWrapper(deployment.Entrypoint, assetBindingName(deployment.AssetConfig), deployment.Databases, deployment.ObjectStorageBuckets))
		}
		add("nanoflare-internal:runtime-bindings", runtimeassets.BindingsModule)
		for _, file := range entrypointFirst(deployment.Files, deployment.Entrypoint) {
			if addWasm(file) {
				continue
			}
			source := file.Content
			if dnsEnabled {
				source = rewriteDNSImportsForModule(source, nodeCompatibility.v2, file.Path)
			}
			add(file.Path, workerdSafeSource(source))
		}
	}
	if dnsEnabled {
		add("nanoflare-internal:dns", dnsShimSource())
		add("nanoflare-internal:dns/promises", dnsPromisesShimSource())
	}
	return snapshot
}
