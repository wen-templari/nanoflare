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
			fmt.Fprintf(out, "    (name = %s, external = (address = %s, http = (injectRequestHeaders = [(name = \"Authorization\", value = %s), (name = \"X-Nanoflare-Dispatch-Namespace-ID\", value = %s)]))),\n", capnpQuote(dispatchServiceName(item, index)), capnpQuote(runtimeAddr), capnpQuote("Bearer "+nanoflare.RuntimeCapability(item, nanoflare.RuntimeDispatch, binding.NamespaceID)), capnpQuote(binding.NamespaceID))
		}
	}
}
func dispatchWorkerBindings(item nanoflare.ActiveDeployment) []string {
	if len(item.Deployment.DispatchNamespaces) == 0 {
		return nil
	}
	bindings := []string{`(name = "__NANOFLARE_WORKER_LOADER", workerLoader = ())`, `(name = "__NANOFLARE_OUTBOUND_NETWORK", service = "internet")`}
	mapping := map[string]string{}
	for index, binding := range item.Deployment.DispatchNamespaces {
		control := fmt.Sprintf("__NANOFLARE_DISPATCH_CONTROL_%d", index)
		mapping[binding.Binding] = control
		bindings = append(bindings, fmt.Sprintf("(name = %s, service = %s)", capnpQuote(control), capnpQuote(dispatchServiceName(item, index))))
	}
	data, _ := json.Marshal(mapping)
	return append(bindings, fmt.Sprintf("(name = \"__NANOFLARE_DISPATCH_BINDINGS\", json = %s)", capnpQuote(string(data))))
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
	return renderWorkerJavaScript("dynamic-entrypoint", struct {
		Entrypoint string
		Metadata   string
	}{
		Entrypoint: jsStringLiteral("./" + d.Entrypoint),
		Metadata:   string(metadata),
	})
}
