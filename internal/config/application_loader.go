package config

const applicationLoaderCode = `
const childCodeLimit = 512 * 1024;
const freshChildCodeLimit = 8 * 1024 * 1024;

function isLoaderRecord(value) {
  if (value === null || typeof value !== "object") return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function normalizeChildCode(descriptor, decode, publishedWasm, owner, limit = childCodeLimit) {
  if (!isLoaderRecord(descriptor)) {
    throw new TypeError("Worker Loader requires a WorkerCode object");
  }
  const fields = new Set(["compatibilityDate", "compatibilityFlags", "mainModule", "modules", "env", "globalOutbound", "tails", "limits"]);
  for (const field of Object.keys(descriptor)) {
    if (!fields.has(field)) throw new TypeError("Unsupported WorkerCode field: " + field);
  }
  const {globalOutbound, tails, limits} = descriptor;
  function binding(value, method) {
    if (!decode) return encodeApplicationBinding(value, owner, method);
    if (!isLoaderRecord(value) || typeof value.name !== "string" || !isLoaderRecord(value.props)) {
      throw new TypeError("Invalid Worker Loader " + method + " entrypoint reference");
    }
    return value;
  }
  if (tails !== undefined && (!Array.isArray(tails) || tails.length > 8)) {
    throw new TypeError("Worker Loader tails must be an array of up to eight entrypoints");
  }
  if (limits !== undefined && (!isLoaderRecord(limits) || Object.entries(limits).some(([name, value]) =>
    !["cpuMs", "subRequests"].includes(name) ||
    (value !== undefined && (!Number.isSafeInteger(value) || value < 0 || value > 0xffffffff))))) {
    throw new TypeError("Invalid Worker Loader limits");
  }
  const {compatibilityDate, mainModule, modules, env} = descriptor;
  if (typeof compatibilityDate !== "string" || typeof mainModule !== "string" ||
      !isLoaderRecord(modules) || !Object.hasOwn(modules, mainModule)) {
    throw new TypeError("WorkerCode requires compatibilityDate, mainModule and modules");
  }
  let wasmBytes = 0;
  const normalizedModules = Object.fromEntries(Object.entries(modules).map(([name, module]) => {
    if (typeof module === "string") return [name, module];
    if (!isLoaderRecord(module)) throw new TypeError("Invalid Worker module");
    const fields = Object.keys(module);
    if (fields.length === 1 && fields[0] === "wasm") {
      const wasm = module.wasm;
      if (decode && typeof wasm === "string") return [name, {wasm: Uint8Array.fromBase64(wasm).buffer}];
      if (decode && isLoaderRecord(wasm) && Object.keys(wasm).length === 1 && typeof wasm.published === "string") {
        return [name, {wasm}];
      }
      if (!decode && wasm instanceof WebAssembly.Module) {
        const published = publishedWasm.get(wasm);
        if (published === undefined) throw new TypeError("Worker Loader compiled Wasm must be imported from the parent revision");
        return [name, {wasm: {published}}];
      }
      if (!decode && (wasm instanceof ArrayBuffer || ArrayBuffer.isView(wasm))) {
        const bytes = wasm instanceof ArrayBuffer ? new Uint8Array(wasm) : new Uint8Array(wasm.buffer, wasm.byteOffset, wasm.byteLength);
        wasmBytes += bytes.byteLength;
        if (wasmBytes > limit) throw new RangeError("WorkerCode exceeds " + (limit >> 10) + " KiB");
        return [name, {wasm: bytes.toBase64()}];
      }
      throw new TypeError("Worker Loader Wasm modules require bytes");
    }
    if (fields.length !== 1 || !["js", "cjs", "text", "json"].includes(fields[0]) ||
        (fields[0] !== "json" && typeof module[fields[0]] !== "string")) {
      throw new TypeError("Unsupported Worker module type");
    }
    return [name, module];
  }));
  if (env !== undefined && (!env || typeof env !== "object" || Array.isArray(env))) {
    throw new TypeError("WorkerCode.env must be an object");
  }
  return {...descriptor, modules: normalizedModules,
    globalOutbound: globalOutbound == null ? null : binding(globalOutbound, "fetch"),
    tails: tails?.map(value => binding(value, "tail")),
  };
}

function encodeLoaderJSON(input, limit, message) {
  const encoded = JSON.stringify(input, (_, value) => {
    if (value !== null && typeof value === "object" && !Array.isArray(value) && !isLoaderRecord(value)) {
      throw new TypeError("WorkerCode supports JSON values only");
    }
    if (typeof value === "function" || typeof value === "symbol" ||
        (typeof value === "number" && !Number.isFinite(value))) {
      throw new TypeError("Worker Loader supports JSON values only");
    }
    return value;
  });
  if (new TextEncoder().encode(encoded).byteLength > limit) throw new RangeError(message);
  return encoded;
}

function encodeEntrypointOptions(options) {
  if (options === undefined) return undefined;
  if (!options || typeof options !== "object" || Array.isArray(options) ||
      Object.keys(options).some(field => field !== "props")) {
    throw new TypeError("Worker Loader entrypoint options only support props");
  }
  return encodeLoaderJSON(options, 16 * 1024, "Worker Loader props exceed 16 KiB");
}

function isApplicationRpcMethod(name) {
  return typeof name === "string" && !name.startsWith("_") && ![
    "constructor", "prototype", "ctx", "env", "then", "toJSON", "toString", "valueOf",
    "fetch", "connect", "scheduled", "alarm", "webSocketMessage", "webSocketClose", "webSocketError",
  ].includes(name);
}
`

