package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtimeassets"
)

type Writer struct {
	workerdPath  string
	traefikPath  string
	authURL      string
	authHost     string
	workerHost   string
	runtimeAddr  string
	networkAllow []string
	egressAddr   string
	dnsAddr      string
	dnsProfiles  map[string]bool
}

type TraefikWriter interface {
	WriteTraefik([]nanoflare.ActiveDeployment) error
}

type RuntimeWriter struct {
	workerdPath  string
	traefik      TraefikWriter
	runtimeAddr  string
	networkAllow []string
	egressAddr   string
	dnsAddr      string
	dnsProfiles  map[string]bool
}

func NewWriter(workerdPath, traefikPath, authURL, workerHost string) *Writer {
	return &Writer{workerdPath: workerdPath, traefikPath: traefikPath, authURL: authURL, workerHost: workerHost, runtimeAddr: "127.0.0.1:8081", networkAllow: DefaultNetworkAllow()}
}

func (w *Writer) SetAuthHost(host string) {
	w.authHost = strings.TrimSpace(host)
}

func NewRuntimeWriter(workerdPath string, traefik TraefikWriter) *RuntimeWriter {
	return &RuntimeWriter{workerdPath: workerdPath, traefik: traefik, runtimeAddr: "127.0.0.1:8081", networkAllow: DefaultNetworkAllow()}
}

func (w *Writer) SetNanoflareRuntimeAddr(addr string) {
	w.runtimeAddr = addr
}

func (w *RuntimeWriter) SetNanoflareRuntimeAddr(addr string) {
	w.runtimeAddr = addr
}

func (w *Writer) SetWorkerdEgressAddr(addr string)        { w.egressAddr = strings.TrimSpace(addr) }
func (w *RuntimeWriter) SetWorkerdEgressAddr(addr string) { w.egressAddr = strings.TrimSpace(addr) }
func (w *Writer) SetWorkerdDNSAddr(addr string)           { w.dnsAddr = strings.TrimSpace(addr) }
func (w *RuntimeWriter) SetWorkerdDNSAddr(addr string)    { w.dnsAddr = strings.TrimSpace(addr) }
func (w *Writer) SetDNSProfiles(profiles []string)        { w.dnsProfiles = profileSet(profiles) }
func (w *RuntimeWriter) SetDNSProfiles(profiles []string) { w.dnsProfiles = profileSet(profiles) }

func DefaultNetworkAllow() []string {
	return []string{"public", "10.0.0.0/8"}
}

func ParseNetworkAllow(value string) []string {
	parts := strings.Split(value, ",")
	allow := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			allow = append(allow, part)
		}
	}
	if len(allow) == 0 {
		return DefaultNetworkAllow()
	}
	return allow
}

func (w *Writer) SetNetworkAllow(allow []string) {
	w.networkAllow = normalizedNetworkAllow(allow)
}

func (w *RuntimeWriter) SetNetworkAllow(allow []string) {
	w.networkAllow = normalizedNetworkAllow(allow)
}

func (w *Writer) Write(active []nanoflare.ActiveDeployment) error {
	if err := w.WriteWorkerd(w.workerdPath, active); err != nil {
		return err
	}
	return w.WriteTraefik(active)
}

func (w *Writer) WriteWorkerd(path string, active []nanoflare.ActiveDeployment) error {
	if err := validateDNSProfiles(active, w.dnsProfiles); err != nil {
		return err
	}
	return writeAtomic(path, []byte(WorkerdWithOptions(active, WorkerdOptions{RuntimeAddr: w.runtimeAddr, NetworkAllow: w.networkAllow, EgressAddr: w.egressAddr, DNSAddr: w.dnsAddr})))
}

func (w *Writer) WriteTraefik(active []nanoflare.ActiveDeployment) error {
	return writeAtomic(w.traefikPath, []byte(Traefik(active, w.authURL, w.authHost, w.workerHost)))
}

func (w *RuntimeWriter) WriteWorkerd(path string, active []nanoflare.ActiveDeployment) error {
	if err := validateDNSProfiles(active, w.dnsProfiles); err != nil {
		return err
	}
	return writeAtomic(path, []byte(WorkerdWithOptions(active, WorkerdOptions{RuntimeAddr: w.runtimeAddr, NetworkAllow: w.networkAllow, EgressAddr: w.egressAddr, DNSAddr: w.dnsAddr})))
}

func profileSet(profiles []string) map[string]bool {
	result := make(map[string]bool, len(profiles))
	for _, profile := range profiles {
		result[profile] = true
	}
	return result
}

func validateDNSProfiles(active []nanoflare.ActiveDeployment, profiles map[string]bool) error {
	if len(profiles) == 0 {
		return nil
	}
	for _, item := range active {
		profile := strings.TrimSpace(item.Deployment.DNS.Profile)
		if profile != "" && !profiles[profile] {
			return fmt.Errorf("worker %q selects unknown DNS profile %q", item.App.Name, profile)
		}
	}
	return nil
}

func (w *RuntimeWriter) WriteTraefik(active []nanoflare.ActiveDeployment) error {
	return w.traefik.WriteTraefik(active)
}

func Workerd(active []nanoflare.ActiveDeployment) string {
	return WorkerdWithRuntimeAddr(active, "127.0.0.1:8081")
}

func WorkerdWithRuntimeAddr(active []nanoflare.ActiveDeployment, runtimeAddr string) string {
	return WorkerdWithOptions(active, WorkerdOptions{RuntimeAddr: runtimeAddr})
}

type WorkerdOptions struct {
	RuntimeAddr  string
	NetworkAllow []string
	EgressAddr   string
	DNSAddr      string
}

