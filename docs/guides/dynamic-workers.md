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
named entrypoint fetch and RPC method calls, JSON `props`, and JSON values in
`WorkerCode.env`. Returned RPC targets use workerd's disposal semantics. A child
that retains an RPC callback beyond the receiving call must duplicate the
callback stub with `dup()` and dispose it when finished.

## Pilot limits

WebAssembly modules accept `{ wasm: value }`, where `value` is an `ArrayBuffer`,
an `ArrayBuffer` view, or a compiled `WebAssembly.Module` imported from the
parent's published revision. Inline bytes count toward the descriptor limit
after base64 encoding. Published modules are resolved from the parent revision.
Python modules, Durable Object entrypoints, and capability bindings in child
`env` are outside this pilot.
Omitting `globalOutbound` disables child outbound. Explicit `globalOutbound:
null` also disables it on Cloudflare. A fresh child can use a `ctx.exports`
entrypoint from the same parent invocation as `globalOutbound`. It can also
declare up to eight `ctx.exports` tail entrypoints to receive traces. These
entrypoints remain tied to the parent revision and invocation. Use the
[Cloudflare Worker Loader API reference](https://developers.cloudflare.com/dynamic-workers/api-reference/)
for the hosted API.

A deployment can declare up to eight loader bindings. Each parent revision
admits up to 128 distinct binding and cache ID pairs, with IDs up to 256 UTF-8
bytes. Each parent invocation can make up to 32 bridge operations. A fresh stub
uses one operation when it first creates a child and one per fetch or direct
entrypoint RPC call. Cached code descriptors are limited to 512 KiB and fresh
descriptors to 8 MiB. Entrypoint options are limited to 16 KiB. Child fetches
receive a 30-second abort signal.

`limits.subRequests` caps the number of requests sent through a fresh child's
outbound binding over that child's lifetime. Nanoflare forwards `limits.cpuMs`
to `workerd`, whose standalone runtime currently lacks CPU enforcement for
individual children. Public uploads require the sandboxing described in the
[integration plan](../plans/dynamic-worker-loader.md#public-hosting-gate).

To qualify another `workerd` build, run the loader test with
`NANOFLARE_TEST_WORKERD_CPU_LIMITS=1`. It expects CPU-bound JavaScript and WASM
to stop with the `exceededCpu` tail outcome and a timer longer than the CPU
budget to complete normally. The pinned standalone build fails this check.

## Run the Kitesurf navigation smoke test

Extract `/opt/kitesurf` from the upstream Kitesurf Docker image. The test uses
Node.js 22 or later, deploys the extracted bundle to the local Nanoflare
runtime, and checks the title of a controlled page over CDP.

```sh
NANOFLARE_TEST_KITESURF_ARTIFACT=/path/to/extracted/kitesurf \
NANOFLARE_TEST_WORKERD=/path/to/pinned/workerd \
  go test -v ./internal/api -run '^TestWorkerdKitesurfNavigation$'
```