const applicationLoaderSource = applicationLoaderCode + `
import { RpcTarget as LoaderRpcTarget } from "cloudflare:workers";

async function readChildCode(input, token) {
  if (typeof input === "string" && new TextEncoder().encode(input).byteLength <= childCodeLimit) return input;
  if (!token || !(input instanceof ReadableStream)) throw new RangeError("WorkerCode exceeds 512 KiB");
  const reader = input.getReader();
  const decoder = new TextDecoder("utf-8", {fatal: true});
  const parts = [];
  let length = 0;
  try {
    for (;;) {
      const {done, value} = await reader.read();
      if (done) break;
      length += value.byteLength;
      if (length > freshChildCodeLimit) throw new RangeError("WorkerCode exceeds 8 MiB");
      parts.push(decoder.decode(value, {stream: true}));
    }
    parts.push(decoder.decode());
    return parts.join("");
  } catch (error) {
    await reader.cancel(error).catch(() => {});
    throw error;
  } finally {
    reader.releaseLock();
  }
}

async function decodeChildCode(env, key, token, binding, encoded) {
  const descriptor = normalizeChildCode(JSON.parse(await readChildCode(encoded, token)), true);
  // The dispatcher can only resolve references in the owning parent's immutable snapshot.
  let publishedModules;
  for (const module of Object.values(descriptor.modules)) {
    if (typeof module.wasm?.published !== "string") continue;
    publishedModules ??= (await loadWorkerSource(env, key)).modules;
    const name = module.wasm.published;
    if (!Object.hasOwn(publishedModules, name) || typeof publishedModules[name].wasm !== "string") {
      throw new TypeError("Worker Loader published Wasm module unavailable");
    }
    module.wasm = Uint8Array.fromBase64(publishedModules[name].wasm).buffer;
  }
  const {globalOutbound, tails} = descriptor;
  if ((globalOutbound || tails?.length) && !token) {
    throw new TypeError("Worker Loader outbound and tails require load()");
  }
  let child;
  if (globalOutbound && descriptor.limits?.subRequests !== undefined) {
    child = crypto.randomUUID();
    await admitLoader(env, key, token, {binding, child, subrequests: descriptor.limits.subRequests});
  }
  return {...descriptor,
    globalOutbound: globalOutbound && parentExports.ApplicationChildOutbound({props: {key, token, child, entrypoint: globalOutbound}}),
    tails: tails?.map(entrypoint => parentExports.ApplicationChildTail({props: {key, token, entrypoint}})),
  };
}

async function invokeApplicationExport(env, ctx, method, argument) {
  const {key, token, child: loaderChild, entrypoint} = ctx.props;
  const headers = {"X-Nanoflare-Invocation": token, "X-Nanoflare-Application-Loader": "true"};
  if (loaderChild !== undefined) headers["X-Nanoflare-Application-Child"] = loaderChild;
  const response = await env.BROKER.fetch(brokerURL("/export/" + encodeURIComponent(key)), {
    method: "POST", headers,
  });
  if (response.status === 429) throw new Error("Worker Loader child subrequest limit exceeded");
  if (!response.ok) throw new Error("Worker Loader export admission rejected: " + response.status);
  const child = await response.json();
  try {
    const parent = await load(env, ctx, key);
    const dispatcher = parent.getEntrypoint("NanoflareExportDispatcher", {props: {__nanoflare_invocation: {key, token: child.token}}});
    return await dispatcher.invoke(entrypoint.name, restoreExportProps(entrypoint.props, key, token), method, [argument], false, true);
  } catch (error) {
    ctx.waitUntil(env.BROKER.fetch(brokerURL("/abandon/" + encodeURIComponent(key)), {
      method: "POST", headers: {"X-Nanoflare-Invocation": child.token},
    }));
    throw error;
  }
}

export class ApplicationChildOutbound extends WorkerEntrypoint {
  fetch(request) { return invokeApplicationExport(this.env, this.ctx, "fetch", request); }
}

export class ApplicationChildTail extends WorkerEntrypoint {
  tail(events) {
    // Native TraceItem objects expose a JSON representation but cannot cross RPC.
    return invokeApplicationExport(this.env, this.ctx, "tail", JSON.parse(JSON.stringify(events)));
  }
}

async function admitLoader(env, key, token, input) {
  const response = await env.BROKER.fetch(brokerURL("/application-loader/" + encodeURIComponent(key)), {
    method: "POST",
    headers: {"content-type": "application/json", "X-Nanoflare-Invocation": token},
    body: JSON.stringify(input),
  });
  if (!response.ok) throw new Error("Worker Loader admission rejected: " + response.status);
}

function fetchApplicationWorker(worker, name, encodedOptions, request) {
  const options = encodedOptions === undefined ? undefined : JSON.parse(encodedOptions);
  const signal = AbortSignal.any([request.signal, AbortSignal.timeout(30_000)]);
  return worker.getEntrypoint(name, options).fetch(new Request(request, {signal}));
}

function invokeApplicationWorker(worker, name, encodedOptions, method, args) {
  if (!isApplicationRpcMethod(method) || !Array.isArray(args)) {
    throw new TypeError("Invalid Worker Loader RPC method");
  }
  const options = encodedOptions === undefined ? undefined : JSON.parse(encodedOptions);
  return worker.getEntrypoint(name, options)[method](...args);
}

class FreshApplicationWorker extends LoaderRpcTarget {
  constructor(bridge, worker, token, binding) {
    super();
    this.bridge = bridge;
    this.worker = worker;
    this.token = token;
    this.binding = binding;
  }

  async fetch(name, options, request) {
    await this.bridge.admit(this.token, this.binding);
    return fetchApplicationWorker(this.worker, name, options, request);
  }

  async invoke(name, options, method, args) {
    await this.bridge.admit(this.token, this.binding);
    return invokeApplicationWorker(this.worker, name, options, method, args);
  }
}

export class ApplicationLoaderBridge extends WorkerEntrypoint {
  async _cached(token, binding, id, callback) {
    const key = this.ctx.props.key;
    await admitLoader(this.env, key, token, {binding, id});
    return this.env.APPLICATION_LOADER.get(JSON.stringify([key, binding, id]), async () =>
      decodeChildCode(this.env, key, undefined, binding, await callback.code()));
  }

  async fetchCached(token, binding, id, callback, name, options, request) {
    return fetchApplicationWorker(await this._cached(token, binding, id, callback), name, options, request);
  }

  async invokeCached(token, binding, id, callback, name, options, method, args) {
    return invokeApplicationWorker(await this._cached(token, binding, id, callback), name, options, method, args);
  }

  async createFresh(token, binding, descriptor) {
    const env = this.env;
    const key = this.ctx.props.key;
    await admitLoader(env, key, token, {binding});
    const worker = env.APPLICATION_LOADER.load(await decodeChildCode(env, key, token, binding, descriptor));
    const bridge = { admit: (token, binding) => admitLoader(env, key, token, {binding}) };
    return new FreshApplicationWorker(bridge, worker, token, binding);
  }
}
`