func WorkerdWithOptions(active []nanoflare.ActiveDeployment, options WorkerdOptions) string {
	active = staticDeployments(active)
	runtimeAddr := strings.TrimSpace(options.RuntimeAddr)
	if runtimeAddr == "" {
		runtimeAddr = "127.0.0.1:8081"
	}
	networkAllow := normalizedNetworkAllow(options.NetworkAllow)
	var out strings.Builder
	out.WriteString("using Workerd = import \"/workerd/workerd.capnp\";\n\n")
	out.WriteString("const config :Workerd.Config = (\n  services = [\n")
	fmt.Fprintf(&out, "    (name = \"internet\", network = (allow = [%s], tlsOptions = (trustBrowserCas = true))),\n", quotedList(networkAllow))
	if egressAddr := strings.TrimSpace(options.EgressAddr); egressAddr != "" {
		fmt.Fprintf(&out, "    (name = \"nanoflare-egress\", external = (address = %s, http = (style = proxy))),\n", quote(egressAddr))
	}
	out.WriteString(durationTelemetryServices(runtimeAddr, active))
	writeDispatchServices(&out, runtimeAddr, active)
	for _, item := range active {
		fmt.Fprintf(&out, "    (name = %s, worker = .%s),\n", quote(deploymentServiceName(item)), workerName(deploymentServiceName(item)))
	}
	for _, item := range active {
		if dnsAddr := strings.TrimSpace(options.DNSAddr); dnsAddr != "" && dnsCompatibilityEnabled(item.Deployment) {
			fmt.Fprintf(&out, "    (name = %s, external = (address = %s, http = (injectRequestHeaders = [(name = \"X-Nanoflare-DNS-Profile\", value = %s)]))),\n",
				quote(dnsServiceName(item)), quote(dnsAddr), quote(item.Deployment.DNS.Profile))
		}
		for index, binding := range item.Deployment.KVNamespaces {
			fmt.Fprintf(&out, "    (name = %s, external = (address = %s, http = (injectRequestHeaders = [(name = \"Authorization\", value = %s), (name = \"X-Nanoflare-KV-Namespace-ID\", value = %s)]))),\n",
				quote(kvServiceName(item, index)), quote(runtimeAddr), quote("Bearer "+nanoflare.RuntimeCapability(item, nanoflare.RuntimeKV, binding.ID)), quote(binding.ID))
		}
		for index, binding := range item.Deployment.Databases {
			fmt.Fprintf(&out, "    (name = %s, external = (address = %s, http = (injectRequestHeaders = [(name = \"Authorization\", value = %s), (name = \"X-Nanoflare-Binding\", value = \"db\"), (name = \"X-Nanoflare-Database-ID\", value = %s)]))),\n",
				quote(dbServiceName(item, index)), quote(runtimeAddr), quote("Bearer "+nanoflare.RuntimeCapability(item, nanoflare.RuntimeDatabase, binding.DatabaseID)), quote(binding.DatabaseID))
		}
		fmt.Fprintf(&out, "    (name = %s, external = (address = %s, http = (injectRequestHeaders = [(name = \"Authorization\", value = %s), (name = \"X-Nanoflare-Binding\", value = \"assets\"), (name = \"X-Nanoflare-Deployment-ID\", value = %s)]))),\n",
			quote(assetServiceName(item)), quote(runtimeAddr), quote("Bearer "+nanoflare.RuntimeCapability(item, nanoflare.RuntimeAssets, item.Deployment.ID)), quote(item.Deployment.ID))
		for index, binding := range item.Deployment.ObjectStorageBuckets {
			fmt.Fprintf(&out, "    (name = %s, external = (address = %s, http = (injectRequestHeaders = [(name = \"Authorization\", value = %s), (name = \"X-Nanoflare-Object-Bucket-ID\", value = %s)]))),\n",
				quote(objectServiceName(item, index)), quote(runtimeAddr), quote("Bearer "+nanoflare.RuntimeCapability(item, nanoflare.RuntimeObjects, binding.BucketID)), quote(binding.BucketID))
		}
	}
	out.WriteString("  ],\n\n  sockets = [\n")
	for _, item := range active {
		if item.App.IsPrivate() && item.SocketPath == "" {
			continue
		}
		address := fmt.Sprintf("*:%d", item.Deployment.Port)
		if item.SocketPath != "" {
			address = "unix:" + item.SocketPath
		}
		fmt.Fprintf(&out, "    (name = %s, address = %s, http = (forwardedProtoHeader = \"X-Forwarded-Proto\"), service = %s),\n",
			quote(deploymentServiceName(item)), quote(address), quote(deploymentServiceName(item)))
	}
	out.WriteString("  ]\n);\n")
	for _, item := range active {
		nodeCompatibility := effectiveNodeCompatibility(item.Deployment)
		dnsEnabled := strings.TrimSpace(options.DNSAddr) != "" && nodeCompatibility.runtime
		fmt.Fprintf(&out, "\nconst %s :Workerd.Worker = (\n", workerName(deploymentServiceName(item)))
		writeWorkerSource(&out, item, dnsEnabled, nodeCompatibility.v2)
		fmt.Fprintf(&out, "  tails = [%s],\n", quote(telemetryServiceName(item, "tail")))
		if strings.TrimSpace(options.EgressAddr) != "" {
			out.WriteString("  globalOutbound = \"nanoflare-egress\",\n")
		}
		fmt.Fprintf(&out, "  bindings = [%s],\n",
			strings.Join(workerBindings(item, active, dnsEnabled), ", "))
		fmt.Fprintf(&out, "  compatibilityDate = %s,\n", quote(item.Deployment.CompatibilityDate))
		fmt.Fprintf(&out, "  compatibilityFlags = [%s],\n", quotedList(runtimeCompatibilityFlags(item.Deployment)))
		out.WriteString(");\n")
	}
	return out.String()
}

