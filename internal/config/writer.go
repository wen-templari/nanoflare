package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type Writer struct {
	workerdPath string
	traefikPath string
	authURL     string
	authHost    string
	workerHost  string
	runtimeAddr string
}

type TraefikWriter interface {
	WriteTraefik([]nanoflare.ActiveDeployment) error
}

type RuntimeWriter struct {
	workerdPath string
	traefik     TraefikWriter
	runtimeAddr string
}

func NewWriter(workerdPath, traefikPath, authURL, workerHost string) *Writer {
	return &Writer{workerdPath: workerdPath, traefikPath: traefikPath, authURL: authURL, workerHost: workerHost, runtimeAddr: "127.0.0.1:8081"}
}

func (w *Writer) SetAuthHost(host string) {
	w.authHost = strings.TrimSpace(host)
}

func NewRuntimeWriter(workerdPath string, traefik TraefikWriter) *RuntimeWriter {
	return &RuntimeWriter{workerdPath: workerdPath, traefik: traefik, runtimeAddr: "127.0.0.1:8081"}
}

func (w *Writer) SetNanoflareRuntimeAddr(addr string) {
	w.runtimeAddr = addr
}

func (w *RuntimeWriter) SetNanoflareRuntimeAddr(addr string) {
	w.runtimeAddr = addr
}

func (w *Writer) Write(active []nanoflare.ActiveDeployment) error {
	if err := w.WriteWorkerd(w.workerdPath, active); err != nil {
		return err
	}
	return w.WriteTraefik(active)
}

func (w *Writer) WriteWorkerd(path string, active []nanoflare.ActiveDeployment) error {
	return writeAtomic(path, []byte(WorkerdWithRuntimeAddr(active, w.runtimeAddr)))
}

func (w *Writer) WriteTraefik(active []nanoflare.ActiveDeployment) error {
	return writeAtomic(w.traefikPath, []byte(Traefik(active, w.authURL, w.authHost, w.workerHost)))
}

func (w *RuntimeWriter) WriteWorkerd(path string, active []nanoflare.ActiveDeployment) error {
	return writeAtomic(path, []byte(WorkerdWithRuntimeAddr(active, w.runtimeAddr)))
}

func (w *RuntimeWriter) WriteTraefik(active []nanoflare.ActiveDeployment) error {
	return w.traefik.WriteTraefik(active)
}

func Workerd(active []nanoflare.ActiveDeployment) string {
	return WorkerdWithRuntimeAddr(active, "127.0.0.1:8081")
}

func WorkerdWithRuntimeAddr(active []nanoflare.ActiveDeployment, runtimeAddr string) string {
	var out strings.Builder
	out.WriteString("using Workerd = import \"/workerd/workerd.capnp\";\n\n")
	out.WriteString("const config :Workerd.Config = (\n  services = [\n")
	out.WriteString(durationTelemetryServices(runtimeAddr))
	for _, item := range active {
		fmt.Fprintf(&out, "    (name = %s, worker = .%s),\n", quote(item.App.ID), workerName(item.App.ID))
	}
	for _, item := range active {
		for index, binding := range item.Deployment.KVNamespaces {
			fmt.Fprintf(&out, "    (name = %s, external = (address = %s, http = (injectRequestHeaders = [(name = \"Authorization\", value = %s), (name = \"X-Nanoflare-KV-Namespace-ID\", value = %s)]))),\n",
				quote(kvServiceName(item.App.ID, index)), quote(runtimeAddr), quote("Bearer "+item.App.RuntimeToken), quote(binding.ID))
		}
		fmt.Fprintf(&out, "    (name = %s, external = (address = %s, http = (injectRequestHeaders = [(name = \"Authorization\", value = %s), (name = \"X-Nanoflare-Binding\", value = \"assets\")]))),\n",
			quote(assetServiceName(item.App.ID)), quote(runtimeAddr), quote("Bearer "+item.App.RuntimeToken))
		for index, binding := range item.Deployment.ObjectStorageBuckets {
			fmt.Fprintf(&out, "    (name = %s, external = (address = %s, http = (injectRequestHeaders = [(name = \"Authorization\", value = %s), (name = \"X-Nanoflare-Object-Bucket-ID\", value = %s)]))),\n",
				quote(objectServiceName(item.App.ID, index)), quote(runtimeAddr), quote("Bearer "+item.App.RuntimeToken), quote(binding.BucketID))
		}
	}
	out.WriteString("  ],\n\n  sockets = [\n")
	for _, item := range active {
		fmt.Fprintf(&out, "    (name = %s, address = %s, http = (), service = %s),\n",
			quote(item.App.ID), quote(fmt.Sprintf("*:%d", item.Deployment.Port)), quote(item.App.ID))
	}
	out.WriteString("  ]\n);\n")
	for _, item := range active {
		fmt.Fprintf(&out, "\nconst %s :Workerd.Worker = (\n", workerName(item.App.ID))
		writeWorkerSource(&out, item.Deployment)
		fmt.Fprintf(&out, "  bindings = [%s],\n",
			strings.Join(workerBindings(item), ", "))
		fmt.Fprintf(&out, "  compatibilityDate = %s,\n", quote(item.Deployment.CompatibilityDate))
		out.WriteString(");\n")
	}
	return out.String()
}