const applicationLoaderFacade = applicationLoaderCode + `
const publishedApplicationWasm = new WeakMap();
export function registerPublishedApplicationWasm(module, name) {
  publishedApplicationWasm.set(module, name);
}

function encodeApplicationBinding(value, owner, method) {
  const reference = exportReferences.get(value);
  if (!owner || reference?.owner !== owner || !reference.methods.includes(method)) {
    throw new TypeError("Worker Loader " + method + " binding requires a ctx.exports entrypoint from this load() invocation");
  }
  return {name: reference.name, props: exportProps(reference.props)};
}

function encodeChildCode(descriptor, owner, fresh = false) {
  const limit = fresh ? freshChildCodeLimit : childCodeLimit;
  return encodeLoaderJSON(normalizeChildCode(descriptor, false, publishedApplicationWasm, owner, limit),
    limit, fresh ? "WorkerCode exceeds 8 MiB" : "WorkerCode exceeds 512 KiB");
}

class ApplicationCodeCallback extends RpcTarget {
  constructor(callback) {
    super();
    this.callback = callback;
  }

  async code() {
    return encodeChildCode(await this.callback());
  }
}

function applicationEntrypoint(fetch, invoke) {
  let disposed = false;
  function checkOpen() {
    if (disposed) throw new TypeError("Worker Loader entrypoint was disposed");
  }
  const entrypoint = {
    fetch(input, init) {
      checkOpen();
      return fetchWithSignal(new Request(input, init), fetch);
    },
    [Symbol.dispose]() { disposed = true; },
  };
  return new Proxy(entrypoint, {
    get(target, name) {
      if (Object.hasOwn(target, name)) return target[name];
      if (!isApplicationRpcMethod(name)) return undefined;
      return (...args) => {
        checkOpen();
        return invoke(name, args);
      };
    },
  });
}

function applicationLoader(binding) {
  function invocation() {
    const state = storage.getStore();
    if (!state?.applicationLoader) throw new Error("Worker Loader used outside an admitted invocation");
    return state;
  }
  return Object.freeze({
    get(id, callback) {
      if (typeof id !== "string" || typeof callback !== "function") {
        throw new TypeError("Worker Loader get requires a string ID and code callback");
      }
      if (!id.isWellFormed() || new TextEncoder().encode(id).byteLength > 256) {
        throw new RangeError("Worker Loader ID must be valid Unicode and at most 256 bytes");
      }
      return {getEntrypoint(name, options) {
        const encodedOptions = encodeEntrypointOptions(options);
        function cached(operation, ...args) {
          const state = invocation();
          return state.applicationLoader[operation](state.applicationLoaderToken, binding, id,
            new ApplicationCodeCallback(() => storage.run(state, callback)), name, encodedOptions, ...args);
        }
        return applicationEntrypoint(request => cached("fetchCached", request),
          (method, args) => cached("invokeCached", method, args));
      }};
    },
    load(descriptor) {
      const owner = descriptor?.globalOutbound != null || descriptor?.tails?.length ? invocation() : undefined;
      const code = new Blob([encodeChildCode(descriptor, owner, true)]);
      const ownerToken = owner?.applicationLoaderToken;
      const workers = new WeakMap();
      return {getEntrypoint(name, options) {
        const encodedOptions = encodeEntrypointOptions(options);
        function workerForInvocation() {
          const state = invocation();
          if (ownerToken && state.applicationLoaderToken !== ownerToken) {
            throw new TypeError("Worker Loader binding belongs to another invocation");
          }
          let worker = workers.get(state);
          if (!worker) {
            worker = state.applicationLoader.createFresh(state.applicationLoaderToken, binding, code.stream());
            workers.set(state, worker);
          }
          return worker;
        }
        return applicationEntrypoint(async request => (await workerForInvocation()).fetch(name, encodedOptions, request),
          async (method, args) => (await workerForInvocation()).invoke(name, encodedOptions, method, args));
      }};
    },
  });
}
`