// DispatchBroker is an internal RPC export even when the application pins a
// compatibility date before ctx.exports became enabled by default.
func runtimeCompatibilityFlags(deployment nanoflare.Deployment) []string {
	flags := outputIdentityCompatibilityFlags(deployment.CompatibilityFlags)
	if len(deployment.DispatchNamespaces) > 0 && deployment.CompatibilityDate < "2025-11-17" {
		found := false
		for _, flag := range flags {
			if flag == "enable_ctx_exports" {
				found = true
			}
		}
		if !found {
			flags = append(flags, "enable_ctx_exports")
		}
	}
	return flags
}

func outputIdentityCompatibilityFlags(flags []string) []string {
	result := append([]string(nil), flags...)
	for _, flag := range result {
		if flag == "nodejs_als" || flag == "nodejs_compat" {
			return result
		}
	}
	return append(result, "nodejs_als")
}

func normalizedNetworkAllow(allow []string) []string {
	normalized := make([]string, 0, len(allow))
	for _, item := range allow {
		item = strings.TrimSpace(item)
		if item != "" {
			normalized = append(normalized, item)
		}
	}
	if len(normalized) == 0 {
		return DefaultNetworkAllow()
	}
	return normalized
}

func quotedList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, quote(value))
	}
	return strings.Join(quoted, ", ")
}

func telemetryServiceName(item nanoflare.ActiveDeployment, kind string) string {
	return "nanoflare-" + kind + "-" + deploymentServiceName(item)
}

func durationTelemetryServices(runtimeAddr string, active []nanoflare.ActiveDeployment) string {
	var out strings.Builder
	for _, item := range active {
		for _, kind := range []string{nanoflare.RuntimeDurations, nanoflare.RuntimeLogs, nanoflare.RuntimeAccess} {
			fmt.Fprintf(&out, "    (name = %s, external = (address = %s, http = (injectRequestHeaders = [(name = \"Authorization\", value = %s)]))),\n", quote(telemetryServiceName(item, kind)), quote(runtimeAddr), quote("Bearer "+nanoflare.RuntimeCapability(item, kind, "")))
		}
		source := `export default { async tail(events, env) {
  await env.COLLECTOR.fetch("http://nanoflare.internal/internal/runtime/logs", {
    method: "POST", headers: {"content-type":"application/json"}, body: JSON.stringify(events)
  });
}};`
		fmt.Fprintf(&out, "    (name = %s, worker = (compatibilityDate = \"2025-01-01\", modules = [(name = \"tail.js\", esModule = %s)], bindings = [(name = \"COLLECTOR\", service = %s)])),\n", quote(telemetryServiceName(item, "tail")), quote(source), quote(telemetryServiceName(item, nanoflare.RuntimeLogs)))
	}
	return out.String()
}

func workerBindings(item nanoflare.ActiveDeployment, active []nanoflare.ActiveDeployment, dnsEnabled bool) []string {
	bindings := make([]string, 0, len(item.Deployment.Vars)+len(item.App.SecretValues)+len(item.Deployment.KVNamespaces)+len(item.Deployment.Databases)+len(item.Deployment.ObjectStorageBuckets)+len(item.Deployment.Services)+3)
	bindings = append(bindings,
		fmt.Sprintf("(name = \"__NANOFLARE_APP_ID\", text = %s)", quote(item.App.ID)),
		fmt.Sprintf("(name = \"__NANOFLARE_DEPLOYMENT_ID\", text = %s)", quote(item.Deployment.ID)),
		fmt.Sprintf("(name = \"__NANOFLARE_DURATION_COLLECTOR\", service = %s)", quote(telemetryServiceName(item, nanoflare.RuntimeDurations))),
	)
	if dnsEnabled {
		bindings = append(bindings, fmt.Sprintf("(name = \"__NANOFLARE_DNS\", service = %s)", quote(dnsServiceName(item))))
	}
	bindings = append(bindings, fmt.Sprintf("(name = \"__NANOFLARE_ACCESS\", service = %s)", quote(telemetryServiceName(item, nanoflare.RuntimeAccess))))
	bindings = append(bindings, dispatchWorkerBindings(item)...)
	varNames := make([]string, 0, len(item.Deployment.Vars))
	for name := range item.Deployment.Vars {
		varNames = append(varNames, name)
	}
	sort.Strings(varNames)
	for _, name := range varNames {
		value := item.Deployment.Vars[name]
		if len(value) > 0 && value[0] == '"' {
			var text string
			if err := json.Unmarshal(value, &text); err == nil {
				bindings = append(bindings, fmt.Sprintf("(name = %s, text = %s)", quote(name), quote(text)))
				continue
			}
		}
		bindings = append(bindings, fmt.Sprintf("(name = %s, json = %s)", quote(name), quote(string(value))))
	}
	secretNames := make([]string, 0, len(item.App.SecretValues))
	for name := range item.App.SecretValues {
		secretNames = append(secretNames, name)
	}
	sort.Strings(secretNames)
	for _, name := range secretNames {
		value := item.App.SecretValues[name]
		bindings = append(bindings, fmt.Sprintf("(name = %s, text = %s)", quote(name), quote(value)))
	}
	for index, binding := range item.Deployment.KVNamespaces {
		bindings = append(bindings, fmt.Sprintf("(name = %s, kvNamespace = %s)", quote(binding.Binding), quote(kvServiceName(item, index))))
	}
	for index, binding := range item.Deployment.Databases {
		bindings = append(bindings, fmt.Sprintf("(name = %s, service = %s)", quote(binding.Binding), quote(dbServiceName(item, index))))
	}
	bindings = append(bindings, fmt.Sprintf("(name = %s, service = %s)", quote(assetBindingName(item.Deployment.AssetConfig)), quote(assetServiceName(item))))
	for index, binding := range item.Deployment.ObjectStorageBuckets {
		bindings = append(bindings, fmt.Sprintf("(name = %s, service = %s)", quote(binding.Binding), quote(objectServiceName(item, index))))
	}
	for _, binding := range item.Deployment.Services {
		if target, ok := activeServiceBinding(active, item.App, binding); ok {
			bindings = append(bindings, fmt.Sprintf("(name = %s, service = %s)", quote(binding.Binding), quote(deploymentServiceName(target))))
		}
	}
	return bindings
}