func durationTelemetryServices(runtimeAddr string) string {
	return fmt.Sprintf("    (name = \"nanoflare-duration-collector\", external = (address = %s)),\n", quote(runtimeAddr))
}

func workerBindings(item nanoflare.ActiveDeployment) []string {
	bindings := make([]string, 0, len(item.Deployment.Vars)+len(item.App.SecretValues)+len(item.Deployment.KVNamespaces)+len(item.Deployment.ObjectStorageBuckets)+3)
	bindings = append(bindings,
		fmt.Sprintf("(name = \"__NANOFLARE_APP_ID\", text = %s)", quote(item.App.ID)),
		`(name = "__NANOFLARE_DURATION_COLLECTOR", service = "nanoflare-duration-collector")`,
	)
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
		bindings = append(bindings, fmt.Sprintf("(name = %s, kvNamespace = %s)", quote(binding.Binding), quote(kvServiceName(item.App.ID, index))))
	}
	bindings = append(bindings, fmt.Sprintf("(name = %s, service = %s)", quote(assetBindingName(item.Deployment.AssetConfig)), quote(assetServiceName(item.App.ID))))
	for index, binding := range item.Deployment.ObjectStorageBuckets {
		bindings = append(bindings, fmt.Sprintf("(name = %s, service = %s)", quote(binding.Binding), quote(objectServiceName(item.App.ID, index))))
	}
	return bindings
}

func writeWorkerSource(out *strings.Builder, deployment nanoflare.Deployment) {
	if deploymentFormat(deployment) == "service-worker" {
		fmt.Fprintf(out, "  serviceWorkerScript = %s,\n", quote(serviceWorkerWrapper(deployment.Files[0].Content, deployment.ObjectStorageBuckets)))
		return
	}
	out.WriteString("  modules = [\n")
	fmt.Fprintf(out, "    (name = %s, esModule = %s),\n", quote("__nanoflare_internal_entrypoint__.js"), quote(entrypointWrapper(deployment.Entrypoint, assetBindingName(deployment.AssetConfig), deployment.ObjectStorageBuckets)))
	for _, file := range entrypointFirst(deployment.Files, deployment.Entrypoint) {
		fmt.Fprintf(out, "    (name = %s, esModule = %s),\n", quote(file.Path), quote(file.Content))
	}
	out.WriteString("  ],\n")
}

