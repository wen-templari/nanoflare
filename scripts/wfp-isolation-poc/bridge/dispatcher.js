import { WorkerEntrypoint } from "cloudflare:workers";

// This shim receives a cell-local credential, not platform/control-plane authority.
export class Resource extends WorkerEntrypoint {
  fetch(request) {
    return this.env.BROKER.fetch("http://broker/effect/" + this.ctx.props.lease, {
      method: "POST",
      redirect: "manual",
      body: JSON.stringify({ operation: "fetch", url: request.url }),
    });
  }
  async identify() {
    const response = await this.env.BROKER.fetch("http://broker/effect/" + this.ctx.props.lease, {
      method: "POST",
      redirect: "manual",
      body: JSON.stringify({ operation: "identify" }),
    });
    if (!response.ok) throw new Error("capability denied: " + response.status);
    return response.json();
  }
}

export default {
  async fetch(request, env, ctx) {
    if (new URL(request.url).pathname === "/health") return new Response("ready");
    const lease = request.headers.get("x-poc-lease");
    const revision = request.headers.get("x-poc-revision");
    const clean = new Request(request);
    for (const name of ["x-poc-lease", "x-poc-revision", "x-poc-cell-key"])
      clean.headers.delete(name);
    // Critical difference from the previous fixture: cache by immutable revision,
    // never by request. Only plain static env belongs in this closure.
    const worker = env.LOADER.get("worker:" + revision, () => ({
      compatibilityDate: "2025-12-10",
      mainModule: "customer.js",
      modules: { "customer.js": env.SOURCE },
      env: { REVISION: revision },
      globalOutbound: null,
    }));
    return worker
      .getEntrypoint(undefined, {
        props: {
          RESOURCE: ctx.exports.Resource({ props: { lease } }),
        },
      })
      .fetch(clean);
  },
};