func activeServiceBinding(active []nanoflare.ActiveDeployment, source nanoflare.App, binding nanoflare.ServiceBinding) (nanoflare.ActiveDeployment, bool) {
	var found nanoflare.ActiveDeployment
	id := ""
	for _, item := range active {
		match := binding.WorkerID != "" && item.App.ID == binding.WorkerID
		if binding.WorkerID == "" {
			match = item.App.Platform == nil && item.App.Name == binding.Service
		}
		if !match || !nanoflare.ServiceTargetAllowed(source, item.App) {
			continue
		}
		if id != "" && id != item.App.ID {
			return nanoflare.ActiveDeployment{}, false
		}
		if id == "" {
			found = item
			id = item.App.ID
		}
	}
	return found, id != ""
}

func writeWorkerSource(out *strings.Builder, item nanoflare.ActiveDeployment, dnsEnabled, nodeCompatibilityV2 bool) {
	deployment := item.Deployment
	if len(deployment.DispatchNamespaces) > 0 {
		writeDispatchPlatform(out, item)
		return
	}
	if deploymentFormat(deployment) == "service-worker" {
		source := deployment.Files[0].Content
		if dnsEnabled {
			source = rewriteDNSImports(source, nodeCompatibilityV2)
			fmt.Fprintf(out, "  modules = [(name = %s, esModule = %s), (name = \"nanoflare-internal:runtime-bindings\", esModule = %s), (name = \"nanoflare-internal:dns\", esModule = %s), (name = \"nanoflare-internal:dns/promises\", esModule = %s)],\n", quote("__nanoflare_internal_entrypoint__.js"), quote(serviceWorkerWrapper(workerdSafeSource(source), deployment.Databases, deployment.ObjectStorageBuckets)), quote(runtimeassets.BindingsModule), quote(dnsShimSource()), quote(dnsPromisesShimSource()))
			return
		}
		fmt.Fprintf(out, "  modules = [(name = %s, esModule = %s), (name = \"nanoflare-internal:runtime-bindings\", esModule = %s)],\n", quote("__nanoflare_internal_entrypoint__.js"), quote(serviceWorkerWrapper(workerdSafeSource(source), deployment.Databases, deployment.ObjectStorageBuckets)), quote(runtimeassets.BindingsModule))
		return
	}
	out.WriteString("  modules = [\n")
	if workerEntrypointModule(deployment) {
		fmt.Fprintf(out, "    (name = %s, esModule = %s),\n", quote("__nanoflare_internal_entrypoint__.js"), quote(workerEntrypointInstrumentationWrapper(deployment.Entrypoint)))
	} else {
		fmt.Fprintf(out, "    (name = %s, esModule = %s),\n", quote("__nanoflare_internal_entrypoint__.js"), quote(entrypointWrapper(deployment.Entrypoint, assetBindingName(deployment.AssetConfig), deployment.Databases, deployment.ObjectStorageBuckets)))
	}
	fmt.Fprintf(out, "    (name = \"nanoflare-internal:runtime-bindings\", esModule = %s),\n", quote(runtimeassets.BindingsModule))
	for _, file := range entrypointFirst(deployment.Files, deployment.Entrypoint) {
		source := file.Content
		if dnsEnabled {
			source = rewriteDNSImportsForModule(source, nodeCompatibilityV2, file.Path)
		}
		fmt.Fprintf(out, "    (name = %s, esModule = %s),\n", quote(file.Path), quote(workerdSafeSource(source)))
	}
	if dnsEnabled {
		fmt.Fprintf(out, "    (name = \"nanoflare-internal:dns\", esModule = %s),\n", quote(dnsShimSource()))
		fmt.Fprintf(out, "    (name = \"nanoflare-internal:dns/promises\", esModule = %s),\n", quote(dnsPromisesShimSource()))
	}
	out.WriteString("  ],\n")
}

const (
	nodeCompatibilityDefaultDate   = "2026-08-04"
	nodeCompatibilityV2ImpliedDate = "2024-09-23"
)

type nodeCompatibility struct {
	runtime bool
	v2      bool
}

