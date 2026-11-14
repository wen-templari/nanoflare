# Outbound Workers: Cloudflare alignment plan

Updated 2026-11-12. This supersedes the original subset-first plan. The initial
implementation remains in the working tree; this revision changes the roadmap,
not the runtime. The objective is the closest practical Cloudflare feature and
application compatibility, with explicit evidence for differences and unknowns.

## Execution checkpoint — 2026-11-13

The goal is active. The compatibility contract and executable manifest exist in
`docs/reference/outbound-workers-compatibility.md` and
`scripts/wfp-client-flows/outbound/`. Their initial-baseline entries are being
updated as production coverage is established; they are not hosted parity claims.

Current verified progress:

- Pinned workerd 1.20260911.1 engine qualification passes native global fetch and
  Node HTTP interception, binary POST/authorization preservation, cross-origin
  buffered-body 302 method conversion and credential stripping, customer TCP
  denial, concurrent policy parameters and unrestricted descriptor reuse on both
  static and loader-hosted paths (`TestWorkerdOutboundNativeHTTP`). A streamed
  request body cannot be assumed replayable across redirects; the redirect probe
  deliberately uses a buffer-like initializer.
- Native/compatible configuration preserves named entrypoint selectors, including
  keep/inherit translation. Target selector changes affect frozen execution
  identity. Focused Go tests pass in nanoflare, cfcompat and CLI packages.
- The ordinary-target and native-interception production path passes the existing
  four-way static/dynamic and local/remote outbound smoke matrix. Broader ordinary
  capabilities, lifecycle and authority qualification remain in progress. The
  extended race matrix additionally passes target-owned KV/secrets, service
  fetch/RPC with separate helper secret authority, a named fetch export with a
  throwing default, and target-owned nested dispatch.
- Direct retained-revision publication passes service and endpoint race checks
  for exact scopes, stale base, split traffic, retry/key conflicts, concurrent
  edits, frozen secret/configuration preservation and failed-rollout restoration.
  UI type checking and focused lint pass; visual verification is pending.
- Repository-wide `go test ./...` passes without native test environment variables.
  Native, client and PostgreSQL prerequisites skipped by that command are not
  implied to have passed.

Request-bound network and target capabilities currently require anonymous loader
descriptors. Immutable source may be reused, but this is not warm-isolate reuse;
performance qualification remains required. Direct retained-revision editing is
implemented in the worktree and still undergoing endpoint/UI verification.
Outbound target DNS profiles now pass the production and race matrix, with the
profile carried only in trusted runtime context. Non-production service environments,
broader storage/lifecycle qualification and optional hosted comparisons remain open. No WFP-entitled account is available.

Latest persistence gate: the disposable PostgreSQL 16.9 repository suite passes
with race detection. The outbound close/reopen recovery test now passes on both memory and PostgreSQL
with race detection. It verifies the hydrated execution boundary: dispatch metadata
is stored in immutable artifacts rather than legacy deployment rows. An initial
raw-row assertion was corrected; no production persistence change was required.
The production HTTP four-way race suite now passes streamed binary POST echo,
302 POST-to-GET conversion, 307 binary-body replay and cross-origin authorization
stripping. Fixes keep policy responses manual until the customer follows each
redirect, and avoid forwarding a body for GET/HEAD.

## Final implementation checkpoint — 2026-11-14

The implementation now includes real service environments, immutable production
service identities, environment deployment/readback/secret routes, CLI resolution,
canonical selector preservation, and memory/PostgreSQL runtime and recovery
qualification. This supersedes the earlier dated environment-gap notes above.
The [completion audit](workers-for-platforms-outbound-workers-audit.md) maps the
six phases to current implementation and qualification evidence, and distinguishes
remaining hosted comparison and broader platform dependencies from local work.
The final machine-readable report is
`scripts/wfp-client-flows/outbound/reports/local-2026-11-14.json`.
Changes remain unreleased; shared versions and tags are unchanged.

## Success criteria

