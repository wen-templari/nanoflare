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

The probe embeds its own dispatcher and publication pointer. Its JavaScript
stub defers loading until default-entrypoint fetch and forces child outbound
off. Production request admission, resource bindings, publication storage,
runtime limits, and broader Worker Loader API compatibility remain to be tested.

## Implementation status

The trusted pilot now has config parsing, native and Cloudflare-compatible upload
metadata, immutable binding declarations, a scoped dispatcher bridge, and fetch
entrypoints. The [operator and developer guide](../guides/dynamic-workers.md)
lists the supported syntax, defaults and limits. Native tests run embedded and
remote runners. Full Cloudflare API and public tenant qualification remain
follow-up work.

## Implementation and qualification

1. **Persist the binding declaration.** Add an optional binding list to deployment
   input and immutable artifacts. Parse the Wrangler field in the CLI. Validate
   duplicate and reserved names across all binding types. Preserve declarations
   through vars and secret edits, rollback, and weighted deployments. Read old
   artifacts as having an empty list. Reject declarations explicitly where the
   selected runtime or deployment path cannot serve them. Keep the feature off by
   default.
2. **Scope the bridge to declared bindings.** Configure a dedicated native
   loader next to the publication loader in the dispatcher. Attach a bridge
   with a server-assigned execution identity when building the parent's
   environment. Verify the binding declaration at admission. Key cached children
   by the tuple of parent execution identity, binding name, and caller ID. Use the same scope for all invocations
   of that snapshot. Verify that the bridge belongs to the admitted parent and
   avoid using caller-supplied routing or identity headers as authority.
3. **Implement the fetch API and qualify its semantics.** Start with JavaScript
   modules and fetch on default and named entrypoints for both `get()` and
   `load()`. The native test covers concurrent cache misses and repeated fetches
   against a fresh stub within one invocation. Qualify callback timing and
   errors, streaming, cancellation, and client disconnects on the pinned engine.
   Defer RPC on child entrypoints and arbitrary capability values until
   independently qualified.
4. **Tie execution to admission and bound usage.** Associate child dispatch with
   a live parent invocation and its deadlines. Keep child code and data separate
   from the parent's platform bindings and credentials. Start with outbound off.
   Cap descriptor bytes, concurrent child calls, and admitted cache-key
   cardinality. Require callers to avoid request-specific secrets or authority in
   cached code descriptors. Test revision retirement and resource retention with
   active children. Measure workerd memory across child-cache churn and verify
   what can be reclaimed without recycling the whole process.
5. **Wire up end-to-end coverage.** Run the standalone probe and native
   integration tests with the pinned workerd in Linux CI. Exercise Wrangler
   config through the normal deployment path, both embedded and remote runners,
   binding opt-in and rejection, old artifacts, weighted revisions, peer
   continuity, and old admitted requests completing after an update. Inject
   publication failure, session loss, and runner crash. Document the exact
   supported API and operational limits for a trusted pilot.

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