func entrypointWrapper(entrypoint, binding string, objectBindings []nanoflare.ObjectStorageBucketBinding) string {
	return fmt.Sprintf(`import userWorker from %s;

const assetBindingName = %s;
const objectBindingNames = %s;

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

function wrapAssetBinding(binding) {
  if (!binding) return binding;
  return {
    fetch(input, init) {
      if (typeof input === "string" && input.startsWith("/")) {
        return binding.fetch(new Request("https://assets.local" + input, init));
      }
      return binding.fetch(input, init);
    },
  };
}

function buildObjectBody(response, object) {
  if (!response) return null;
  return {
    key: object.key,
    size: object.size,
    etag: object.etag,
    httpEtag: object.httpEtag,
    uploaded: new Date(object.uploaded),
    httpMetadata: object.httpMetadata ?? {},
    body: response.body,
    get bodyUsed() {
      return response.bodyUsed;
    },
    arrayBuffer() {
      return response.arrayBuffer();
    },
    text() {
      return response.text();
    },
    json() {
      return response.json();
    },
    blob() {
      return response.blob();
    },
  };
}

async function objectBindingError(bindingName, operation, response) {
  const detail = (await response.text()).trim();
  return new Error(detail ? bindingName + "." + operation + " failed: " + response.status + " - " + detail : bindingName + "." + operation + " failed: " + response.status);
}

async function toObjectRequestInit(value, options) {
  if (value instanceof Request || value instanceof Response) {
    const contentLength = value.headers.get("content-length");
    return {
      body: value.body,
      contentType: value.headers.get("content-type") || options?.httpMetadata?.contentType || "",
      size: contentLength ? Number(contentLength) : undefined,
    };
  }
  if (typeof value === "string") {
    return {
      body: value,
      contentType: options?.httpMetadata?.contentType || "",
      size: new TextEncoder().encode(value).byteLength,
    };
  }
  if (value instanceof ArrayBuffer) {
    return {
      body: value,
      contentType: options?.httpMetadata?.contentType || "",
      size: value.byteLength,
    };
  }
  if (ArrayBuffer.isView(value)) {
    return {
      body: value,
      contentType: options?.httpMetadata?.contentType || "",
      size: value.byteLength,
    };
  }
  if (value instanceof Blob) {
    return {
      body: value,
      contentType: options?.httpMetadata?.contentType || value.type || "",
      size: value.size,
    };
  }
  return {
    body: value,
    contentType: options?.httpMetadata?.contentType || (value instanceof Blob ? value.type : ""),
    size: undefined,
  };
}

function wrapObjectsBinding(bindingName, binding) {
  if (!binding) return binding;
  return {
    async put(key, value, options) {
      const init = await toObjectRequestInit(value, options);
      const response = await binding.fetch(new Request("https://objects.local/internal/runtime/objects/" + encodeURIComponent(key), {
        method: "PUT",
        headers: init.contentType ? { "content-type": init.contentType } : undefined,
        body: init.body,
      }));
      if (!response.ok) throw await objectBindingError(bindingName, "put", response);
      const raw = await response.text();
      if (!raw.trim()) {
        return {
          key,
          size: init.size ?? 0,
          etag: "",
          httpEtag: "",
          uploaded: new Date(),
          httpMetadata: {
            contentType: init.contentType || "",
          },
        };
      }
      const object = JSON.parse(raw);
      object.uploaded = new Date(object.uploaded);
      return object;
    },
    async get(key) {
      const response = await binding.fetch(new Request("https://objects.local/internal/runtime/objects/" + encodeURIComponent(key)));
      if (response.status === 404) return null;
      if (!response.ok) throw await objectBindingError(bindingName, "get", response);
      const object = {
        key: response.headers.get("x-nanoflare-object-key") || key,
        size: Number(response.headers.get("content-length") || "0"),
        etag: response.headers.get("x-nanoflare-object-etag") || "",
        httpEtag: response.headers.get("etag") || "",
        uploaded: response.headers.get("x-nanoflare-object-uploaded") || new Date(0).toISOString(),
        httpMetadata: {
          contentType: response.headers.get("content-type") || "",
        },
      };
      return buildObjectBody(response, object);
    },
    async head(key) {
      const response = await binding.fetch(new Request("https://objects.local/internal/runtime/objects/" + encodeURIComponent(key), {
        method: "HEAD",
      }));
      if (response.status === 404) return null;
      if (!response.ok) throw await objectBindingError(bindingName, "head", response);
      return {
        key: response.headers.get("x-nanoflare-object-key") || key,
        size: Number(response.headers.get("content-length") || "0"),
        etag: response.headers.get("x-nanoflare-object-etag") || "",
        httpEtag: response.headers.get("etag") || "",
        uploaded: new Date(response.headers.get("x-nanoflare-object-uploaded") || new Date(0).toISOString()),
        httpMetadata: {
          contentType: response.headers.get("content-type") || "",
        },
      };
    },
    async delete(key) {
      const response = await binding.fetch(new Request("https://objects.local/internal/runtime/objects/" + encodeURIComponent(key), {
        method: "DELETE",
      }));
      if (!response.ok) throw await objectBindingError(bindingName, "delete", response);
    },
  };
}

function wrapEnv(env) {
  if (!env) return env;
  const wrapped = Object.create(env);
  if (env[assetBindingName]) {
    Object.defineProperty(wrapped, assetBindingName, {
      value: wrapAssetBinding(env[assetBindingName]),
      enumerable: true,
    });
  }
  for (const name of objectBindingNames) {
    if (!env[name]) continue;
    Object.defineProperty(wrapped, name, {
      value: wrapObjectsBinding(name, env[name]),
      enumerable: true,
    });
  }
  return wrapped;
}

export default {
  ...userWorker,
  async fetch(request, env, ctx) {
    const startedAt = Date.now();
    try {
      const response = await userWorker.fetch(request, wrapEnv(env), ctx);
      recordRuntimeDuration(env, ctx, startedAt, "ok");
      return response;
    } catch (error) {
      recordRuntimeDuration(env, ctx, startedAt, "exception");
      throw error;
    }
  },
};`, quote("./"+strings.TrimPrefix(entrypoint, "./")), quote(binding), objectBindingNamesJSON(objectBindings))
}

