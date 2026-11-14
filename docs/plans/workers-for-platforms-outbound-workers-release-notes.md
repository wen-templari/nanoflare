# Outbound Workers release notes draft

Status: unreleased, 2026-11-14. Publish through Release Please after the
[completion audit](workers-for-platforms-outbound-workers-audit.md) and required
checks are complete. This draft does not establish a release version or claim
hosted Cloudflare parity.

## Changes

Dispatchers can attach an outbound Worker to an individual namespace binding and
pass declared context with the third argument to `namespace.get()`. Customer HTTP
requests pass through that Worker for filtering, request changes, forwarding or
logging. Customer TCP connections are denied when the binding has an outbound
policy. Bindings without a policy retain their existing behavior.

Outbound targets use ordinary Worker execution with their own variables, frozen
secret versions, storage bindings, service fetch/RPC bindings, explicit assets,
supported dispatch bindings and executor networking/DNS configuration. Native
and Node HTTP interception have dedicated engine qualification; production
coverage is tracked separately in the audit. Targets remain ordinary modules
Workers in the same organization, identified by immutable IDs. Named fetch
entrypoints are available through native configuration and compatible upload
metadata.

The console can edit an existing dispatch binding's outbound target, named
entrypoint and parameter declarations without uploading code again. Publishing
creates a retained revision that preserves source, assets, variables, secret
references and other settings. It requires `deployments:write` and
`namespaces:dispatch`, plus a sole active revision receiving 100% of traffic.
Expected-revision checks reject stale edits; an identical idempotent retry returns
the original result without reactivating it. Confirmed rollout failure restores
prior traffic. The existing full-bundle workflow remains available.

Namespace pages now list dispatchers with active bindings to the namespace,
including revision traffic and links to outbound targets. Inspecting references
requires `workers:read`. Generated API types and the project configuration schema
include outbound configuration.

## Qualification and compatibility

The pinned local runtime is workerd npm `1.20260911.1` (engine `2026-09-11`).
Official-client probes use Wrangler `4.54.0` and Cloudflare TypeScript SDK `7.3.0`.
Offline schema/serialization probes passed for both clients; this does not imply
that every deployment and runtime workflow passes. Check the final versioned
qualification report and audit before publishing this draft.

Wrangler 4.54.0 exposes `service`, `environment` and `parameters` in outbound
configuration; its schema does not expose the named `entrypoint` field. Native
configuration and compatible upload metadata expose that selector. Named
service environments select independently deployed code and secrets through a
persisted relationship; no suffix inference or silent production fallback occurs.

Console qualification includes retained-publication race tests and authenticated
endpoint tests, plus browser checks at 1440 × 1000 and 390 × 844. Browser checks
covered namespace references, direct editing, a rejected duplicate parameter,
corrected publication and refreshed readback. TypeScript and focused lint passed.

## Remaining limitations

- No WFP-entitled Cloudflare account was available. Hosted comparisons, the WFP
  dashboard and undocumented lifecycle/parameter semantics remain unverified.
- Parameters use a finite JSON snapshot with explicit executor bounds: 16 KiB,
  depth 64 and 64 declared names. Reserved names and binding collisions are
  rejected; undeclared values are rejected and missing declared values read as
  `undefined`. These are local contracts, not established Cloudflare guarantees.
- Targets and secret versions are pinned at admission. Hosted update timing,
  collision precedence, non-JSON values and nested-call behavior require comparison.
- Request-bound capabilities use anonymous descriptors. Immutable sources are
  cached, but warm-isolate reuse and its performance are not promised.
- Missing underlying Worker capabilities, CPU interruption
  and untrusted isolation remain separate platform dependencies. Durable Object
  and mTLS interception exclusions stay explicit until those capabilities exist.
- Local lifecycle, persistence and client-flow qualification is recorded in the
  versioned report and audit. Hosted timing and side-effect comparisons remain
  unperformed.

See the [compatibility contract](../reference/outbound-workers-compatibility.md)
and [example](../../examples/outbound-workers/README.md) for configuration and
precise evidence boundaries. Do not manually change shared package versions or
create replacement release tags.
