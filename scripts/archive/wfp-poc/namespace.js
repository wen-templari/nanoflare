// Experimental workerd Worker Loader facade, not a native WFP namespace binding.
// Registry grants and resource attachment are fixture-owned; user code cannot use REGISTRY/LOADER.
export function dispatchNamespace(env, ctx, namespace) {
  return {
    get(script, { props = {} } = {}, options = {}) {
      return {
        async fetch(request) {
          const response = await env.REGISTRY.fetch(
            `http://registry.invalid/${encodeURIComponent(namespace)}/${encodeURIComponent(script)}`,
          );
          if (!response.ok) {
            throw Object.assign(new Error("Worker not found"), { status: 404 });
          }
          const artifact = await response.json();
          const hasOutbound = options.outbound !== undefined;
          // Request-bound outbound stubs must not be captured in a revision-cached isolate.
          // A unique ID is deliberate here; measuring/optimizing that cost is follow-up work.
          const key = JSON.stringify([
            namespace,
            script,
            artifact.revision,
            ...(hasOutbound ? [props.requestId] : []),
          ]);
          const worker = env.LOADER.get(key, () => ({
            compatibilityDate: "2025-12-10",
            mainModule: "worker.js",
            modules: { "worker.js": artifact.source },
            env: {
              VERSION: artifact.revision,
              ...(namespace === "production" && script === "alpha"
                ? { STATE: env.ALPHA_STATE }
                : {}),
            },
            globalOutbound: hasOutbound
              ? ctx.exports.Outbound({ props: options.outbound.requestContext })
              : null,
            // Only exercised by a bounded disposable CPU probe. Do not advertise support.
            ...(options.limits ? { limits: options.limits } : {}),
          }));
          return worker
            .getEntrypoint(undefined, {
              props,
              ...(options.limits ? { limits: options.limits } : {}),
            })
            .fetch(request);
        },
      };
    },
  };
}
