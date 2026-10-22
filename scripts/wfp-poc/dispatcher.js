import { WorkerEntrypoint } from "cloudflare:workers";

import { dispatchNamespace } from "./namespace.js";

// Fixture identities only. Production must resolve its real authentication and grants.
export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    if (url.pathname === "/health") return new Response("ready");
    const [, namespace, script] = url.pathname.split("/");
    const userId = request.headers.get("authorization")?.replace(/^Bearer /, "");
    if (!userId) return new Response("authentication required", { status: 401 });
    if (userId !== script || !["alpha", "beta"].includes(userId)) {
      return new Response("not authorized", { status: 403 });
    }
    try {
      const props = { userId, requestId: crypto.randomUUID() };
      if (url.pathname.endsWith("/capability")) {
        props.CONNECTOR = ctx.exports.Connector({ props: { userId } });
      }
      const options = {};
      if (url.pathname.endsWith("/egress") || url.pathname.endsWith("/subrequests")) {
        options.outbound = { requestContext: { ...props } };
      }
      if (url.pathname.endsWith("/cpu-limit")) options.limits = { cpuMs: 10 };
      if (url.pathname.endsWith("/subrequests")) options.limits = { subRequests: 2 };
      const worker = dispatchNamespace(env, ctx, namespace).get(script, { props }, options);
      return await worker.fetch(request);
    } catch (error) {
      return Response.json({ error: error.message }, { status: error.status ?? 500 });
    }
  },
};

export class Connector extends WorkerEntrypoint {
  identify() {
    return this.ctx.props.userId;
  }
}

export class Outbound extends WorkerEntrypoint {
  async fetch(request) {
    // Never contact the requested host: the PoC uses a deterministic policy sink.
    if (new URL(request.url).hostname !== "allowed.example") {
      return new Response("destination denied", { status: 403 });
    }
    return Response.json({ context: this.ctx.props, url: request.url });
  }
}
