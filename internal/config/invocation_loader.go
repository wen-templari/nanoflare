package config

import (
	_ "embed"
	"encoding/json"
	"maps"
	"slices"
	"strconv"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtimeassets"
)

const (
	loaderInvocationContextModule = "nanoflare-internal:invocation-context"
	importedEnvModule             = "__nanoflare_imported_env__.js"
)

// InstrumentLoaderInvocation adds revision-bound resource capabilities and
// invocation completion accounting. The input snapshot is not mutated. The parent must
// admit an invocation before supplying its private props to getEntrypoint().
func InstrumentLoaderInvocation(code WorkerCodeSnapshot, active nanoflare.ActiveDeployment) WorkerCodeSnapshot {
	// Request signals cross internal RPC bridges independently of the user's
	// compatibility date. The pinned native runtime implements their transport.
	code.CompatibilityFlags = slices.Clone(code.CompatibilityFlags)
	if !slices.Contains(code.CompatibilityFlags, "enable_abortsignal_rpc") {
		code.CompatibilityFlags = append(code.CompatibilityFlags, "enable_abortsignal_rpc")
	}
	code.Modules = maps.Clone(code.Modules)
	code.moduleOrder = slices.Clone(code.moduleOrder)
	declarations := struct {
		KV       []string `json:"kv"`
		Fetch    []string `json:"fetch"`
		Services []string `json:"services"`
		Dispatch []string `json:"dispatch"`
		Loaders  []string `json:"loaders,omitempty"`
	}{KV: []string{}, Fetch: []string{}, Services: []string{}}
	for _, binding := range active.Deployment.KVNamespaces {
		declarations.KV = append(declarations.KV, binding.Binding)
	}
	for _, binding := range active.Deployment.Databases {
		declarations.Fetch = append(declarations.Fetch, binding.Binding)
	}
	for _, binding := range active.Deployment.ObjectStorageBuckets {
		declarations.Fetch = append(declarations.Fetch, binding.Binding)
	}
	if len(active.Deployment.Assets) > 0 || active.Deployment.AssetConfig.Binding != "" {
		declarations.Fetch = append(declarations.Fetch, assetBindingName(active.Deployment.AssetConfig))
	}
	for _, binding := range active.Deployment.Services {
		declarations.Services = append(declarations.Services, binding.Binding)
	}
	for _, binding := range active.Deployment.DispatchNamespaces {
		declarations.Dispatch = append(declarations.Dispatch, binding.Binding)
	}
	for _, binding := range active.Deployment.WorkerLoaders {
		declarations.Loaders = append(declarations.Loaders, binding.Binding)
	}
	declarationJSON, _ := json.Marshal(declarations)
	type wasmImport struct {
		Identifier string
		Path       string
		Name       string
	}
	var wasmImports []wasmImport
	if len(declarations.Loaders) > 0 {
		for index, name := range slices.Sorted(maps.Keys(code.Modules)) {
			if code.Modules[name].Wasm == nil {
				continue
			}
			wasmImports = append(wasmImports, wasmImport{
				Identifier: "loaderWasm" + strconv.Itoa(index),
				Path:       jsStringLiteral("./" + name),
				Name:       jsStringLiteral(name),
			})
		}
	}
	importedEnv := renderWorkerJavaScript("imported-env", struct {
		ContextModule    string
		Declarations     string
		AssetBinding     string
		DatabaseBindings string
		ObjectBindings   string
		WasmImports      []wasmImport
	}{
		ContextModule:    jsStringLiteral(loaderInvocationContextModule),
		Declarations:     string(declarationJSON),
		AssetBinding:     jsStringLiteral(assetBindingName(active.Deployment.AssetConfig)),
		DatabaseBindings: dbBindingNamesJSON(active.Deployment.Databases),
		ObjectBindings:   objectBindingNamesJSON(active.Deployment.ObjectStorageBuckets),
		WasmImports:      wasmImports,
	})
	code.Modules[importedEnvModule] = WorkerCodeModule{JS: importedEnv}
	code.moduleOrder = append(code.moduleOrder, importedEnvModule)
	const main = "__nanoflare_invocation_entrypoint__.js"
	if existing, exists := code.Modules[loaderInvocationContextModule]; !exists || existing.JS != loaderInvocationContextSource {
		code.Modules[loaderInvocationContextModule] = WorkerCodeModule{JS: loaderInvocationContextSource}
		code.moduleOrder = append(code.moduleOrder, loaderInvocationContextModule)
	}
	entrypoint := renderWorkerJavaScript("invocation-entrypoint", struct {
		ImportedEnv   string
		Entrypoint    string
		ContextModule string
		Declarations  string
	}{
		ImportedEnv:   jsStringLiteral("./" + importedEnvModule),
		Entrypoint:    jsStringLiteral("./" + code.MainModule),
		ContextModule: jsStringLiteral(loaderInvocationContextModule),
		Declarations:  string(declarationJSON),
	})
	code.Modules[main] = WorkerCodeModule{JS: entrypoint}
	code.moduleOrder = append(code.moduleOrder, main)
	code.MainModule = main
	return code
}

// InstallOutboundParameters overlays request-local values without storing them in code.
func InstallOutboundParameters(code *WorkerCodeSnapshot, names []string) {
	encoded, _ := json.Marshal(append([]string{}, names...))
	module := code.Modules[importedEnvModule]
	module.JS += renderWorkerJavaScript("outbound-parameters", struct {
		ContextModule string
		Names         string
	}{
		ContextModule: jsStringLiteral(loaderInvocationContextModule),
		Names:         string(encoded),
	})
	code.Modules[importedEnvModule] = module
}

//go:embed workerjs/invocation-context.js
var loaderInvocationContextModuleSource string

var loaderInvocationContextSource = runtimeassets.DispatchOptionsModule + "\n" + runtimeassets.ExportPropsModule + "\n" + runtimeassets.ErrorsModule + "\n" + runtimeassets.FetchSignalModule + "\n" + applicationLoaderFacade + "\n" + loaderInvocationContextModuleSource
