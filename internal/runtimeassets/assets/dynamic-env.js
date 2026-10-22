import { AsyncLocalStorage } from "node:async_hooks";
const invocation = new AsyncLocalStorage();
export function withInvocation(ctx, run) {
  return invocation.run({ broker: ctx.props.__NANOFLARE_BROKER, ctx }, run);
}
async function scopedFetch(input, init) {
  const broker = invocation.getStore()?.broker;
  if (!broker) throw new TypeError("Outbound fetch requires a live invocation");
  if (init?.cf) throw new TypeError("Custom fetch/cache options are unavailable");
  let request = new Request(input, init);
  const mode = request.redirect;
  for (let hop = 0; hop <= 20; hop++) {
    if (new URL(request.url).username || new URL(request.url).password)
      throw new TypeError("URL credentials are unavailable");
    if (request.headers.has("upgrade")) throw new TypeError("Outbound WebSockets are unavailable");
    const replay = request.clone();
    const response = await broker.outbound(request);
    if (
      ![301, 302, 303, 307, 308].includes(response.status) ||
      !response.headers.has("location") ||
      mode === "manual"
    )
      return response;
    if (mode === "error") {
      await response.body?.cancel();
      throw new TypeError("Redirect rejected");
    }
    await response.body?.cancel();
    if (hop === 20) throw new TypeError("Too many redirects");
    const nextURL = new URL(response.headers.get("location"), replay.url);
    const headers = new Headers(replay.headers);
    if (nextURL.origin !== new URL(replay.url).origin) {
      headers.delete("authorization");
      headers.delete("cookie");
    }
    const get =
      (response.status === 303 && replay.method !== "HEAD") ||
      ([301, 302].includes(response.status) && replay.method === "POST");
    if (get) {
      headers.delete("content-type");
      headers.delete("content-length");
    }
    request = new Request(nextURL, {
      method: get ? "GET" : replay.method,
      headers,
      body: get || ["GET", "HEAD"].includes(replay.method) ? null : replay.body,
      redirect: mode,
    });
  }
}
Object.defineProperty(globalThis, "fetch", {
  value: scopedFetch,
  writable: false,
  configurable: false,
});

// The native loader currently has no supported cache backend contract. Reject
// cache operations explicitly rather than exposing workerd's silent local no-op.
const unavailableCache = () => {
  throw new TypeError("Cache API is unavailable in the private dynamic profile");
};
const nativeStorage = globalThis.caches;
const nativeCache = nativeStorage.default;
for (const [object, names] of [
  [Object.getPrototypeOf(nativeStorage), ["open"]],
  [Object.getPrototypeOf(nativeCache), ["match", "put", "delete"]],
]) {
  for (const name of names)
    Object.defineProperty(object, name, {
      value: unavailableCache,
      writable: false,
      configurable: false,
    });
}
Object.defineProperty(globalThis, "caches", { get: unavailableCache, configurable: false });

import { wrapEnv } from "nanoflare-internal:runtime-bindings.js";

function kvBinding(broker, name) {
  const fetch = (path, init) =>
    broker.effect("kv", name, new Request(`https://kv.internal/${path}`, init));
  const keyPath = (key) => encodeURIComponent(String(key));
  const check = async (response) => {
    if (!response.ok)
      throw new Error(`KV request failed (${response.status}): ${await response.text()}`);
    return response;
  };
  return {
    async get(key, options = "text") {
      if (typeof options === "object" && Object.keys(options).some((k) => k !== "type"))
        throw new TypeError("KV metadata/cache options are unavailable");
      const type = typeof options === "string" ? options : options.type || "text";
      if (!["text", "json", "arrayBuffer", "stream"].includes(type))
        throw new TypeError("Unsupported KV type");
      const response = await fetch(keyPath(key) + "?urlencoded=true");
      if (response.status === 404) return null;
      await check(response);
      if (type === "stream") return response.body;
      return response[type]();
    },
    async getWithMetadata(key, options) {
      return { value: await this.get(key, options), metadata: null };
    },
    async put(key, value, options = {}) {
      if (Object.keys(options).some((k) => !["expiration", "expirationTtl"].includes(k)))
        throw new TypeError("Unsupported KV put options");
      const query = new URLSearchParams({ urlencoded: "true" });
      if (options.expiration !== undefined) query.set("expiration", String(options.expiration));
      if (options.expirationTtl !== undefined)
        query.set("expiration_ttl", String(options.expirationTtl));
      await check(await fetch(keyPath(key) + "?" + query, { method: "PUT", body: value }));
    },
    async delete(key) {
      await check(await fetch(keyPath(key) + "?urlencoded=true", { method: "DELETE" }));
    },
    async list(options = {}) {
      if (Object.keys(options).some((k) => !["prefix", "limit", "cursor"].includes(k)))
        throw new TypeError("Unsupported KV list options");
      const query = new URLSearchParams();
      for (const [k, v] of Object.entries(options))
        query.set(k === "limit" ? "key_count_limit" : k, String(v));
      return (await check(await fetch("?" + query))).json();
    },
  };
}
export function dynamicEnv(values, ctx, metadata) {
  const env = Object.create(values),
    broker = ctx.props.__NANOFLARE_BROKER;
  for (const b of metadata.kv_namespaces || [])
    Object.defineProperty(env, b.binding, {
      value: kvBinding(broker, b.binding),
      enumerable: true,
    });
  for (const [kind, bindings] of [
    ["db", metadata.db || []],
    ["objects", metadata.object_storage_buckets || []],
  ])
    for (const b of bindings)
      Object.defineProperty(env, b.binding, {
        value: {
          fetch(input, init) {
            return broker.effect(kind, b.binding, new Request(input, init));
          },
        },
        enumerable: true,
      });
  for (const b of metadata.services || [])
    Object.defineProperty(env, b.binding, {
      value: {
        fetch(input, init) {
          return broker.service(b.binding, new Request(input, init));
        },
      },
      enumerable: true,
    });
  const asset = metadata.asset_config?.binding || "ASSETS";
  Object.defineProperty(env, asset, {
    value: {
      fetch(input, init) {
        if (typeof input === "string" && input.startsWith("/"))
          input = "https://assets.internal" + input;
        return broker.effect("assets", asset, new Request(input, init));
      },
    },
    enumerable: true,
  });
  return wrapEnv(
    env,
    asset,
    (metadata.db || []).map((b) => b.binding),
    (metadata.object_storage_buckets || []).map((b) => b.binding),
  );
}

for (const level of ["log", "info", "debug", "warn", "error"]) {
  console[level] = (...values) => {
    const current = invocation.getStore();
    if (!current) return;
    let message = values
      .slice(0, 64)
      .map((value) => {
        try {
          return typeof value === "string" ? value : JSON.stringify(value);
        } catch {
          return String(value);
        }
      })
      .join(" ");
    if (new TextEncoder().encode(message).byteLength > 4096)
      message = "[log message omitted: exceeds collection limit]";
    current.ctx.waitUntil(
      Promise.resolve(current.broker.log(level === "log" ? "info" : level, message)).catch(
        () => {},
      ),
    );
  };
}
