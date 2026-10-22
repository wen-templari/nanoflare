export { Broker } from "./broker.js";
// Only fixture authority. It has no host credentials or registry access.
export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    if (url.pathname === "/health") return new Response("ready");
    const name = url.searchParams.get("worker") || "alpha";
    if (!["alpha", "beta"].includes(name)) return new Response("unknown", { status: 404 });
    const lease = request.headers.get("x-poc-lease");
    const budgetProbe = url.pathname.startsWith("/budget-");
    const broker = budgetProbe ? ctx.exports.Broker({ props: { lease } }) : undefined;
    const clean = new Request(request);
    clean.headers.delete("x-poc-lease");
    const stub = env.LOADER.get(budgetProbe ? name + ":" + lease : name, () => ({
      compatibilityDate: "2025-12-10",
      compatibilityFlags: ["nodejs_compat"],
      mainModule: "customer.js",
      modules: { "customer.js": env.SOURCE },
      globalOutbound: broker || null,
      env: budgetProbe ? { RESOURCE: broker } : {},
      limits: { cpuMs: 10, subRequests: 2 },
    }));
    return stub.getEntrypoint(undefined, { limits: { cpuMs: 10, subRequests: 2 } }).fetch(clean);
  },
};