// effectiveNodeCompatibility mirrors Cloudflare's date and flag rules. The
// disable flags win here so Nanoflare never injects a shim that depends on a
// Node runtime API the deployment explicitly disabled.
func effectiveNodeCompatibility(deployment nanoflare.Deployment) nodeCompatibility {
	flags := make(map[string]bool, len(deployment.CompatibilityFlags))
	for _, flag := range deployment.CompatibilityFlags {
		flags[flag] = true
	}

	runtimeEnabled := deployment.CompatibilityDate >= nodeCompatibilityDefaultDate || flags["nodejs_compat"]
	if flags["no_nodejs_compat"] {
		runtimeEnabled = false
	}

	v2Enabled := deployment.CompatibilityDate >= nodeCompatibilityDefaultDate || flags["nodejs_compat_v2"]
	if deployment.CompatibilityDate >= nodeCompatibilityV2ImpliedDate && flags["nodejs_compat"] {
		v2Enabled = true
	}
	if flags["no_nodejs_compat_v2"] {
		v2Enabled = false
	}

	return nodeCompatibility{runtime: runtimeEnabled, v2: v2Enabled}
}

func dnsCompatibilityEnabled(deployment nanoflare.Deployment) bool {
	return effectiveNodeCompatibility(deployment).runtime
}

func rewriteDNSImports(source string, includeBareImports bool) string {
	return rewriteDNSImportsForModule(source, includeBareImports, "")
}

func rewriteDNSImportsForModule(source string, includeBareImports bool, modulePath string) string {
	dnsModule := relativeModuleSpecifier(modulePath, "nanoflare-internal:dns")
	dnsPromisesModule := relativeModuleSpecifier(modulePath, "nanoflare-internal:dns/promises")
	replacements := []struct{ old, new string }{
		{`"node:dns/promises"`, `"` + dnsPromisesModule + `"`},
		{`'node:dns/promises'`, `'` + dnsPromisesModule + `'`},
		{`"node:dns"`, `"` + dnsModule + `"`},
		{`'node:dns'`, `'` + dnsModule + `'`},
	}
	if includeBareImports {
		replacements = append(replacements,
			struct{ old, new string }{`"dns/promises"`, `"` + dnsPromisesModule + `"`},
			struct{ old, new string }{`'dns/promises'`, `'` + dnsPromisesModule + `'`},
			struct{ old, new string }{`"dns"`, `"` + dnsModule + `"`},
			struct{ old, new string }{`'dns'`, `'` + dnsModule + `'`},
		)
	}
	for _, replacement := range replacements {
		source = strings.ReplaceAll(source, replacement.old, replacement.new)
	}
	return source
}

func relativeModuleSpecifier(modulePath, target string) string {
	moduleDir := path.Dir(path.Clean(modulePath))
	if moduleDir == "." {
		return target
	}
	relativeRoot, err := filepath.Rel(filepath.FromSlash(moduleDir), ".")
	if err != nil {
		return target
	}
	return path.Join(filepath.ToSlash(relativeRoot), target)
}

func dnsShimSource() string {
	return `import { env } from "cloudflare:workers";
import nativeDNS from "node:dns";

function normalizeOptions(options) {
  if (typeof options === "number") return { family: options, all: false };
  if (options == null) return { family: 0, all: false };
  let family = options.family || 0;
  if (family === "IPv4") family = 4;
  if (family === "IPv6") family = 6;
  return { family: Number(family), all: Boolean(options.all) };
}

async function lookupResult(hostname, options) {
  const normalized = normalizeOptions(options);
  if (![0, 4, 6].includes(normalized.family)) {
    const error = new TypeError("family must be 0, 4, or 6");
    error.code = "ERR_INVALID_ARG_VALUE";
    throw error;
  }
  const response = await env.__NANOFLARE_DNS.fetch("http://nanoflare.dns/lookup", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ hostname: String(hostname), family: normalized.family }),
  });
  const body = await response.json();
  if (!response.ok) {
    const error = new Error(body.error || "hostname lookup failed");
    error.code = body.code || "EAI_AGAIN";
    error.hostname = hostname;
    error.syscall = "getaddrinfo";
    throw error;
  }
  if (!body.addresses?.length) {
    const error = new Error("hostname lookup returned no addresses");
    error.code = "ENOTFOUND";
    throw error;
  }
  return normalized.all ? body.addresses : body.addresses[0];
}

export function lookup(hostname, options, callback) {
  if (typeof options === "function") { callback = options; options = undefined; }
  if (typeof callback !== "function") throw new TypeError("callback must be a function");
  const normalized = normalizeOptions(options);
  lookupResult(hostname, options).then(
    (result) => normalized.all ? callback(null, result) : callback(null, result.address, result.family),
    (error) => callback(error),
  );
}

export const promises = { ...nativeDNS.promises, lookup: lookupResult };
export * from "node:dns";
export default { ...nativeDNS, lookup, promises };`
}

func dnsPromisesShimSource() string {
	dnsModule := relativeModuleSpecifier("nanoflare-internal:dns/promises", "nanoflare-internal:dns")
	return `import { promises } from "` + dnsModule + `";
export * from "node:dns/promises";
export const lookup = promises.lookup;
export default promises;`
}

// workerd recognizes a WorkerEntrypoint only when it remains the module's
// default export. RPC modules therefore use an instrumentation module which
// leaves that export intact while wrapping its public prototype methods.
func workerEntrypointModule(deployment nanoflare.Deployment) bool {
	if deploymentFormat(deployment) != "modules" {
		return false
	}
	for _, file := range deployment.Files {
		if file.Path == deployment.Entrypoint {
			return regexp.MustCompile(`(?m)export\s+default\s+class\b`).MatchString(file.Content)
		}
	}
	return false
}

