package config

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/clas/nanoflare/internal/nanoflare"
)

const loaderInvocationContextModule = "nanoflare-internal:invocation-context"

// InstrumentLoaderInvocation adds request-scoped resource capabilities and
// completion accounting. The input snapshot is not mutated. The parent must
// admit an invocation before supplying its private props to getEntrypoint().
func InstrumentLoaderInvocation(code WorkerCodeSnapshot, active nanoflare.ActiveDeployment) WorkerCodeSnapshot {
	code.Modules = maps.Clone(code.Modules)
	code.moduleOrder = slices.Clone(code.moduleOrder)
	declarations := struct {
		KV       []string `json:"kv"`
		Fetch    []string `json:"fetch"`
		Services []string `json:"services"`
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
	if len(active.Deployment.Assets) > 0 {
		declarations.Fetch = append(declarations.Fetch, assetBindingName(active.Deployment.AssetConfig))
	}
	for _, binding := range active.Deployment.Services {
		declarations.Services = append(declarations.Services, binding.Binding)
	}
	declarationJSON, _ := json.Marshal(declarations)
	const main = "__nanoflare_invocation_entrypoint__.js"
	if existing, exists := code.Modules[loaderInvocationContextModule]; !exists || existing.JS != loaderInvocationContextSource {
		code.Modules[loaderInvocationContextModule] = WorkerCodeModule{JS: loaderInvocationContextSource}
		code.moduleOrder = append(code.moduleOrder, loaderInvocationContextModule)
	}
	code.Modules[main] = WorkerCodeModule{JS: `import UserWorker from "./` + code.MainModule + `";
import { makeInvocation, runInvocation } from "` + loaderInvocationContextModule + `";
import { wrapEnv as wrapRuntimeEnv } from "nanoflare-internal:runtime-bindings";
const declarations = ` + string(declarationJSON) + `;
const states = new WeakMap();
let WrappedWorker;
if (typeof UserWorker === "function") {
  WrappedWorker = class extends UserWorker {
    constructor(ctx, env) {
      const state = makeInvocation(env, ctx, declarations);
      state.env = wrapRuntimeEnv(state.env, ` + quote(assetBindingName(active.Deployment.AssetConfig)) + `, ` + dbBindingNamesJSON(active.Deployment.Databases) + `, ` + objectBindingNamesJSON(active.Deployment.ObjectStorageBuckets) + `);
      super(state.ctx, state.env);
      states.set(this, state);
    }
  };
  const seen = new Set(["constructor", "env", "ctx"]);
  for (let prototype = UserWorker.prototype; prototype && prototype !== Object.prototype; prototype = Object.getPrototypeOf(prototype)) {
    for (const name of Object.getOwnPropertyNames(prototype)) {
      if (seen.has(name)) continue;
      seen.add(name);
      const descriptor = Object.getOwnPropertyDescriptor(prototype, name);
      if (typeof descriptor.value !== "function") continue;
      Object.defineProperty(WrappedWorker.prototype, name, {
        configurable: true,
        value: function(...args) {
          return runInvocation(states.get(this), () => descriptor.value.apply(this, args));
        },
      });
    }
  }
} else {
  WrappedWorker = {};
  for (const [name, method] of Object.entries(UserWorker)) {
    if (typeof method !== "function") { WrappedWorker[name] = method; continue; }
    WrappedWorker[name] = function(first, env, ctx, ...rest) {
      const state = makeInvocation(env, ctx, declarations);
      return runInvocation(state, () => method.call(UserWorker, first, state.env, state.ctx, ...rest));
    };
  }
}
export default WrappedWorker;
`}
	code.moduleOrder = append(code.moduleOrder, main)
	code.MainModule = main
	return code
}

const loaderInvocationContextSource = `import { AsyncLocalStorage } from "node:async_hooks";
import { env as nativeEnv, RpcTarget } from "cloudflare:workers";
const storage = new AsyncLocalStorage();
export function currentInvocationEnv() { return storage.getStore()?.env ?? nativeEnv; }

function returnedValue(state, value, seen = new WeakMap()) {
  if (!value || typeof value !== "object") return value;
  if (value instanceof RpcTarget) {
    if (state.capabilities.has(value)) return state.capabilities.get(value);
    // This facade forwards ordinary public prototype methods. Instance fields,
    // accessor properties and custom Symbol.dispose hooks are not forwarded.
    // Native disposal releases the facade capability, not a user disposal hook.
    class ScopedCapability extends RpcTarget {}
    const methods = new Set(["constructor", "then", "__proto__", "prototype"]);
    for (let prototype = Object.getPrototypeOf(value); prototype && prototype !== RpcTarget.prototype; prototype = Object.getPrototypeOf(prototype)) {
      for (const name of Object.getOwnPropertyNames(prototype)) {
        if (methods.has(name) || name.startsWith("_")) continue;
        methods.add(name);
        const descriptor = Object.getOwnPropertyDescriptor(prototype, name);
        if (typeof descriptor.value !== "function") continue;
        Object.defineProperty(ScopedCapability.prototype, name, {
          value: function(...args) { return runInvocation(state, () => Reflect.apply(descriptor.value, value, args)); },
        });
      }
    }
    const capability = new ScopedCapability();
    state.capabilities.set(value, capability);
    return capability;
  }
  if (seen.has(value)) return seen.get(value);
  if (Array.isArray(value)) {
    const output = []; seen.set(value, output);
    for (const item of value) output.push(returnedValue(state, item, seen));
    return output;
  }
  if (Object.getPrototypeOf(value) === Object.prototype || Object.getPrototypeOf(value) === null) {
    const output = {}; seen.set(value, output);
    for (const [name, item] of Object.entries(value)) Object.defineProperty(output, name, { enumerable:true, configurable:true, writable:true, value:returnedValue(state, item, seen) });
    return output;
  }
  return value;
}

function resourceBinding(broker, token, name) {
  return { fetch(input, init) {
    const request = new Request(typeof input === "string" && input.startsWith("/") ? "https://assets.local" + input : input, init);
    request.headers.set("X-Nanoflare-Invocation", token);
    request.headers.set("X-Nanoflare-Binding-Name", name);
    return broker.fetch(request);
  } };
}
function namespaceBinding(fetcher) {
  async function checked(response) {
    if (!response.ok) throw new Error("KV operation failed: " + response.status + " " + await response.text());
    return response;
  }
  function keyURL(key) {
    if (typeof key !== "string" || key.length === 0 || key === "." || key === "..") throw new TypeError("KV key must be a non-empty string other than . or ..");
    return new URL("https://kv.local/" + encodeURIComponent(key) + "?urlencoded=true");
  }
  return {
    async get(key, options) {
      const type = typeof options === "string" ? options : options?.type ?? "text";
      if (options && typeof options === "object" && Object.keys(options).some(key => key !== "type")) throw new TypeError("KV cache and metadata options are not supported");
      if (!["text", "json", "arrayBuffer", "stream"].includes(type)) throw new TypeError("Unsupported KV value type");
      const response = await fetcher.fetch(keyURL(key));
      if (response.status === 404) return null;
      await checked(response);
      return type === "stream" ? response.body : response[type]();
    },
    async put(key, value, options = {}) {
      if (Object.keys(options).some(key => !["expiration", "expirationTtl"].includes(key))) throw new TypeError("KV metadata options are not supported");
      const url = keyURL(key);
      if (options.expiration !== undefined) url.searchParams.set("expiration", String(options.expiration));
      if (options.expirationTtl !== undefined) url.searchParams.set("expiration_ttl", String(options.expirationTtl));
      await checked(await fetcher.fetch(url, { method: "PUT", body: value }));
    },
    async delete(key) { await checked(await fetcher.fetch(keyURL(key), { method: "DELETE" })); },
    async list(options = {}) {
      if (Object.keys(options).some(key => !["prefix", "limit", "cursor"].includes(key))) throw new TypeError("Unsupported KV list options");
      const url = new URL("https://kv.local/");
      if (options.prefix !== undefined) url.searchParams.set("prefix", options.prefix);
      if (options.limit !== undefined) url.searchParams.set("key_count_limit", String(options.limit));
      if (options.cursor !== undefined) url.searchParams.set("cursor", options.cursor);
      return (await checked(await fetcher.fetch(url))).json();
    },
  };
}

export function makeInvocation(rawEnv, rawCtx, declarations) {
  const metadata = rawCtx.props?.__nanoflare_invocation;
  if (!metadata || typeof metadata.token !== "string" || !metadata.token || typeof metadata.key !== "string" || !metadata.key) throw new Error("Missing admitted invocation metadata");
  const token = metadata.token;
  const state = { env: {}, pending: 0, running: 0, mainDone: false, completing: false, epoch: 0, capabilities: new WeakMap() };
  for (const [name, value] of Object.entries(rawEnv)) {
    if (!["__NANOFLARE_RESOURCE_BROKER", "__NANOFLARE_LIFECYCLE", "__NANOFLARE_SERVICE_BROKER"].includes(name)) state.env[name] = value;
  }
  for (const name of declarations.fetch) state.env[name] = resourceBinding(rawEnv.__NANOFLARE_RESOURCE_BROKER, token, name);
  for (const name of declarations.kv) state.env[name] = namespaceBinding(resourceBinding(rawEnv.__NANOFLARE_RESOURCE_BROKER, token, name));
  for (const name of declarations.services) {
    state.env[name] = new Proxy({}, { get(_, method) {
      if (method === "then") return undefined;
      if (method === "fetch") return (input, init) => rawEnv.__NANOFLARE_SERVICE_BROKER.invoke(token, name, "fetch", [new Request(input, init)]);
      if (typeof method !== "string") return undefined;
      return (...args) => rawEnv.__NANOFLARE_SERVICE_BROKER.invoke(token, name, method, args);
    } });
  }
  let finish, fail;
  async function lifecycle(operation, epoch) {
    let attempt = 0;
    for (;;) {
      try {
        const response = await rawEnv.__NANOFLARE_LIFECYCLE.fetch("http://nanoflare.lifecycle/" + operation, {
          method: "POST", headers: { "X-Nanoflare-Invocation": token, "X-Nanoflare-Invocation-Epoch": String(epoch) },
        });
        await response.body?.cancel();
        if (response.ok) return;
        if (response.status < 500) {
          const error = new Error("Invocation " + operation + " rejected: " + response.status);
          error.definitive = true;
          throw error;
        }
      } catch (error) { if (error.definitive) throw error; }
      await new Promise(resolve => setTimeout(resolve, Math.min(1000, [20, 100, 500][attempt++] ?? 1000)));
    }
  }
  function beginEpoch() {
    state.epoch++;
    state.started = false;
    state.mainDone = false;
    state.completing = false;
    const completion = new Promise((resolve, reject) => { finish = resolve; fail = reject; });
    rawCtx.waitUntil(completion);
    state.startPromise = lifecycle("start", state.epoch).then(() => { state.started = true; }).catch(error => { finish(); throw error; });
    return state.startPromise;
  }
  state.start = () => {
    if (state.completing) {
      if (!state.transitionPromise) {
        state.transitionPromise = state.completionAck.then(beginEpoch).finally(() => { state.transitionPromise = undefined; });
      }
      return state.transitionPromise;
    }
    return state.startPromise ?? beginEpoch();
  };
  state.maybeComplete = () => {
    if (!state.started || !state.mainDone || state.running || state.pending || state.completing || state.completionQueued) return;
    state.completionQueued = true;
    queueMicrotask(() => {
    state.completionQueued = false;
    if (!state.mainDone || state.running || state.pending || state.completing) return;
    state.completing = true;
    state.completionAck = lifecycle("complete", state.epoch);
    state.completionAck.then(finish, fail);
    });
  };
  state.ctx = {
    props: Object.fromEntries(Object.entries(rawCtx.props ?? {}).filter(([name]) => name !== "__nanoflare_invocation")),
    exports: rawCtx.exports,
    waitUntil(value) {
      state.pending++;
      const tracked = Promise.resolve(value).then(
        result => { state.pending--; state.maybeComplete(); return result; },
        error => { state.pending--; state.maybeComplete(); throw error; },
      );
      rawCtx.waitUntil(tracked);
    },
    passThroughOnException() { rawCtx.passThroughOnException(); },
  };
  return state;
}

export function runInvocation(state, callback) {
  state.running++;
  const done = () => { state.running--; state.mainDone = true; state.maybeComplete(); };
  const invoke = () => storage.run(state, () => {
    try {
      const result = callback();
      if (result && typeof result.then === "function") return Promise.resolve(result).then(value => { try { return returnedValue(state,value); } finally { done(); } }, error => { done(); throw error; });
      const wrapped = returnedValue(state,result);
      done();
      return wrapped;
    } catch (error) { done(); throw error; }
  });
  // Once admitted, ordinary synchronous calls between methods retain their
  // synchronous behavior. Only the first runtime entry waits for the handshake.
  return state.started && !state.completing ? invoke() : state.start().then(invoke, error => { done(); throw error; });
}
`
