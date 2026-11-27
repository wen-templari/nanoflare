# Worker Loader bindings for deployed Workers

## Decision

Add an opt-in Worker Loader binding to ordinary deployed Workers. A Worker
declares `worker_loaders = [{ binding = "LOADER" }]` in Wrangler configuration
and uses `env.LOADER.get(id, callback)` or `env.LOADER.load(code)` to instantiate
code at runtime. Workers for Platforms namespace dispatch has its own API and
publication lifecycle.

Keep the existing shared workerd hot-update path. The pinned workerd rejects
serializing a native `WorkerLoader` into a loaded Worker's environment. Declare a
second native loader on the static dispatcher for application-created children.
Pass each opted-in parent a scoped RPC bridge, and construct the application-facing
loader in the parent's binding adapter. Use the native engine for child isolate
creation and caching.

## Proof of concept

Run the [standalone probe](../../scripts/dynamic-worker-bridge-poc.mjs)
with Node.js 22 or later and the project's pinned workerd `1.20260911.1`.

```sh
NANOFLARE_TEST_WORKERD=/path/to/workerd node scripts/dynamic-worker-bridge-poc.mjs
```

The probe starts one workerd on an ephemeral loopback port and shuts it down on
exit. It verified these behaviors on macOS ARM64 with workerd `2026-09-11`.

- Two fetches using the same `get()` key advanced the child's counter from one
  to two and ran the parent's code callback once.
- The same caller ID in a different parent or binding created a separate child.
- Two `load()` calls each created a fresh child.
- A simulated change to Alpha's active revision started a fresh Alpha child
  while Beta kept its existing child and counter in the same process.

The probe embeds its own dispatcher and publication pointer. The Nanoflare
integration tests also exercise the gateway, execution broker, and runner.

## Stage 1 — Trusted Kitesurf pilot

The opt-in pilot supports Wrangler declarations, native and Cloudflare-compatible
upload metadata, and revision-scoped admission. It supports fresh and cached
children, named entrypoint fetch and RPC, disposable RPC sessions, inline WASM,
and WASM imported from the parent deployment. Fresh children can use scoped
outbound and tail entrypoints. Nanoflare enforces the child's outbound subrequest
budget for the life of that child. The
[Dynamic Workers guide](../guides/dynamic-workers.md) documents the API and
limits.

On macOS ARM64 with workerd `1.20260911.1`, the
[loader integration test](../../internal/api/workerd_application_loader_e2e_test.go)
passed in embedded and remote mode. The
[Kitesurf smoke test](../../internal/api/workerd_kitesurf_e2e_test.go) deployed the
upstream Docker bundle with an entrypoint adapter to an embedded Nanoflare
runner. CDP navigated to controlled HTML and read back the expected page title.
The smoke test used image digest
`sha256:076e1614b048b00620a3bf1eadc5515de2e49dc9a92b0a6c52fe53fdaf706a8a`.

## Stage 2 — Runtime and deployment qualification

- **CPU enforcement.** The pinned standalone workerd accepts `cpuMs`. The
  opt-in CPU qualification showed that JavaScript and WASM exceeded the requested
  budget and completed with outcome `ok`. An experimental watchdog interrupted
  both workloads in a standalone V8 test and allowed a subsequent call. The
  experimental workerd build and Nanoflare qualification are pending. Qualify
  measured CPU interruption and `exceededCpu` reporting in both runner modes,
  including I/O waits, background work, nested RPC, concurrent invocations, and
  recovery. Follow the [CPU enforcement plan](workers-for-platforms-cpu-enforcement.md).
- **Deployment compatibility.** Qualify the upstream bundle on Linux and through
  a remote runner. Exercise the Wrangler deployment path and its upload limits
  with the full bundle. The current smoke test uses the native deployment service,
  higher test-specific upload limits, and an adapter for the bundle's aliased
  class entrypoint and text modules. Document a repeatable deployment workflow.
- **Loader compatibility and recovery.** Qualify streaming, cancellation, client
  disconnects, child retirement, runner restart and session loss, and memory
  reclamation during cache churn. Extend coverage for weighted deployments,
  rollback, and older artifacts. Review API gaps such as Durable Object
  entrypoints and capability bindings in child `env` against workloads that
  need them.

## Public hosting gate

Pilot the bridge with trusted deployed code. For public user uploads, place
workerd runners inside hardened tenant sandboxes and keep the gateway and
control plane outside them. Route all access to shared resources through scoped
bindings. Start with one sandbox per tenant so that each runner can still share
workerd across that tenant's Workers. Validate containment, runner-level
CPU/memory limits, and independent tenant failure recovery before enabling public
uploads. Shared workerd currently has no per-Worker CPU interruption guarantee.
Workloads that require independent termination need a dedicated sandbox or a
qualified per-Worker interruption mechanism.

- [Dispatcher and execution loader](../../internal/config/dynamic_dispatcher.go)
- [Nanoflare runtime manager](../../internal/runtime/dynamic_manager.go)
- [Cloudflare Worker Loader API](https://developers.cloudflare.com/dynamic-workers/api-reference/)
- [workerd sandbox guidance](https://github.com/cloudflare/workerd#warning-workerd-is-not-a-hardened-sandbox)