func workerEntrypointInstrumentationWrapper(entrypoint string) string {
	return fmt.Sprintf(`import userWorker from %s;

function withOutputIdentity(_env, callback) { return callback(); }

function recordRuntimeDuration(env, ctx, startedAt, outcome) {
  const durationMs = Math.max(1, Date.now() - startedAt);
  ctx?.waitUntil?.(env.__NANOFLARE_DURATION_COLLECTOR.fetch("http://nanoflare.internal/internal/runtime/durations", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify([{
      scriptName: env.__NANOFLARE_APP_ID,
      eventTimestamp: Date.now(),
      durationMs,
      outcome,
    }]),
  }));
}

for (const name of Object.getOwnPropertyNames(userWorker.prototype)) {
  if (name === "constructor") continue;
  const original = userWorker.prototype[name];
  if (typeof original !== "function") continue;
  Object.defineProperty(userWorker.prototype, name, {
    configurable: true,
    writable: true,
    value: function (...args) {
      const env = this.env;
      const ctx = this.ctx;
      return withOutputIdentity(env, () => {
        const startedAt = Date.now();
        try {
          const result = original.apply(this, args);
          if (result && typeof result.then === "function") {
            return result.then(
              (value) => {
                recordRuntimeDuration(env, ctx, startedAt, "ok");
                return value;
              },
              (error) => {
                recordRuntimeDuration(env, ctx, startedAt, "exception");
                throw error;
              },
            );
          }
          recordRuntimeDuration(env, ctx, startedAt, "ok");
          return result;
        } catch (error) {
          recordRuntimeDuration(env, ctx, startedAt, "exception");
          throw error;
        }
      });
    },
  });
}

export default userWorker;`, quote("./"+strings.TrimPrefix(entrypoint, "./")))
}

// workerd rejects U+00A0 in module text before it evaluates the Worker. JSX
// compilers can emit that character from an &nbsp; entity, including in code
// paths that are never executed. Treat it as ordinary whitespace at the
// workerd-config boundary while preserving the uploaded deployment source.
func workerdSafeSource(source string) string {
	return strings.ReplaceAll(source, "\u00a0", " ")
}

func entrypointWrapper(entrypoint, binding string, dbBindings []nanoflare.DatabaseBinding, objectBindings []nanoflare.ObjectStorageBucketBinding) string {
	return fmt.Sprintf(`import { wrapEnv as wrapRuntimeEnv } from "nanoflare-internal:runtime-bindings";
import userWorker from %s;
export * from %s;

const assetBindingName = %s;
const dbBindingNames = %s;
const objectBindingNames = %s;

function withOutputIdentity(_env, callback) { return callback(); }

function recordRuntimeDuration(env, ctx, startedAt, outcome) {
  const durationMs = Math.max(1, Date.now() - startedAt);
  const payload = [{
    scriptName: env.__NANOFLARE_APP_ID,
    eventTimestamp: Date.now(),
    durationMs,
    outcome,
  }];
  ctx?.waitUntil?.(env.__NANOFLARE_DURATION_COLLECTOR.fetch("http://nanoflare.internal/internal/runtime/durations", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(payload),
  }));
}

function scheduledController(request) {
  const url = new URL(request.url);
  const cron = url.searchParams.get("cron") || "* * * * *";
  const rawTime = Number(url.searchParams.get("time") || "");
  const scheduledTime = Number.isFinite(rawTime) && rawTime > 0 ? (rawTime < 1000000000000 ? rawTime * 1000 : rawTime) : Date.now();
  return { scheduledTime, cron, noRetry() {} };
}

async function runScheduled(request, env, ctx) {
  if (typeof userWorker.scheduled !== "function") {
    return new Response("scheduled handler not found", { status: 404 });
  }
  return withOutputIdentity(env, async () => {
    const startedAt = Date.now();
    try {
      await userWorker.scheduled(scheduledController(request), wrapRuntimeEnv(env, assetBindingName, dbBindingNames, objectBindingNames), ctx);
      recordRuntimeDuration(env, ctx, startedAt, "ok");
      return new Response(null, { status: 204 });
    } catch (error) {
      recordRuntimeDuration(env, ctx, startedAt, "exception");
      throw error;
    }
  });
}

export default {
  ...userWorker,
  async fetch(request, env, ctx) {
    if (new URL(request.url).pathname === "/cdn-cgi/handler/scheduled") {
      return runScheduled(request, env, ctx);
    }
    const assertion = request.headers.get("X-Nanoflare-User-JWT");
    if (assertion && env.__NANOFLARE_ACCESS) {
      let identity;
      Object.defineProperty(ctx, "access", { value: Object.freeze({
        getIdentity() {
          return identity ??= env.__NANOFLARE_ACCESS.fetch("https://nanoflare.internal/internal/runtime/access", {
            headers: {"X-Nanoflare-User-JWT": assertion}
          }).then(async response => {
            if (!response.ok) throw new Error("Access identity is unavailable");
            return response.json();
          });
        }
      }) });
      const headers = new Headers(request.headers);
      headers.delete("X-Nanoflare-User-JWT");
      request = new Request(request, { headers });
    }
    return withOutputIdentity(env, async () => {
      const startedAt = Date.now();
      try {
        if (typeof userWorker.fetch !== "function") {
          return new Response("fetch handler not found", { status: 404 });
        }
        const response = await userWorker.fetch(request, wrapRuntimeEnv(env, assetBindingName, dbBindingNames, objectBindingNames), ctx);
        recordRuntimeDuration(env, ctx, startedAt, "ok");
        return response;
      } catch (error) {
        recordRuntimeDuration(env, ctx, startedAt, "exception");
        throw error;
      }
    });
  },
};`, quote("./"+strings.TrimPrefix(entrypoint, "./")), quote("./"+strings.TrimPrefix(entrypoint, "./")), quote(binding), dbBindingNamesJSON(dbBindings), objectBindingNamesJSON(objectBindings))
}

