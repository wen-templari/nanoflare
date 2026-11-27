package config

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
)

// SnapshotWorkerCodeForLoader adapts legacy fetch listeners to the module
// entrypoint required by WorkerLoader. Bindings are immutable per loaded Worker,
// so installing its own environment on its globals cannot cross revisions.
func SnapshotWorkerCodeForLoader(item nanoflare.ActiveDeployment, options WorkerdOptions) WorkerCodeSnapshot {
	code := SnapshotWorkerCode(item, options)
	if strings.TrimSpace(options.EgressAddr) != "" {
		main := code.Modules[code.MainModule]
		main.JS = `import "nanoflare-internal:egress-fetch";` + "\n" + main.JS
		code.Modules[code.MainModule] = main
		code.Modules["nanoflare-internal:egress-fetch"] = WorkerCodeModule{JS: egressFetchSource}
		code.Modules[loaderInvocationContextModule] = WorkerCodeModule{JS: loaderInvocationContextSource}
		code.moduleOrder = append(code.moduleOrder, "nanoflare-internal:egress-fetch", loaderInvocationContextModule)
	}
	if deploymentFormat(item.Deployment) != "service-worker" {
		return code
	}
	main := code.Modules[code.MainModule]
	names := []string{}
	resourceNames := []string{}
	for name := range item.Deployment.Vars {
		names = append(names, name)
	}
	for name := range item.App.SecretValues {
		names = append(names, name)
	}
	for _, binding := range item.Deployment.KVNamespaces {
		names = append(names, binding.Binding)
		resourceNames = append(resourceNames, binding.Binding)
	}
	for _, binding := range item.Deployment.Databases {
		names = append(names, binding.Binding)
		resourceNames = append(resourceNames, binding.Binding)
	}
	for _, binding := range item.Deployment.ObjectStorageBuckets {
		names = append(names, binding.Binding)
		resourceNames = append(resourceNames, binding.Binding)
	}
	for _, binding := range item.Deployment.Services {
		names = append(names, binding.Binding)
		resourceNames = append(resourceNames, binding.Binding)
	}
	if len(item.Deployment.Assets) > 0 || item.Deployment.AssetConfig.Binding != "" {
		names = append(names, assetBindingName(item.Deployment.AssetConfig))
		resourceNames = append(resourceNames, assetBindingName(item.Deployment.AssetConfig))
	}
	namesJSON, _ := json.Marshal(names)
	resourceNamesJSON, _ := json.Marshal(resourceNames)
	// Global resource values must be resolved inside the invocation rather than
	// capturing the native broker environment during module evaluation.
	main.JS = strings.Replace(main.JS, "installGlobalBindings(globalThis, __nanoflareDBBindingNames, __nanoflareObjectBindingNames);", "", 1)
	main.JS = renderWorkerJavaScript("legacy-loader", struct {
		ContextModule    string
		AssetBinding     string
		DatabaseBindings string
		ObjectBindings   string
		ResourceNames    string
		BindingNames     string
		Script           string
	}{
		ContextModule:    jsStringLiteral(loaderInvocationContextModule),
		AssetBinding:     jsStringLiteral(assetBindingName(item.Deployment.AssetConfig)),
		DatabaseBindings: dbBindingNamesJSON(item.Deployment.Databases),
		ObjectBindings:   objectBindingNamesJSON(item.Deployment.ObjectStorageBuckets),
		ResourceNames:    string(resourceNamesJSON),
		BindingNames:     string(namesJSON),
		Script:           main.JS,
	})
	code.Modules[code.MainModule] = main
	code.Modules[loaderInvocationContextModule] = WorkerCodeModule{JS: loaderInvocationContextSource}
	if !slices.Contains(code.moduleOrder, loaderInvocationContextModule) {
		code.moduleOrder = append(code.moduleOrder, loaderInvocationContextModule)
	}
	return code
}