func serviceWorkerWrapper(script string, objectBindings []nanoflare.ObjectStorageBucketBinding) string {
	return `function __nanoflareRecordRuntimeDuration(startedAt, outcome) {
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
globalThis.addEventListener = function(type, listener, options) {
  if (type !== "fetch" || typeof listener !== "function") {
    return __nanoflareAddEventListener(type, listener, options);
  }
  return __nanoflareAddEventListener(type, (event) => {
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
    const result = listener(event);
    if (result && typeof result.then === "function") {
      event.waitUntil(result.catch(() => {}));
    }
    return result;
  }, options);
};

function __nanoflareBuildObjectBody(response, object) {
  if (!response) return null;
  return {
    key: object.key,
    size: object.size,
    etag: object.etag,
    httpEtag: object.httpEtag,
    uploaded: new Date(object.uploaded),
    httpMetadata: object.httpMetadata ?? {},
    body: response.body,
    get bodyUsed() {
      return response.bodyUsed;
    },
    arrayBuffer() {
      return response.arrayBuffer();
    },
    text() {
      return response.text();
    },
    json() {
      return response.json();
    },
    blob() {
      return response.blob();
    },
  };
}

async function __nanoflareObjectBindingError(bindingName, operation, response) {
  const detail = (await response.text()).trim();
  return new Error(detail ? bindingName + "." + operation + " failed: " + response.status + " - " + detail : bindingName + "." + operation + " failed: " + response.status);
}

async function __nanoflareToObjectRequestInit(value, options) {
  if (value instanceof Request || value instanceof Response) {
    const contentLength = value.headers.get("content-length");
    return {
      body: value.body,
      contentType: value.headers.get("content-type") || options?.httpMetadata?.contentType || "",
      size: contentLength ? Number(contentLength) : undefined,
    };
  }
  if (typeof value === "string") {
    return {
      body: value,
      contentType: options?.httpMetadata?.contentType || "",
      size: new TextEncoder().encode(value).byteLength,
    };
  }
  if (value instanceof ArrayBuffer) {
    return {
      body: value,
      contentType: options?.httpMetadata?.contentType || "",
      size: value.byteLength,
    };
  }
  if (ArrayBuffer.isView(value)) {
    return {
      body: value,
      contentType: options?.httpMetadata?.contentType || "",
      size: value.byteLength,
    };
  }
  if (value instanceof Blob) {
    return {
      body: value,
      contentType: options?.httpMetadata?.contentType || value.type || "",
      size: value.size,
    };
  }
  return {
    body: value,
    contentType: options?.httpMetadata?.contentType || (value instanceof Blob ? value.type : ""),
    size: undefined,
  };
}

const __nanoflareObjectBindingNames = ` + objectBindingNamesJSON(objectBindings) + `;

function __nanoflareWrapObjectsBinding(bindingName, binding) {
  if (!binding) return binding;
  return {
    async put(key, value, options) {
      const init = await __nanoflareToObjectRequestInit(value, options);
      const response = await binding.fetch(new Request("https://objects.local/internal/runtime/objects/" + encodeURIComponent(key), {
        method: "PUT",
        headers: init.contentType ? { "content-type": init.contentType } : undefined,
        body: init.body,
      }));
      if (!response.ok) throw await __nanoflareObjectBindingError(bindingName, "put", response);
      const raw = await response.text();
      if (!raw.trim()) {
        return {
          key,
          size: init.size ?? 0,
          etag: "",
          httpEtag: "",
          uploaded: new Date(),
          httpMetadata: {
            contentType: init.contentType || "",
          },
        };
      }
      const object = JSON.parse(raw);
      object.uploaded = new Date(object.uploaded);
      return object;
    },
    async get(key) {
      const response = await binding.fetch(new Request("https://objects.local/internal/runtime/objects/" + encodeURIComponent(key)));
      if (response.status === 404) return null;
      if (!response.ok) throw await __nanoflareObjectBindingError(bindingName, "get", response);
      return __nanoflareBuildObjectBody(response, {
        key: response.headers.get("x-nanoflare-object-key") || key,
        size: Number(response.headers.get("content-length") || "0"),
        etag: response.headers.get("x-nanoflare-object-etag") || "",
        httpEtag: response.headers.get("etag") || "",
        uploaded: response.headers.get("x-nanoflare-object-uploaded") || new Date(0).toISOString(),
        httpMetadata: {
          contentType: response.headers.get("content-type") || "",
        },
      });
    },
    async head(key) {
      const response = await binding.fetch(new Request("https://objects.local/internal/runtime/objects/" + encodeURIComponent(key), {
        method: "HEAD",
      }));
      if (response.status === 404) return null;
      if (!response.ok) throw await __nanoflareObjectBindingError(bindingName, "head", response);
      return {
        key: response.headers.get("x-nanoflare-object-key") || key,
        size: Number(response.headers.get("content-length") || "0"),
        etag: response.headers.get("x-nanoflare-object-etag") || "",
        httpEtag: response.headers.get("etag") || "",
        uploaded: new Date(response.headers.get("x-nanoflare-object-uploaded") || new Date(0).toISOString()),
        httpMetadata: {
          contentType: response.headers.get("content-type") || "",
        },
      };
    },
    async delete(key) {
      const response = await binding.fetch(new Request("https://objects.local/internal/runtime/objects/" + encodeURIComponent(key), {
        method: "DELETE",
      }));
      if (!response.ok) throw await __nanoflareObjectBindingError(bindingName, "delete", response);
    },
  };
}

for (const __nanoflareName of __nanoflareObjectBindingNames) {
  globalThis[__nanoflareName] = __nanoflareWrapObjectsBinding(__nanoflareName, globalThis[__nanoflareName]);
}
` + "\n" + script
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

func kvServiceName(appID string, index int) string {
	return fmt.Sprintf("kv-%s-%d", appID, index)
}

func assetServiceName(appID string) string {
	return "assets-" + appID
}

func objectServiceName(appID string, index int) string {
	return fmt.Sprintf("objects-%s-%d", appID, index)
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
	backendBase := nanoflaredGatewayBase(authURL)
	for _, item := range active {
		name := identifier(item.App.ID)
		fmt.Fprintf(&out, "    %s-prefix:\n      addPrefix:\n        prefix: %s\n",
			name, yamlQuote(fmt.Sprintf("/internal/http/apps/%s", item.App.ID)))
	}
	out.WriteString("  routers:\n")
	if authHost != "" {
		fmt.Fprintf(&out, "    nanoflare_auth_callback:\n      rule: %s\n      priority: 1000\n      entryPoints:\n        - web\n        - websecure\n      service: nanoflare_auth_callback\n      tls: {}\n",
			yamlQuote("Host(`"+authHost+"`) && PathPrefix(`/internal/auth/`)"))
	}
	for _, item := range active {
		name := identifier(item.App.ID)
		fmt.Fprintf(&out, "    %s:\n      rule: %s\n      priority: 1\n      entryPoints:\n        - web\n        - websecure\n      middlewares:\n        - %s-prefix\n      service: %s\n      tls: {}\n",
			name, yamlQuote("Host(`"+item.App.Hostname+"`)"), name, name)
		for index, route := range item.App.Auth.ProtectedRoutes {
			fmt.Fprintf(&out, "    %s-auth-%d:\n      rule: %s\n      priority: %d\n      entryPoints:\n        - web\n        - websecure\n      middlewares:\n        - nanoflare-auth\n        - %s-prefix\n      service: %s\n      tls: {}\n",
				name, index, yamlQuote(protectedRouteRule(item.App.Hostname, route)), protectedRoutePriority(route), name, name)
		}
	}
	out.WriteString("  services:\n")
	if authHost != "" {
		fmt.Fprintf(&out, "    nanoflare_auth_callback:\n      loadBalancer:\n        servers:\n          - url: %s\n", yamlQuote(backendBase))
	}
	for _, item := range active {
		name := identifier(item.App.ID)
		fmt.Fprintf(&out, "    %s:\n      loadBalancer:\n        servers:\n          - url: %s\n",
			name, yamlQuote(backendBase))
	}
	return out.String()
}

func protectedRouteRule(hostname, route string) string {
	if strings.HasSuffix(route, "/*") {
		return "Host(`" + hostname + "`) && PathPrefix(`" + strings.TrimSuffix(route, "*") + "`)"
	}
	return "Host(`" + hostname + "`) && Path(`" + route + "`)"
}

func protectedRoutePriority(route string) int {
	if strings.HasSuffix(route, "/*") {
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
