# Dynamic Workers

A deployed module Worker can create child Workers at runtime. Enable the
trusted pilot on `nanoflared` with `--enable-worker-loaders` or
`NANOFLARE_WORKER_LOADERS_ENABLED=true`. The switch allows new deployments to
declare loader bindings. Existing revisions retain their declarations, including
during rollback. Use this pilot for Workers and child code you trust.

Declare the binding in `wrangler.jsonc` and deploy with `nanoflare deploy`.

```jsonc
{
  "name": "parent",
  "main": "src/index.js",
  "compatibility_date": "2026-09-08",
  "worker_loaders": [{ "binding": "LOADER" }],
}
```

In `wrangler.toml`, use `[[worker_loaders]]` with `binding = "LOADER"`.
The Cloudflare-compatible upload API accepts a metadata binding with
`"name": "LOADER"` and `"type": "worker_loader"`. That API has its own
[enablement requirements](../reference/cloudflare-management-api.md).

```js
export default {
  fetch(request, env) {
    const worker = env.LOADER.get("hello-v1", () => ({
      compatibilityDate: "2026-09-08",
      mainModule: "hello.js",
      modules: {
        "hello.js":
          "export default { fetch() { return new Response('Hello from a child Worker'); } };",
      },
      globalOutbound: null,
    }));
    return worker.getEntrypoint().fetch(request);
  },
};
```

Use `get(id, callback)` to reuse a child by ID while it stays warm. Choose a new
ID when you change its code or cached environment. The cache is scoped to the
parent execution snapshot and binding. A parent update starts a new cache scope
while peer Workers can continue serving traffic in the same workerd process.

Use `load(code)` for a fresh child. A stub can serve multiple fetches against the
same fresh child within the parent invocation. Both methods support default and
named entrypoint fetch, JSON `props`, and JSON values in `WorkerCode.env`.

## Pilot limits

WebAssembly and Python modules, RPC methods on child entrypoints, Durable Object
entrypoints, and capability bindings in child `env` are outside this pilot.
Child outbound is disabled even when `globalOutbound` is omitted. Explicit
`globalOutbound: null` makes this behavior portable to Cloudflare. Use the
[Cloudflare Worker Loader API reference](https://developers.cloudflare.com/dynamic-workers/api-reference/)
for the hosted API.

A deployment can declare up to eight loader bindings. Each parent revision
admits up to 128 distinct binding and cache ID pairs, with IDs up to 256 UTF-8
bytes. Each parent invocation can make up to 32 bridge operations. A fresh stub
uses one operation when it first creates a child and one per fetch. The code
descriptor is limited to 512 KiB, entrypoint options to 16 KiB, and child fetches
receive a 30-second abort signal. The shared runner does not enforce
independent Worker CPU interruption. Public uploads require the sandboxing
described in the [integration plan](../plans/dynamic-worker-loader.md#public-hosting-gate).