func serviceWorkerWrapper(script string, dbBindings []nanoflare.DatabaseBinding, objectBindings []nanoflare.ObjectStorageBucketBinding) string {
	return `import { installGlobalBindings } from "nanoflare-internal:runtime-bindings";

function __nanoflareRecordRuntimeDuration(startedAt, outcome) {
  const durationMs = Math.max(1, Date.now() - startedAt);
  const payload = [{
    scriptName: globalThis.__NANOFLARE_APP_ID,
    eventTimestamp: Date.now(),
    durationMs,
    outcome,
  }];
  globalThis.__NANOFLARE_DURATION_COLLECTOR.fetch("http://nanoflare.internal/internal/runtime/durations", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(payload),
  });
}

const __nanoflareAddEventListener = globalThis.addEventListener.bind(globalThis);
const __nanoflareScheduledListeners = [];
let __nanoflareFetchListenerCount = 0;

function __nanoflareWithOutputIdentity(callback) { return callback(); }

function __nanoflareScheduledEvent(request) {
  const url = new URL(request.url);
  const cron = url.searchParams.get("cron") || "* * * * *";
  const rawTime = Number(url.searchParams.get("time") || "");
  const scheduledTime = Number.isFinite(rawTime) && rawTime > 0 ? (rawTime < 1000000000000 ? rawTime * 1000 : rawTime) : Date.now();
  const pending = [];
  return {
    scheduledTime,
    cron,
    noRetry() {},
    waitUntil(promise) {
      pending.push(Promise.resolve(promise));
    },
    async __nanoflareRunWaitUntil() {
      await Promise.all(pending);
    },
  };
}

async function __nanoflareRunScheduledListeners(request) {
  return __nanoflareWithOutputIdentity(async () => {
    const event = __nanoflareScheduledEvent(request);
    for (const listener of __nanoflareScheduledListeners) {
      const result = listener(event);
      if (result && typeof result.then === "function") {
        event.waitUntil(result);
      }
    }
    await event.__nanoflareRunWaitUntil();
  });
}

globalThis.addEventListener = function(type, listener, options) {
  if (type === "scheduled" && typeof listener === "function") {
    __nanoflareScheduledListeners.push(listener);
    return;
  }
  if (type !== "fetch" || typeof listener !== "function") {
    return __nanoflareAddEventListener(type, listener, options);
  }
  __nanoflareFetchListenerCount++;
  return __nanoflareAddEventListener(type, (event) => {
    if (new URL(event.request.url).pathname === "/cdn-cgi/handler/scheduled") {
      event.respondWith(__nanoflareRunScheduledListeners(event.request).then(
        () => new Response(null, { status: 204 }),
        (error) => new Response(error && error.message ? error.message : "scheduled handler failed", { status: 500 }),
      ));
      return;
    }
    const startedAt = Date.now();
    const originalRespondWith = event.respondWith.bind(event);
    event.respondWith = (response) => originalRespondWith(Promise.resolve(response).then(
      (value) => {
        __nanoflareRecordRuntimeDuration(startedAt, "ok");
        return value;
      },
      (error) => {
        __nanoflareRecordRuntimeDuration(startedAt, "exception");
        throw error;
      },
    ));
    const result = __nanoflareWithOutputIdentity(() => listener(event));
    if (result && typeof result.then === "function") {
      event.waitUntil(result.catch(() => {}));
    }
    return result;
  }, options);
};

const __nanoflareObjectBindingNames = ` + objectBindingNamesJSON(objectBindings) + `;
const __nanoflareDBBindingNames = ` + dbBindingNamesJSON(dbBindings) + `;

installGlobalBindings(globalThis, __nanoflareDBBindingNames, __nanoflareObjectBindingNames);
` + "\n" + script + `

__nanoflareAddEventListener("fetch", (event) => {
  if (__nanoflareFetchListenerCount > 0 || new URL(event.request.url).pathname !== "/cdn-cgi/handler/scheduled") {
    return;
  }
  event.respondWith(__nanoflareRunScheduledListeners(event.request).then(
    () => new Response(null, { status: 204 }),
    (error) => new Response(error && error.message ? error.message : "scheduled handler failed", { status: 500 }),
  ));
});
`
}

func deploymentFormat(deployment nanoflare.Deployment) string {
	if deployment.Format != "" {
		return deployment.Format
	}
	if len(deployment.Files) == 1 {
		return "service-worker"
	}
	return "modules"
}

func deploymentServiceName(item nanoflare.ActiveDeployment) string {
	if item.Deployment.ID == "" {
		return item.App.ID
	}
	return item.App.ID + "-" + item.Deployment.ID
}

func kvServiceName(item nanoflare.ActiveDeployment, index int) string {
	return fmt.Sprintf("kv-%s-%d", deploymentServiceName(item), index)
}

func assetServiceName(item nanoflare.ActiveDeployment) string {
	return "assets-" + deploymentServiceName(item)
}

func dnsServiceName(item nanoflare.ActiveDeployment) string {
	return "dns-" + deploymentServiceName(item)
}

func dbServiceName(item nanoflare.ActiveDeployment, index int) string {
	return fmt.Sprintf("db-%s-%d", deploymentServiceName(item), index)
}

func objectServiceName(item nanoflare.ActiveDeployment, index int) string {
	return fmt.Sprintf("objects-%s-%d", deploymentServiceName(item), index)
}