An application using documented outbound configuration should run on Nanoflare
with organization/credentials and deployment endpoint changes. Outbound execution
should preserve the ordinary Worker capabilities Nanoflare already implements.
Every remaining difference must be identified as a missing platform capability,
a deployment policy, or behavior requiring comparison. Local tests establish local
conformance; they do not establish hosted Cloudflare parity.

The scope includes configuration, request interception, target execution,
parameters, lifecycle, revision selection, errors and developer tooling. Preserve
existing behavior for bindings without outbound and for existing ordinary Workers.

## Evidence and current baseline

The [Cloudflare outbound guide](https://developers.cloudflare.com/cloudflare-for-platforms/workers-for-platforms/configuration/outbound-workers/)
defines binding-local outbound selection, dispatcher parameters, customer TCP
restriction and the Durable Object/mTLS interception exclusions.
The [upload API](https://developers.cloudflare.com/api/resources/workers/subresources/scripts/methods/update/)
exposes outbound service, entrypoint and environment selectors and named parameters.
These are the primary contract references; absent documentation is not evidence
that every Worker capability or lifecycle detail is supported.

The current implementation covers attachment, fetch interception through a JS
adapter, JSON parameters, immutable target IDs, target revision/secret pinning,
brokered forwarding, native-network denial, CLI/API formats and bundle-based
console editing. It was qualified locally with workerd 2026-09-11 and Wrangler
4.54.0, including concurrent static/dynamic dispatch through local/remote runners.

Known additional restrictions: outbound targets reject service/dispatch bindings
and DNS profiles; only default entrypoints and production selection are accepted;
native HTTP is denied rather than intercepted; outbound targets also lose native
networking. Parameter serialization, collisions, missing/extra values, 16 KiB/64
limits, pinning and nested-call propagation include Nanoflare-specific decisions.
The target capability restriction comes from reusing the restricted customer
adapter rather than the ordinary execution pipeline.

No WFP-entitled account is available. Do not make that a blocker for implementation,
request credentials repeatedly, or enable billing. No WFP dashboard has been
verified. Cloudflare custom DNS profiles have not been established as an equivalent
feature; preserving ours is an internal ordinary-Worker consistency requirement.

## Phase 1 — Establish a versioned compatibility contract

Create `docs/reference/outbound-workers-compatibility.md` and an executable case
manifest under the existing WFP client/qualification fixtures. Each case records
its source/version, expected behavior, Nanoflare result and evidence level:
`documented`, `upstream-client-observed`, `locally-qualified`, or `hosted-unverified`.

Inspect pinned official Wrangler/SDK schemas and serialization, relevant workerd
interfaces and official examples. Use Miniflare only for behaviors its pinned
version actually implements; an emulator result is not hosted evidence.

Include cases for configuration/metadata, default/named exports, environment
selection, parameter transport, native and JS HTTP, TCP, redirects, nested calls,
exceptions, lifetime and deployment updates. Parameter cases must cover nested
objects, scalars, arrays, missing/extra names, collisions, mutation timing, sizes,
non-JSON values and imported-env access. Record the guide's inconsistent parameter
sample as an unknown; do not infer flattening from it.

Exit: every currently imposed restriction has an evidence-backed disposition:
remove it, retain it as documented deployment policy, or track it as unverified.
Unknown behavior gets an isolated comparison test and an explicit interim contract.

## Phase 2 — Execute outbound targets as ordinary Workers

Replace the outbound target's use of `DynamicEntrypoint`/the private customer
resource facade with the existing ordinary compilation, execution snapshot and
invocation machinery. Start with `SnapshotWorkerCode`, `InstrumentLoaderInvocation`,
ordinary service admission and the static parent bridges in `dynamic_dispatcher.go`.
Refactor shared execution helpers instead of building another ordinary runtime.

Preserve the outbound target's own variables, secret versions, storage bindings,
service fetch/RPC bindings, named exports, supported dispatch bindings, explicit
assets, logging and executor DNS/egress configuration. Preserve ordinary compatibility
flags and APIs; do not force the private customer's Cache/native-network profile
onto outbound code. The intercepted request must invoke the selected fetch handler,
while explicit asset bindings remain usable.

Add a trusted outbound admission relation: the customer authorizes invocation of
its configured platform-owned target, while target code uses only its own resource
and service authority. Preserve root correlation/cancellation and bounded admission
without granting customer code access to target bindings. Never put per-request
parameters, credentials or live channels in immutable code-cache state.

Replace blanket target rejection with exact capability validation. Support services
first, then the target's own dispatch bindings with correct request-root handling.
Detect recursive invocation through bounded chain/depth tracking. Restore the
outbound target's ordinary networking under the executor's existing policy; keep
the customer's TCP restriction separate. Preserve supported Nanoflare DNS behavior
without describing it as a Cloudflare outbound contract.

Exit: ordinary and outbound invocation of the same Worker have equivalent supported
capabilities, except for parameter overlay and outbound invocation semantics.
Prove this on both host execution paths and local/remote runners before removing
eligibility checks or advertising broader support.

## Phase 3 — Intercept native HTTP and preserve network semantics

Implement an engine-level customer `globalOutbound` bridge that invokes the selected
outbound target for HTTP and denies customer TCP. Begin with a pinned-workerd proof
covering cached customer revisions used concurrently with different policies and
with no policy. Use the static parent to create request-bound capabilities; do not
transfer dynamic loader entrypoints or the loader itself across Worker boundaries.

If engine constraints require descriptor variants, key them by immutable effective
configuration. Do not cache request-bound channels or use an isolate-global mutable
"current customer". Native HTTP must reach policy rather than simply fail. Keep
normal fetch and native forwarding on one accounting/routing path to avoid invoking
policy twice. The outbound target's forwarding must not recursively re-enter its
own hook.

Qualify headers, authorization stripping, binary/streamed bodies, AbortSignal,
redirect method/body handling and cross-origin credentials. Test relevant supported
Node HTTP clients and native APIs instead of assuming global fetch covers them.
Check service-call propagation and exclusion boundaries against phase 1 evidence.
Durable Object and mTLS exclusions must remain explicit when those platform
capabilities are implemented.

Exit: every supported customer HTTP path has the defined interception behavior,
customer TCP remains denied with administrator sockets enabled, and the target
retains ordinary executor capabilities. Keep the current deny behavior until this
replacement passes; do not permit a bypass to gain HTTP compatibility.

## Phase 4 — Complete selectors and parameter compatibility

Add named fetch entrypoint selection throughout the native model, publication,
loader instrumentation, CLI, multipart adapter, settings/readback, inheritance and
cache/execution identity. Validate export existence and eligibility. Preserve the
target's bindings and per-invocation parameters at the selected entrypoint.

Inspect the exact environment behavior of pinned upstream clients. Determine
whether each form selects a service environment or an independently deployed
Worker name. Implement the actual selection/identity model required by that form.
This may require a broader platform environment feature. Never accept a selector
and silently invoke production, and do not invent aliases solely to satisfy parsing.

Apply phase 1 findings to parameter serialization, collision precedence,
missing/extra names, snapshot timing and limits. Remove arbitrary incompatibilities
where evidence supports it. Retain necessary resource bounds as explicit executor
policy until matching limits are established; do not present 16 KiB/64 names or
JSON-only transport as Cloudflare guarantees. Keep RPC-capable `props` separate
unless upstream evidence establishes outbound capability transport.

Exit: selectors survive all read/write/keep/inherit/rollback operations, invalid
selectors fail clearly, and every parameter case is classified and tested.

## Phase 5 — Qualify lifecycle, persistence and deployment behavior

Exercise the completed production path, not only an engine fixture:

- Policy blocks/exceptions, streaming and binary uploads/downloads, redirect chains,
  cancellation/disconnects, background work and exhausted shared budgets.
- Service/RPC and nested dispatch paths; own KV/D1/R2/assets and secret access;
  isolation of request parameters under concurrent warm-revision reuse.
- Missing/revoked targets, rename/delete/recreate, host rollback, target weighted
  traffic and code/secret updates during a request. Revisit admission-time pinning
  if upstream evidence demonstrates different selection semantics.
- Memory and PostgreSQL repositories, daemon/runner restart, recoverable publication
  failures and retained-binding recovery. Run local/remote and static/dynamic matrices.

Run real pinned Wrangler and SDK deployment/readback/invocation flows. Give the
case manifest a second backend for hosted Cloudflare, usable when entitlement is
available. The same cases must compare statuses, results, side effects and lifetimes;
normalize volatile IDs/timing without hiding behavioral differences. Preserve a
machine-readable report with versions, passed/failed/skipped cases and limitations.

Exit: local contract cases pass with race checks and persistence qualification;
hosted comparisons are independently reported rather than required to unblock all
local progress. Fix semantic differences before widening compatibility claims.

## Phase 6 — Finish console workflow and release

Add editing of an existing host revision's outbound bindings without requiring a
bundle upload. The server must derive a complete retained revision and use the
existing publication flow with expected-revision/traffic checks and idempotency.
Preserve code, assets, bindings and secret references; a retry must not republish a
stale snapshot. Add/remove/select target, entrypoint and supported environment,
parameter editing, eligibility feedback and namespace-to-dispatcher references.

Keep the current full-bundle workflow. Apply Kumo conventions and existing scopes.
Distinguish feature availability, supported selectors and binding-specific behavior
in capability responses and the UI. Check narrow/wide layouts and failure states.
This is our console design; do not claim it reproduces an uninspected Cloudflare UI.

Update native/compatible API references, generated schemas/types, configuration
schema, examples and release notes. State tested upstream client/runtime versions
and remaining differences. Release through Conventional Commits and Release Please;
do not manually change shared versions or create replacement tags.

## Delivery order and broader WFP dependencies

Implement phases 1–3 first: contract evidence, ordinary target execution and native
HTTP interception remove the central architectural gaps. Add selectors and refine
parameters next, then complete qualification and console authoring. Add targeted
acceptance tests with each phase rather than postponing all testing to phase 5.

Named environments, missing ordinary binding kinds and engine APIs can require
platform work. CPU interruption/accounting and untrusted isolation remain explicit
broader WFP dependencies, with their own workstreams. Cloudflare documents
[custom CPU/subrequest limits](https://developers.cloudflare.com/cloudflare-for-platforms/workers-for-platforms/configuration/custom-limits/);
do not claim equivalent enforcement from wall deadlines or effect counters.

Completion means the supported Cloudflare outbound contract works across Nanoflare's
existing ordinary-Worker capabilities, local qualification passes, and every
remaining platform or hosted-verification gap is stated precisely. Exact Cloudflare
parity remains unclaimed until corresponding hosted behavior is compared.

## Descriptor measurement scope

The engine fixture optionally accepts a third argument naming a JSON measurement
file. The recorded development sample is
`scripts/wfp-client-flows/outbound/reports/engine-measurements-2026-11-13.json`.
It measures the first policy HTTP invocation and an overlapping batch of 24 on
static and loader-hosted paths, including the fixture's deliberate 5 ms delay.
It is not a production latency estimate, load qualification or evidence of warm
isolate reuse. Reproduce with:

```sh
node internal/config/testdata/outbound-native-http.mjs \
  /absolute/path/to/pinned/workerd /tmp/outbound-engine-measurements.json
```

## Lifetime checkpoint — 2026-11-14

The production matrix now passes `waitUntil()` effects after 2.3 seconds on all
four host/runner paths with race detection. Started ordinary descendants retain
bounded root/ancestor leases. Child completion is explicitly awaited by the parent
RPC event and, on loader-hosted dispatchers, by source invocation accounting.
Request-local completion coordinators are bounded to 1,024 entries and 30 seconds;
server cancellation, effect budgets and the original root deadline remain
independently authoritative. This corrects the former two-second truncation;
hosted Cloudflare lifetime equivalence is still unverified.
