import { RpcTarget as LoaderRpcTarget } from "cloudflare:workers";

async function readChildCode(input, token) {
  if (typeof input === "string" && new TextEncoder().encode(input).byteLength <= childCodeLimit)
    return input;
  if (!token || !(input instanceof ReadableStream))
    throw new RangeError("WorkerCode exceeds 512 KiB");
  const reader = input.getReader();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  const parts = [];
  let length = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      length += value.byteLength;
      if (length > freshChildCodeLimit) throw new RangeError("WorkerCode exceeds 8 MiB");
      parts.push(decoder.decode(value, { stream: true }));
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
  const { globalOutbound, tails } = descriptor;
  if ((globalOutbound || tails?.length) && !token) {
    throw new TypeError("Worker Loader outbound and tails require load()");
  }
  let child;
  if (globalOutbound && descriptor.limits?.subRequests !== undefined) {
    child = crypto.randomUUID();
    await admitLoader(env, key, token, {
      binding,
      child,
      subrequests: descriptor.limits.subRequests,
    });
  }
  return {
    ...descriptor,
    globalOutbound:
      globalOutbound &&
      parentExports.ApplicationChildOutbound({
        props: { key, token, child, entrypoint: globalOutbound },
      }),
    tails: tails?.map((entrypoint) =>
      parentExports.ApplicationChildTail({ props: { key, token, entrypoint } }),
    ),
  };
}

async function invokeApplicationExport(env, ctx, method, argument) {
  const { key, token, child: loaderChild, entrypoint } = ctx.props;
  const headers = { "X-Nanoflare-Invocation": token, "X-Nanoflare-Application-Loader": "true" };
  if (loaderChild !== undefined) headers["X-Nanoflare-Application-Child"] = loaderChild;
  const response = await env.BROKER.fetch(brokerURL("/export/" + encodeURIComponent(key)), {
    method: "POST",
    headers,
  });
  if (response.status === 429) throw new Error("Worker Loader child subrequest limit exceeded");
  if (!response.ok) throw new Error("Worker Loader export admission rejected: " + response.status);
  const child = await response.json();
  try {
    const parent = await load(env, ctx, key);
    const dispatcher = parent.getEntrypoint("NanoflareExportDispatcher", {
      props: { __nanoflare_invocation: { key, token: child.token } },
    });
    return await dispatcher.invoke(
      entrypoint.name,
      restoreExportProps(entrypoint.props, key, token),
      method,
      [argument],
      false,
      true,
    );
  } catch (error) {
    ctx.waitUntil(
      env.BROKER.fetch(brokerURL("/abandon/" + encodeURIComponent(key)), {
        method: "POST",
        headers: { "X-Nanoflare-Invocation": child.token },
      }),
    );
    throw error;
  }
}

export class ApplicationChildOutbound extends WorkerEntrypoint {
  fetch(request) {
    return invokeApplicationExport(this.env, this.ctx, "fetch", request);
  }
}

export class ApplicationChildTail extends WorkerEntrypoint {
  tail(events) {
    // Native TraceItem objects expose a JSON representation but cannot cross RPC.
    return invokeApplicationExport(this.env, this.ctx, "tail", JSON.parse(JSON.stringify(events)));
  }
}

async function admitLoader(env, key, token, input) {
  const response = await env.BROKER.fetch(
    brokerURL("/application-loader/" + encodeURIComponent(key)),
    {
      method: "POST",
      headers: { "content-type": "application/json", "X-Nanoflare-Invocation": token },
      body: JSON.stringify(input),
    },
  );
  if (!response.ok) throw new Error("Worker Loader admission rejected: " + response.status);
}

function fetchApplicationWorker(worker, name, encodedOptions, request) {
  const options = encodedOptions === undefined ? undefined : JSON.parse(encodedOptions);
  const signal = AbortSignal.any([request.signal, AbortSignal.timeout(30_000)]);
  return worker.getEntrypoint(name, options).fetch(new Request(request, { signal }));
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
    await admitLoader(this.env, key, token, { binding, id });
    return this.env.APPLICATION_LOADER.get(JSON.stringify([key, binding, id]), async () =>
      decodeChildCode(this.env, key, undefined, binding, await callback.code()),
    );
  }

  async fetchCached(token, binding, id, callback, name, options, request) {
    return fetchApplicationWorker(
      await this._cached(token, binding, id, callback),
      name,
      options,
      request,
    );
  }

  async invokeCached(token, binding, id, callback, name, options, method, args) {
    return invokeApplicationWorker(
      await this._cached(token, binding, id, callback),
      name,
      options,
      method,
      args,
    );
  }

  async createFresh(token, binding, descriptor) {
    const env = this.env;
    const key = this.ctx.props.key;
    await admitLoader(env, key, token, { binding });
    const worker = env.APPLICATION_LOADER.load(
      await decodeChildCode(env, key, token, binding, descriptor),
    );
    const bridge = { admit: (token, binding) => admitLoader(env, key, token, { binding }) };
    return new FreshApplicationWorker(bridge, worker, token, binding);
  }
}
