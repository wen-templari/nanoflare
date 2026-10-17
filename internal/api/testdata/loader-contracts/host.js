import { WorkerEntrypoint } from "cloudflare:workers";

const active = new Map();

function identityTarget(env) {
  const identity = active.get("identity");
  return env.LOADER.get(`identity:${identity.revision}`, () => ({
    compatibilityDate: "2026-09-08",
    ...(identity.program ?? { mainModule: "worker.js", modules: { "worker.js": identity.source } }),
    env: {
      __NANOFLARE_APP_ID: env.APP_ID,
      __NANOFLARE_DEPLOYMENT_ID: env.DEPLOYMENT_ID,
      __NANOFLARE_DURATION_COLLECTOR: env.EVENTS,
    },
    globalOutbound: null,
  })).getEntrypoint();
}

export class IdentityBridge extends WorkerEntrypoint {
  getUser(id) {
    return identityTarget(this.env).getUser(id);
  }
  session() {
    return identityTarget(this.env).session();
  }
  fetch(request) {
    return identityTarget(this.env).fetch(request);
  }
}

export class KVBridge extends WorkerEntrypoint {
  get(key, options) {
    return this.env.KV.get(key, options);
  }
  put(key, value, options) {
    return this.env.KV.put(key, value, options);
  }
  delete(key) {
    return this.env.KV.delete(key);
  }
  list(options) {
    return this.env.KV.list(options);
  }
}

export class ResourceBroker extends WorkerEntrypoint {
  async fetch(request) {
    const token = request.headers.get("X-Nanoflare-Invocation");
    const name = request.headers.get("X-Nanoflare-Binding-Name");
    if (!token?.startsWith("invocation-")) return new Response("unauthorized", { status: 403 });
    await this.env.EVENTS.fetch("http://events.invalid/invocation-resource", {
      method: "POST", body: JSON.stringify({ token, binding: name, path: new URL(request.url).pathname }),
    });
    const binding = { KV: this.env.KV_BACKEND, DB: this.env.DB, OBJECTS: this.env.OBJECTS, ASSETS: this.env.ASSETS }[name];
    if (!binding) return new Response("unknown binding", { status: 403 });
    return binding.fetch(request);
  }
}

export class LifecycleBroker extends WorkerEntrypoint {
  async fetch(request) {
    const path = new URL(request.url).pathname;
    if (path !== "/complete" && path !== "/start") return new Response("bad path", { status: 400 });
    await this.env.EVENTS.fetch("http://events.invalid/invocation-" + path.slice(1), {
      method: "POST", body: request.headers.get("X-Nanoflare-Invocation"),
    });
    return new Response(null, { status: 204 });
  }
}

export class ServiceBroker extends WorkerEntrypoint {
  invoke(token, binding, method, args) {
    if (!token.startsWith("invocation-") || binding !== "IDENTITY") throw new Error("unauthorized service");
    return identityTarget(this.env)[method](...args);
  }
  fetch(token, binding, request) {
    if (!token.startsWith("invocation-") || binding !== "IDENTITY") throw new Error("unauthorized service");
    return identityTarget(this.env).fetch(request);
  }
}

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    if (url.pathname === "/health") return new Response("ready");
    if (url.pathname === "/static-upstream")
      return env.EVENTS.fetch("http://events.invalid/disconnect/static");
    if (url.pathname === "/static-cancel") {
      let timer;
      return new Response(
        new ReadableStream({
          start(controller) {
            controller.enqueue(new TextEncoder().encode("started"));
            timer = setInterval(
              () => controller.enqueue(new TextEncoder().encode("heartbeat")),
              20,
            );
          },
          cancel() {
            clearInterval(timer);
            console.log("CANCELLED:static");
          },
        }),
      );
    }
    if (url.pathname === "/publish") {
      const { name, revision, source, program } = await request.json();
      active.set(name, { revision, source, program });
      return new Response("published");
    }
    const [, name, ...segments] = url.pathname.split("/");
    const record = active.get(name);
    if (!record) return new Response("missing", { status: 404 });
    const bindings = { EVENTS: env.EVENTS, KV: env.KV };
    if (name === "bridged-kv") bindings.KV = ctx.exports.KVBridge({ props: {} });
    if (name.startsWith("resource-"))
      Object.assign(bindings, {
        KV: ctx.exports.KVBridge({ props: {} }),
        DB: env.DB,
        OBJECTS: env.OBJECTS,
        ASSETS: env.ASSETS,
        __NANOFLARE_APP_ID: env.APP_ID,
        __NANOFLARE_DEPLOYMENT_ID: env.DEPLOYMENT_ID,
        __NANOFLARE_DURATION_COLLECTOR: env.EVENTS,
        __NANOFLARE_DNS: env.DNS,
      });
    if (name.startsWith("invocation-")) Object.assign(bindings, {
      __NANOFLARE_RESOURCE_BROKER: ctx.exports.ResourceBroker({ props: {} }),
      __NANOFLARE_LIFECYCLE: ctx.exports.LifecycleBroker({ props: {} }),
      __NANOFLARE_SERVICE_BROKER: ctx.exports.ServiceBroker({ props: {} }),
      __NANOFLARE_APP_ID: env.APP_ID,
      __NANOFLARE_DEPLOYMENT_ID: env.DEPLOYMENT_ID,
      __NANOFLARE_DURATION_COLLECTOR: env.EVENTS,
    });
    if (name === "caller") {
      const identity = active.get("identity");
      bindings.IDENTITY = env.LOADER.get(`identity:${identity.revision}`, () => ({
        compatibilityDate: "2026-09-08",
        mainModule: "worker.js",
        modules: { "worker.js": identity.source },
        globalOutbound: null,
      })).getEntrypoint();
    }
    if (name === "bridged-caller") bindings.IDENTITY = ctx.exports.IdentityBridge({ props: {} });
    const target = env.LOADER.get(`${name}:${record.revision}`, () => ({
      compatibilityDate: "2026-09-08",
      compatibilityFlags: ["nodejs_compat"],
      ...(record.program ?? { mainModule: "worker.js", modules: { "worker.js": record.source } }),
      env:
        name === "native-kv" || name === "bridged-kv" || name.startsWith("resource-")
          ? bindings
          : { ...bindings, KV: undefined },
      globalOutbound: name === "resource-network" || name === "tcp" ? env.EGRESS : null,
    })).getEntrypoint(undefined, name.startsWith("invocation-") ? {
      props: { __nanoflare_invocation: {
        token: request.headers.get("X-Test-Invocation") ?? name,
        key: `${name}:${record.revision}`,
      }, publicValue: "safe" },
    } : undefined);
    url.pathname = `/${segments.join("/")}`;
    if (name === "invocation-class" && url.pathname === "/method")
      return Response.json(await target.inspect("direct-rpc"));
    if (name === "invocation-class" && url.pathname === "/nested") {
      using session = await target.session();
      return Response.json({ value: await session.greet() });
    }
    return target
      .fetch(new Request(url, request))
      .catch((error) => Response.json({ error: String(error) }, { status: 500 }));
  },
};