func dbBindingNamesJSON(bindings []nanoflare.DatabaseBinding) string {
	names := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		names = append(names, quote(binding.Binding))
	}
	return "[" + strings.Join(names, ", ") + "]"
}

func objectBindingNamesJSON(bindings []nanoflare.ObjectStorageBucketBinding) string {
	names := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		names = append(names, quote(binding.Binding))
	}
	return "[" + strings.Join(names, ", ") + "]"
}

func assetBindingName(config nanoflare.AssetConfig) string {
	if strings.TrimSpace(config.Binding) == "" {
		return "ASSETS"
	}
	return strings.TrimSpace(config.Binding)
}

func entrypointFirst(files []nanoflare.WorkerFile, entrypoint string) []nanoflare.WorkerFile {
	result := make([]nanoflare.WorkerFile, 0, len(files))
	for _, file := range files {
		if file.Path == entrypoint {
			result = append(result, file)
			break
		}
	}
	for _, file := range files {
		if file.Path != entrypoint {
			result = append(result, file)
		}
	}
	return result
}

func Traefik(active []nanoflare.ActiveDeployment, authURL, authHost, workerHost string) string {
	var out strings.Builder
	out.WriteString("http:\n  middlewares:\n    nanoflare-auth:\n      forwardAuth:\n")
	fmt.Fprintf(&out, "        address: %s\n        authResponseHeaders:\n          - X-Nanoflare-User-JWT\n          - X-Nanoflare-User-Email\n", yamlQuote(authURL))
	out.WriteString("    nanoflare-public-url:\n      headers:\n        customRequestHeaders:\n          X-Forwarded-Proto: \"https\"\n")
	backendBase := nanoflaredGatewayBase(authURL)
	apps := uniqueActiveApps(active)
	for _, item := range apps {
		name := identifier(item.App.ID)
		fmt.Fprintf(&out, "    %s-prefix:\n      addPrefix:\n        prefix: %s\n",
			name, yamlQuote(fmt.Sprintf("/internal/http/workers/%s", item.App.ID)))
	}
	out.WriteString("  routers:\n")
	if authHost != "" {
		fmt.Fprintf(&out, "    nanoflare_auth_callback:\n      rule: %s\n      priority: 1000\n      entryPoints:\n        - web\n        - websecure\n      service: nanoflare_auth_callback\n      tls: {}\n",
			yamlQuote("Host(`"+authHost+"`) && PathPrefix(`/internal/auth/`)"))
	}
	for _, item := range apps {
		name := identifier(item.App.ID)
		fmt.Fprintf(&out, "    %s:\n      rule: %s\n      priority: 1\n      entryPoints:\n        - web\n        - websecure\n      middlewares:\n        - nanoflare-public-url\n        - %s-prefix\n      service: %s\n      tls: {}\n",
			name, yamlQuote("Host(`"+item.App.Hostname+"`)"), name, name)
		for index, route := range item.App.Auth.ProtectedRoutes {
			fmt.Fprintf(&out, "    %s-auth-%d:\n      rule: %s\n      priority: %d\n      entryPoints:\n        - web\n        - websecure\n      middlewares:\n        - nanoflare-public-url\n        - nanoflare-auth\n        - %s-prefix\n      service: %s\n      tls: {}\n",
				name, index, yamlQuote(protectedRouteRule(item.App.Hostname, route)), protectedRoutePriority(route), name, name)
		}
	}
	out.WriteString("  services:\n")
	if authHost != "" {
		fmt.Fprintf(&out, "    nanoflare_auth_callback:\n      loadBalancer:\n        servers:\n          - url: %s\n", yamlQuote(backendBase))
	}
	for _, item := range apps {
		name := identifier(item.App.ID)
		fmt.Fprintf(&out, "    %s:\n      loadBalancer:\n        passHostHeader: true\n        servers:\n          - url: %s\n",
			name, yamlQuote(backendBase))
	}
	return out.String()
}

func uniqueActiveApps(active []nanoflare.ActiveDeployment) []nanoflare.ActiveDeployment {
	seen := make(map[string]bool, len(active))
	apps := make([]nanoflare.ActiveDeployment, 0, len(active))
	for _, item := range active {
		if item.App.IsPrivate() || item.App.Hostname == "" || seen[item.App.ID] {
			continue
		}
		seen[item.App.ID] = true
		apps = append(apps, item)
	}
	return apps
}

func protectedRouteRule(hostname, route string) string {
	if route == "/" || strings.HasSuffix(route, "/*") {
		return "Host(`" + hostname + "`) && PathPrefix(`" + strings.TrimSuffix(route, "*") + "`)"
	}
	return "Host(`" + hostname + "`) && Path(`" + route + "`)"
}

func protectedRoutePriority(route string) int {
	if route == "/" || strings.HasSuffix(route, "/*") {
		return 190
	}
	return 200
}

func nanoflaredGatewayBase(authURL string) string {
	parsed, err := url.Parse(authURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return authURL
	}
	return parsed.Scheme + "://" + parsed.Host
}

func writeAtomic(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".nanoflared-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func identifier(value string) string {
	return strings.ReplaceAll(value, "-", "_")
}

func workerName(value string) string {
	var out strings.Builder
	out.WriteString("worker")
	upperNext := true
	for _, char := range value {
		if char == '-' {
			upperNext = true
			continue
		}
		if upperNext && char >= 'a' && char <= 'z' {
			char -= 'a' - 'A'
		}
		upperNext = false
		out.WriteRune(char)
	}
	return out.String()
}

func quote(value string) string {
	return strconv.Quote(value)
}

func yamlQuote(value string) string {
	return strconv.Quote(value)
}
