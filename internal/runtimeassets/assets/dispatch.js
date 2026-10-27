import { WorkerEntrypoint } from "cloudflare:workers";

// Cache only immutable code/configuration. Invocation capabilities never enter
// WorkerCode.env, so one warm revision cannot retain another request's authority.
const revisions = new Map();
const maxRevisions = 64;
const maxCodeBytes = 32 * 1024 * 1024;
let codeBytes = 0;

function argumentError(message) {
  return Object.assign(new TypeError(message), { status: 422 });
}
function requireSupportedOptions(options, dispatchOptions) {
  if (!options || typeof options !== "object" || Object.keys(options).some((k) => k !== "props"))
    throw argumentError("Only native props are supported in dispatch options");
  if (
    !dispatchOptions ||
    typeof dispatchOptions !== "object" ||
    Object.keys(dispatchOptions).length
  )
    throw argumentError("Custom CPU/outbound/limit options are not supported by this profile");
}
async function requireOK(response, missingWorker = false) {
  if (response.ok) return response;
  const detail = await response.text();
  if (missingWorker && response.status === 404)
    throw Object.assign(new Error("Worker not found"), { status: 404 });
  throw Object.assign(new Error(`Dispatch failed (${response.status}): ${detail}`), {
    status: response.status,
  });
}
let parentExports;
export function setDispatchExports(value) {
  parentExports = value;
}
function dispatchControl(entrypoint) {
  return entrypoint.ctx.props.resourceControl
    ? (parentExports || entrypoint.ctx.exports).ResourceBridge({
        props: entrypoint.ctx.props.resourceControl,
      })
    : entrypoint.env[entrypoint.ctx.props.control];
}
export class DispatchBroker extends WorkerEntrypoint {
  async effect(kind, binding, request) {
    const control = dispatchControl(this);
    const original = new Request(request);
    const target = new URL(original.url);
    target.protocol = "http:";
    target.host = "nanoflare.internal";
    target.pathname = `/internal/runtime/dispatch/effect/${encodeURIComponent(kind)}/${encodeURIComponent(binding)}${target.pathname}`;
    const headers = new Headers(original.headers);
    headers.set("X-Nanoflare-Invocation", this.ctx.props.lease);
    headers.delete("Authorization");
    return control.fetch(
      new Request(target, {
        method: original.method,
        headers,
        body: original.body,
        redirect: "manual",
      }),
    );
  }
  async log(level, message) {
    return dispatchControl(this).fetch("http://nanoflare.internal/internal/runtime/dispatch/log", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Nanoflare-Invocation": this.ctx.props.lease,
      },
      body: JSON.stringify({ level, message }),
    });
  }
  async outbound(request) {
    const headers = new Headers(request.headers);
    headers.set("X-Nanoflare-Outbound-URL", request.url);
    headers.set("X-Nanoflare-Outbound-Authorization", request.headers.get("Authorization") || "");
    headers.set("X-Nanoflare-Invocation", this.ctx.props.lease);
    headers.delete("Authorization");
    return dispatchControl(this).fetch(
      "http://nanoflare.internal/internal/runtime/dispatch/outbound",
      { method: request.method, headers, body: request.body, redirect: "manual" },
    );
  }
  async service(binding, request) {
    const response = await requireOK(
      await dispatchControl(this).fetch(
        "http://nanoflare.internal/internal/runtime/dispatch/nested",
        {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ lease: this.ctx.props.lease, binding }),
        },
      ),
    );
    return invokeArtifact(
      {
        ...this.env,
        __NANOFLARE_WORKER_LOADER: this.env.__NANOFLARE_WORKER_LOADER || this.env.LOADER,
        __NANOFLARE_RESOURCE_CONTROL: this.ctx.props.resourceControl,
        [this.ctx.props.control]: dispatchControl(this),
      },
      this.ctx,
      this.ctx.props.control,
      await response.json(),
      new Request(request),
      {},
    );
  }
  async finish(cancel) {
    const response = await dispatchControl(this).fetch(
      "http://nanoflare.internal/internal/runtime/dispatch/finish",
      {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ lease: this.ctx.props.lease, cancel: Boolean(cancel) }),
      },
    );
    // Expiry is terminal and cannot be refreshed by completing a late stream.
    if (response.status !== 410) await requireOK(response);
  }
}
function trackResponse(response, broker, ctx) {
  if (response.status === 101 || response.webSocket) {
    ctx.waitUntil(broker.finish(true));
    throw Object.assign(new Error("WebSockets are not available in private dynamic dispatch"), {
      status: 501,
    });
  }
  if (!response.body) {
    ctx.waitUntil(broker.finish(false));
    return response;
  }
  const reader = response.body.getReader();
  let finished = false;
  const finish = (cancel) => {
    if (finished) return;
    finished = true;
    ctx.waitUntil(broker.finish(cancel));
  };
  const body = new ReadableStream({
    async pull(controller) {
      try {
        const part = await reader.read();
        if (part.done) {
          finish(false);
          controller.close();
        } else controller.enqueue(part.value);
      } catch (error) {
        finish(true);
        controller.error(error);
      }
    },
    async cancel(reason) {
      finish(true);
      await reader.cancel(reason);
    },
  });
  return new Response(body, {
    status: response.status,
    statusText: response.statusText,
    headers: response.headers,
  });
}
async function invokeArtifact(env, ctx, control, artifact, request, props) {
  const broker = (parentExports || ctx.exports).DispatchBroker({
    props: { control, lease: artifact.lease, resourceControl: env.__NANOFLARE_RESOURCE_CONTROL },
  });
  try {
    if (request.headers.get("upgrade"))
      throw Object.assign(new Error("WebSockets are not supported"), { status: 501 });
    const key = `${artifact.deployment.app_id}:${artifact.deployment.id}:${artifact.digest}`;
    let cached = revisions.get(key);
    let code = cached?.code;
    let wasmBytes = 0;
    if (cached) {
      revisions.delete(key);
      codeBytes -= cached.bytes;
    } else {
      const modules = Object.create(null);
      for (const file of artifact.deployment.files) {
        if (file.wasm !== undefined) {
          const bytes = Uint8Array.from(atob(file.wasm), (byte) => byte.charCodeAt(0));
          modules[file.path] = { wasm: bytes.buffer };
          wasmBytes += bytes.byteLength;
        } else {
          modules[file.path] = { js: file.content };
        }
      }
      modules["__nanoflare_dynamic_entrypoint__.js"] = { js: artifact.wrapper };
      modules["nanoflare-internal:runtime-bindings.js"] = { js: artifact.runtime_bindings };
      modules["nanoflare-internal:dynamic-env.js"] = { js: artifact.dynamic_env };
      code = {
        compatibilityDate: artifact.deployment.compatibility_date,
        compatibilityFlags: [
          ...new Set([...(artifact.deployment.compatibility_flags || []), "nodejs_als"]),
        ],
        mainModule: "__nanoflare_dynamic_entrypoint__.js",
        modules,
        env: artifact.values,
        globalOutbound: env.__NANOFLARE_WFP_SOCKET_OUTBOUND ?? null,
      };
    }
    if (!cached)
      cached = {
        code,
        // The administrator service capability is not JSON code payload.
        bytes:
          new TextEncoder().encode(JSON.stringify({ ...code, globalOutbound: null })).byteLength +
          wasmBytes,
      };
    revisions.set(key, cached);
    codeBytes += cached.bytes;
    while (revisions.size > maxRevisions || codeBytes > maxCodeBytes) {
      const oldest = revisions.keys().next().value;
      codeBytes -= revisions.get(oldest).bytes;
      revisions.delete(oldest);
    }
    // get() returns a request-bound native channel. Its ID allows the
    // runtime to reuse code, but the channel itself must never be cached.
    const worker = env.__NANOFLARE_WORKER_LOADER.get(key, () => code);
    request.headers.delete("X-Nanoflare-Dispatch-Root");
    const target = worker.getEntrypoint(undefined, {
      props: { ...props, __NANOFLARE_BROKER: broker },
    });
    return trackResponse(await target.fetch(request), broker, ctx);
  } catch (error) {
    ctx.waitUntil(broker.finish(true));
    throw error;
  }
}
export function wrapDispatchEnv(env, ctx, root) {
  const result = Object.create(env);
  for (const [binding, control] of Object.entries(env.__NANOFLARE_DISPATCH_BINDINGS || {})) {
    Object.defineProperty(result, binding, {
      enumerable: true,
      value: {
        get(script, options = {}, dispatchOptions = {}) {
          requireSupportedOptions(options, dispatchOptions);
          if (typeof script !== "string" || !/^[a-z0-9][a-z0-9_-]{0,62}$/.test(script))
            throw argumentError("Invalid script name");
          const props = options.props === undefined ? {} : options.props;
          if (
            !props ||
            Array.isArray(props) ||
            typeof props !== "object" ||
            Object.keys(props).some((k) => k.startsWith("__NANOFLARE_"))
          )
            throw argumentError("Invalid native props");
          return {
            async fetch(input, init) {
              const request = new Request(input, init);
              if (request.headers.get("upgrade"))
                throw Object.assign(new Error("WebSockets are not supported"), { status: 501 });
              if (env.__NANOFLARE_WFP)
                return env.__NANOFLARE_WFP.invoke(
                  binding,
                  script,
                  options,
                  dispatchOptions,
                  root,
                  request,
                );
              const response = await requireOK(
                await env[control].fetch(
                  "http://nanoflare.internal/internal/runtime/dispatch/acquire",
                  {
                    method: "POST",
                    headers: { "content-type": "application/json" },
                    body: JSON.stringify({ script, root }),
                  },
                ),
                true,
              );
              const artifact = await response.json();
              return invokeArtifact(env, ctx, control, artifact, request, props);
            },
          };
        },
      },
    });
  }
  return result;
}
