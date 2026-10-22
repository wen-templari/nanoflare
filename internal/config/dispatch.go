package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func staticDeployments(active []nanoflare.ActiveDeployment) []nanoflare.ActiveDeployment {
	out := make([]nanoflare.ActiveDeployment, 0, len(active))
	for _, item := range active {
		if !item.App.IsDynamic() {
			out = append(out, item)
		}
	}
	return out
}
func dispatchServiceName(item nanoflare.ActiveDeployment, index int) string {
	return fmt.Sprintf("%s-dispatch-%d", deploymentServiceName(item), index)
}
func writeDispatchServices(out *strings.Builder, runtimeAddr string, active []nanoflare.ActiveDeployment) {
	for _, item := range active {
		for index, binding := range item.Deployment.DispatchNamespaces {
			fmt.Fprintf(out, "    (name = %s, external = (address = %s, http = (injectRequestHeaders = [(name = \"Authorization\", value = %s), (name = \"X-Nanoflare-Dispatch-Namespace-ID\", value = %s)]))),\n", quote(dispatchServiceName(item, index)), quote(runtimeAddr), quote("Bearer "+nanoflare.RuntimeCapability(item, nanoflare.RuntimeDispatch, binding.NamespaceID)), quote(binding.NamespaceID))
		}
	}
}
func dispatchWorkerBindings(item nanoflare.ActiveDeployment) []string {
	if len(item.Deployment.DispatchNamespaces) == 0 {
		return nil
	}
	bindings := []string{`(name = "__NANOFLARE_WORKER_LOADER", workerLoader = ())`}
	mapping := map[string]string{}
	for index, binding := range item.Deployment.DispatchNamespaces {
		control := fmt.Sprintf("__NANOFLARE_DISPATCH_CONTROL_%d", index)
		mapping[binding.Binding] = control
		bindings = append(bindings, fmt.Sprintf("(name = %s, service = %s)", quote(control), quote(dispatchServiceName(item, index))))
	}
	data, _ := json.Marshal(mapping)
	return append(bindings, fmt.Sprintf("(name = \"__NANOFLARE_DISPATCH_BINDINGS\", json = %s)", quote(string(data))))
}

// DynamicEntrypoint wraps module handlers without capturing a request capability
// in revision-cached code. Native props retain their workerd RPC lifetime.
func DynamicEntrypoint(d nanoflare.Deployment) string {
	metadata, _ := json.Marshal(struct {
		KV        []nanoflare.KVBinding                  `json:"kv_namespaces"`
		DB        []nanoflare.DatabaseBinding            `json:"db"`
		Objects   []nanoflare.ObjectStorageBucketBinding `json:"object_storage_buckets"`
		Services  []nanoflare.ServiceBinding             `json:"services"`
		HasAssets bool                                   `json:"has_assets"`
		Assets    nanoflare.AssetConfig                  `json:"asset_config"`
	}{d.KVNamespaces, d.Databases, d.ObjectStorageBuckets, d.Services, len(d.Assets) > 0, d.AssetConfig})
	header := fmt.Sprintf("import { dynamicEnv, withInvocation } from \"nanoflare-internal:dynamic-env.js\";\nimport userWorker from %s;\nconst metadata=%s;\n", quote("./"+d.Entrypoint), string(metadata))
	return header + `async function route(request, env, run) {
 const config=metadata.asset_config || {};
 const pathname=new URL(request.url).pathname;
 let workerFirst=config.run_worker_first===true;
 if(Array.isArray(config.run_worker_first)) for(const route of config.run_worker_first) {
  const negated=route.startsWith("!"); const pattern=negated?route.slice(1):route;
  if(pattern.endsWith("*")?pathname.startsWith(pattern.slice(0,-1)):pathname===pattern) workerFirst=!negated;
 }
 if(metadata.has_assets && !workerFirst && (request.method==="GET" || request.method==="HEAD")) {
  const response=await env[config.binding || "ASSETS"].fetch(request);
  if(response.status!==404) return response;
  await response.body?.cancel();
 }
 return run();
}
const handler = typeof userWorker === "function" ? class extends userWorker {
 constructor(ctx,env) { super(ctx,dynamicEnv(env,ctx,metadata)); }
 fetch(request) { return withInvocation(this.ctx,()=>route(request,this.env,()=>super.fetch(request))); }
} : {
 async fetch(request,env,ctx) { const wrapped=dynamicEnv(env,ctx,metadata); return withInvocation(ctx,()=>route(request,wrapped,()=>typeof userWorker.fetch==="function"?userWorker.fetch(request,wrapped,ctx):new Response("fetch handler not found",{status:404}))); }
};
export default handler;`
}
