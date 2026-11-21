package config

const applicationLoaderCode = `
function encodeChildCode(descriptor) {
  if (!descriptor || typeof descriptor !== "object" || Array.isArray(descriptor)) {
    throw new TypeError("Worker Loader requires a WorkerCode object");
  }
  const fields = new Set(["compatibilityDate", "compatibilityFlags", "mainModule", "modules", "env", "globalOutbound"]);
  for (const field of Object.keys(descriptor)) {
    if (!fields.has(field)) throw new TypeError("Unsupported WorkerCode field: " + field);
  }
  if (descriptor.globalOutbound !== undefined && descriptor.globalOutbound !== null) {
    throw new TypeError("Worker Loader outbound is disabled");
  }
  const {compatibilityDate, mainModule, modules, env} = descriptor;
  if (typeof compatibilityDate !== "string" || typeof mainModule !== "string" ||
      !modules || typeof modules !== "object" || Array.isArray(modules) || !Object.hasOwn(modules, mainModule)) {
    throw new TypeError("WorkerCode requires compatibilityDate, mainModule and modules");
  }
  for (const module of Object.values(modules)) {
    if (typeof module === "string") continue;
    if (!module || typeof module !== "object" || Array.isArray(module)) throw new TypeError("Invalid Worker module");
    const fields = Object.keys(module);
    if (fields.length !== 1 || !["js", "cjs", "text", "json"].includes(fields[0]) ||
        (fields[0] !== "json" && typeof module[fields[0]] !== "string")) {
      throw new TypeError("Unsupported Worker module type");
    }
  }
  if (env !== undefined && (!env || typeof env !== "object" || Array.isArray(env))) {
    throw new TypeError("WorkerCode.env must be an object");
  }
  return encodeLoaderJSON(descriptor, 512 * 1024, "WorkerCode exceeds 512 KiB");
}

function encodeLoaderJSON(input, limit, message) {
  const encoded = JSON.stringify(input, (_, value) => {
    if (value !== null && typeof value === "object" &&
        !Array.isArray(value) && Object.getPrototypeOf(value) !== Object.prototype &&
        Object.getPrototypeOf(value) !== null) {
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
`

const applicationLoaderSource = applicationLoaderCode + `
import { RpcTarget as LoaderRpcTarget } from "cloudflare:workers";

function decodeChildCode(encoded) {
  if (typeof encoded !== "string" || new TextEncoder().encode(encoded).byteLength > 512 * 1024) {
    throw new RangeError("WorkerCode exceeds 512 KiB");
  }
  const descriptor = JSON.parse(encoded);
  encodeChildCode(descriptor);
  return {...descriptor, globalOutbound: null};
}

async function admitLoader(env, key, token, binding, id) {
  const response = await env.BROKER.fetch(brokerURL("/application-loader/" + encodeURIComponent(key)), {
    method: "POST",
    headers: {"content-type": "application/json", "X-Nanoflare-Invocation": token},
    body: JSON.stringify({binding, id}),
  });
  if (!response.ok) throw new Error("Worker Loader admission rejected: " + response.status);
}

function fetchApplicationWorker(worker, name, encodedOptions, request) {
  const options = encodedOptions === undefined ? undefined : JSON.parse(encodedOptions);
  const signal = AbortSignal.any([request.signal, AbortSignal.timeout(30_000)]);
  return worker.getEntrypoint(name, options).fetch(new Request(request, {signal}));
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
}

export class ApplicationLoaderBridge extends WorkerEntrypoint {
  async fetchCached(token, binding, id, callback, name, options, request) {
    const key = this.ctx.props.key;
    await admitLoader(this.env, key, token, binding, id);
    const worker = this.env.APPLICATION_LOADER.get(JSON.stringify([key, binding, id]), async () =>
      decodeChildCode(await callback.code()));
    return fetchApplicationWorker(worker, name, options, request);
  }

  async createFresh(token, binding, descriptor) {
    const env = this.env;
    const key = this.ctx.props.key;
    await admitLoader(env, key, token, binding);
    const worker = env.APPLICATION_LOADER.load(decodeChildCode(descriptor));
    const bridge = { admit: (token, binding) => admitLoader(env, key, token, binding) };
    return new FreshApplicationWorker(bridge, worker, token, binding);
  }
}
`

const applicationLoaderFacade = applicationLoaderCode + `
class ApplicationCodeCallback extends RpcTarget {
  constructor(callback) {
    super();
    this.callback = callback;
  }

  async code() {
    return encodeChildCode(await this.callback());
  }
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
        return {fetch(input, init) {
          const state = invocation();
          return fetchWithSignal(new Request(input, init), request =>
            state.applicationLoader.fetchCached(state.applicationLoaderToken, binding, id,
              new ApplicationCodeCallback(() => storage.run(state, callback)), name, encodedOptions, request));
        }};
      }};
    },
    load(descriptor) {
      const code = encodeChildCode(descriptor);
      const workers = new WeakMap();
      return {getEntrypoint(name, options) {
        const encodedOptions = encodeEntrypointOptions(options);
        return {fetch(input, init) {
          const state = invocation();
          return fetchWithSignal(new Request(input, init), async request => {
            let worker = workers.get(state);
            if (!worker) {
              worker = state.applicationLoader.createFresh(state.applicationLoaderToken, binding, code);
              workers.set(state, worker);
            }
            return (await worker).fetch(name, encodedOptions, request);
          });
        }};
      }};
    },
  });
}
`
