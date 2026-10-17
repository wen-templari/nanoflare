package config

import (
	"encoding/json"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
)

// SnapshotWorkerCodeForLoader adapts legacy fetch listeners to the module
// entrypoint required by WorkerLoader. Bindings are immutable per loaded Worker,
// so installing its own environment on its globals cannot cross revisions.
func SnapshotWorkerCodeForLoader(item nanoflare.ActiveDeployment, options WorkerdOptions) WorkerCodeSnapshot {
	code := SnapshotWorkerCode(item, options)
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
	if len(item.Deployment.Assets) > 0 {
		names = append(names, assetBindingName(item.Deployment.AssetConfig))
		resourceNames = append(resourceNames, assetBindingName(item.Deployment.AssetConfig))
	}
	namesJSON, _ := json.Marshal(names)
	resourceNamesJSON, _ := json.Marshal(resourceNames)
	// Global resource values must be resolved inside the invocation rather than
	// capturing the native broker environment during module evaluation.
	main.JS = strings.Replace(main.JS, "installGlobalBindings(globalThis, __nanoflareDBBindingNames, __nanoflareObjectBindingNames);", "", 1)
	main.JS = `import { currentInvocationEnv as __nanoflareCurrentEnv } from "` + loaderInvocationContextModule + `";
import { wrapEnv as __nanoflareWrapLegacyEnv } from "nanoflare-internal:runtime-bindings";
function __nanoflareLegacyEnv() {
  return __nanoflareWrapLegacyEnv(__nanoflareCurrentEnv(), ` + quote(assetBindingName(item.Deployment.AssetConfig)) + `, ` + dbBindingNamesJSON(item.Deployment.Databases) + `, ` + objectBindingNamesJSON(item.Deployment.ObjectStorageBuckets) + `);
}
const __nanoflareLegacyResourceRefs = new Map(` + string(resourceNamesJSON) + `.map(name => [name, new Proxy({}, {
  get(_, method) {
    const resource = __nanoflareLegacyEnv()[name];
    const value = resource?.[method];
    return typeof value === "function" ? (...args) => Reflect.apply(value, resource, args) : value;
  },
})]));
for (const name of new Set([...Object.keys(__nanoflareCurrentEnv()), ...` + string(namesJSON) + `])) {
  if (["__NANOFLARE_RESOURCE_BROKER", "__NANOFLARE_SERVICE_BROKER", "__NANOFLARE_LIFECYCLE"].includes(name)) continue;
  Object.defineProperty(globalThis, name, { configurable: true, get() { return __nanoflareLegacyResourceRefs.get(name) ?? __nanoflareLegacyEnv()[name]; } });
}
const __nanoflareLegacyListeners = [];
globalThis.addEventListener = (type, listener) => {
  if (type === "fetch") __nanoflareLegacyListeners.push(listener);
};
` + main.JS + `
export default {
  async fetch(request, env, ctx) {
    let response;
    let responded = false;
    const event = {
      request,
      respondWith(value) {
        if (responded) throw new TypeError("respondWith() was already called");
        responded = true;
        response = Promise.resolve(value);
      },
      waitUntil(value) { ctx.waitUntil(value); },
      passThroughOnException() { ctx.passThroughOnException(); },
    };
    for (const listener of __nanoflareLegacyListeners) {
      listener(event);
      if (responded) break;
    }
    if (!responded) throw new TypeError("Legacy fetch listener did not call respondWith()");
    return response;
  },
};
`
	code.Modules[code.MainModule] = main
	code.Modules[loaderInvocationContextModule] = WorkerCodeModule{JS: loaderInvocationContextSource}
	code.moduleOrder = append(code.moduleOrder, loaderInvocationContextModule)
	return code
}
