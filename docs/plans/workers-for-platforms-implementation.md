# Workers for Platforms implementation plan

Updated: 2026-10-22. The first supported native contract is frozen. Steps 2–8 are implemented for the documented disabled-by-default trusted subset, with step 3 production protection gates still open. Step 9 has local adapter evidence but remains incomplete without live Cloudflare comparison; no entitled account is available. Step 10 bounded local trusted-pilot qualification and the requested independent review are complete, including fixes for all three review findings. CPU enforcement is a separate pending workstream. Production expansion remains unqualified. This document does not authorize or represent production deployment.

## Outcome and release boundaries

Nanoflare should let a platform deploy private customer Workers, invoke them by namespace and script name, grant specific resources, and apply platform-controlled ingress and outbound policy.

Deliver two separately qualified releases:

- **Trusted platform MVP:** platform-controlled code, real namespace authorization, private dispatch, explicit bindings, deployment lifecycle, and documented limits. Unsupported protections are rejected explicitly.
- **Untrusted customer-code release:** the MVP plus a validated sandbox/failure boundary, execution budgets, resource/cache isolation, enforced egress policy, and trustworthy telemetry. This release is blocked until its safety gates pass.

Cloudflare API compatibility is an additional adapter over this model. It is not a prerequisite for the native MVP and must not imply complete runtime or security equivalence.

## Current position

The [PoC](../../scripts/wfp-poc/README.md) and [research](../research/workers-for-platforms-alignment.md) established local feasibility. Step 2 is now implemented in the working tree and verified against in-memory and real PostgreSQL repositories. See the [step 2 implementation and verification record](workers-for-platforms-step-2-status.md), including rollout requirements. Step 4 adds organization-scoped namespaces, immutable private script membership, customer/resource grants and policy history in both repositories; see its [status record](workers-for-platforms-step-4-status.md). Step 5 adds immutable revision artifacts, authorized manifest resolution, staged publication/recovery and pinned secret rollback; see its [status record](workers-for-platforms-step-5-status.md). Steps 6–7 add native loader-backed private dispatch, invocation scopes and the documented outbound/cache/resource policy. Nothing has been committed or deployed.

Verified on workerd 2025-12-10 and 2026-10-18: namespace lookup, private user Workers, native props and request-scoped RPC, explicit capability attachment, and customer source/revision updates without dispatcher restart. The experimental Worker Loader is a viable implementation candidate. After the authorization fix, the native telemetry forgery and cross-handler KV probes are rejected on both versions.

Remaining release gates: real CPU interruption and hardened untrusted cells are unavailable; an infinite loop can affect peers in the trusted shared pool. Capacity, production-like storage/recovery and rollout qualification remain. Subrequest budgets and proxy-independent destination authorization now exist in the native path; they are no longer outstanding PoC gaps. Live Cloudflare comparison is blocked by the user's account entitlement. At the user's request, [real CPU enforcement](workers-for-platforms-cpu-enforcement.md) remains separate; untrusted execution is unavailable.

## Step 1 — Freeze the first supported contract

**Status: native contract frozen.** See the [supported contract and capability matrix](../architecture/workers-for-platforms-supported-contract.md). Step 8 exposes these effective capabilities through supported tools. The checklist below defines this adopted scope.

Write a short architecture decision covering:

- Organization as platform owner, with an optional customer identity inside it when one platform hosts multiple customers.
- One logical namespace per environment within a platform, distinct from customer authorization and physical runner placement.
- Stable Worker IDs and unique script names within each namespace.
- Platform Workers versus customer Workers; customer Workers default to private dispatch-only exposure.
- Supported runtime/version/experimental features and explicit trusted/untrusted execution profiles.
- Native features to retain, including immutable revisions and weighted traffic.

Choose the experimental loader as the initial dispatch candidate, subject to step 3. Do not commit to per-request outbound isolate creation as the production reuse strategy.

**Done when:** there is one documented supported contract, a compatibility matrix, and a list of rejected/deferred features. A caller cannot request an unavailable isolation profile or limit and silently receive weaker behavior.

## Step 2 — Fix runtime capability authorization

**Status: implemented and verified locally, 2026-10-18.** See the [status record](workers-for-platforms-step-2-status.md) for the credential contract, lifecycle semantics, tests and rollout caveats. The checklist below describes the completed scope.

Address the existing boundary gaps before introducing more runtime capabilities:

1. Restrict telemetry connections to telemetry operations; reject attempts to use them for KV, database, assets, or object handlers.
2. Require valid runtime caller authentication on every private handler. Keep administrative storage operations separate from Worker-facing operations; empty credentials must not become administrative authority at an HTTP boundary.
3. Validate organization, Worker, exact deployment, resource and allowed operation for each capability. Preserve old deployment grants only for the defined in-flight/draining lifetime; revocation must have explicit semantics.
4. Derive telemetry producer identity from a trusted connection/runtime context rather than an event field or console-text marker.
5. Exercise both the in-memory and PostgreSQL-backed paths. Test token/header substitution, forged IDs, unbound resources and retired revisions.

**Done when:** the PoC's forged identity and cross-handler KV scenarios are rejected, legitimate binding operations still work, and regression tests require the corrected behavior. Preserve the original PoC results as historical evidence rather than overwriting them with the fixed system's results.

## Step 3 — Resolve execution budgets and sandbox architecture

**Status: design and bounded feasibility complete for the restricted subset; production protection gates remain open.** See the [step 3 evidence and decisions](workers-for-platforms-step-3-status.md). The [execution decision](../architecture/workers-for-platforms-execution.md) fixes cell boundaries, typed capability transport, warm reuse and invocation lifecycle. Next is step 4 for the trusted-only model. Real CPU enforcement is split into a [separate workstream](workers-for-platforms-cpu-enforcement.md), per the user's request. Continue independent trusted-only model/dispatch work without advertising CPU guarantees.

Run a bounded runtime/runner experiment before investing in the full untrusted product surface:

- Prove actual CPU interruption independently from HTTP timeouts. Measure CPU separately from elapsed duration.
- Define and enforce subrequest accounting, including redirects, retries, service/resource calls and permitted background work.
- Choose sandbox/failure boundaries for customers and privileged platform code. A shared sandbox around all customers is a different guarantee from isolating them from one another.
- Test infinite loops, memory pressure, runner termination/restart, filesystem and management-endpoint access, and peer availability.
- Determine how runtime capabilities cross the chosen boundary, including RPC, stream lifetime, cancellation and `waitUntil()`.
- Pin the runtime and produce an explicit supported-capabilities record. The newer binary alone did not resolve the tested CPU/subrequest gaps.

If this fails, record the missing runtime/runner work and keep untrusted execution unavailable. A trusted MVP can proceed with accurately stated restrictions. Process quotas or killing a shared pool must not be described as per-invocation limits with peer isolation.

**Done when:** there is an adopted execution design and repeatable evidence for every advertised protection. If no design passes, the untrusted release remains blocked; a trusted-only implementation has a clear boundary.

## Step 4 — Add the production resource model and authorization

**Status: implemented and verified locally.** See the [step 4 resource/API/migration record](workers-for-platforms-step-4-status.md). The model supports trusted private scripts; dynamic dispatch, cell execution and Cloudflare compatibility remain later steps. Step 5 now supplies immutable artifacts and recoverable publication; see its status record below.

Add database records, repository methods, migrations and native API schemas for namespaces, namespace membership, script names, Worker kind/exposure, resource grants and policy revisions.

- Scope namespace names to organizations and script names to namespaces; use immutable IDs internally.
- Authorize namespace-wide dispatch separately from ordinary customer-code deployment.
- Use organization ownership as the customer boundary where appropriate. Add a second tenant identity only for a platform hosting its own customers.
- Keep existing public Workers functional. Do not automatically change their trust or exposure.
- Detect duplicate legacy names during migration; never pick an arbitrary service target.
- Reject namespace deletion while dependencies remain unless an explicit cascade operation is requested.

**Done when:** authorized CRUD works, cross-organization/customer operations fail, migration preserves existing deployments, and private Workers require no hostname.

## Step 5 — Build immutable artifact lookup and publication

Replace the PoC's in-memory source registry with production artifact storage and authorized lookup:

- Publish immutable code/config/binding snapshots identified by deployment ID and content digest.
- Resolve namespace/script identity to an active deployment or native traffic assignment.
- Specify variable and secret update/rollback semantics; preserve secret references without leaking secret values in reads.
- Make publication idempotent, persist outcomes, and distinguish stored, runtime-ready and route-published states.
- Preserve the chosen deployment and its grants for each invocation, including streams and allowed background work.

**Done when:** retries do not duplicate or reactivate revisions, failed publication preserves the previous state, and old/new deployments cannot mix code, assets or resource grants.

**Status: implemented and verified locally for the trusted runtime path.** See the [step 5 artifact/publication record](workers-for-platforms-step-5-status.md), including retry/recovery evidence, secret rollback semantics and migration limitations. Invocation leases and loader-backed dispatch remain step 6.

## Step 6 — Integrate private dynamic dispatch

Integrate the loader-backed namespace facade with the real authorization and artifact model:

- Support `DISPATCHER.get(scriptName, options, dispatchOptions).fetch(request)` for the documented subset.
- Supply native props and request-scoped RPC capabilities; enforce their lifetime.
- Route public traffic through platform dispatch/authentication. Prevent direct private-Worker and asset bypasses.
- Apply a consistent deployment-selection policy to HTTP, dispatch and service calls. Keep native weighted traffic as an extension.
- Drain old invocations during updates. Cover streamed responses, WebSockets, cancellation and runner recovery.
- Keep the dispatcher running when customer code changes, with bounded loading/caching and admission control.

**Done when:** the PoC's successful scenarios pass through Nanoflare's real API and runner, including authenticated deployment, artifact lookup, update and rollback. Fake fixture identities and registry state are no longer involved.

**Status: implemented and verified locally for the documented trusted fetch subset.** Namespace/artifact/native-props integration, exact-target nested service fetches, unchanged-dispatcher updates, streaming, cancellation, capability lifetime and runner recovery pass through local and remote runners on both tested workerd versions. See the [step 6 implementation record](workers-for-platforms-step-6-status.md) for supported operations, rejection contracts and evidence.

## Step 7 — Complete outbound, cache and resource policies

- Separate network authorization from corporate proxy routing. Check destinations after resolution and across redirects, including IPv4/IPv6, loopback, private and link-local ranges.
- Define raw TCP and other transport coverage. Deny capabilities that bypass the selected outbound policy.
- Keep tenant credentials at the outbound service. Attach caller context through trusted runtime capabilities rather than caller-selected headers.
- Resolve request-specific outbound context without relying on an unbounded set of unique loader IDs. Measure reuse and lifecycle behavior.
- Implement Worker-scoped cache policy or explicitly report cache support as unavailable. Define shared-cache behavior for trusted profiles and revision transitions.
- Complete KV/database/object/asset grant tests, including concurrent revisions and deliberate resource sharing.

**Status: implemented and verified locally for the trusted fetch subset.** Authorized HTTP egress is scoped to invocations and pinned before proxy routing; cache and bypass transports have explicit rejection contracts. SQL/file isolation, concurrent revision/resource sharing and revocation tests pass. See the [step 7 record](workers-for-platforms-step-7-status.md).

**Done when:** unauthorized destinations/resources fail closed, authorized traffic works, context cannot cross requests/customers, and cache behavior matches the advertised profile. Required execution limits from step 3 cover these paths.

## Step 8 — Add operational visibility and developer tooling

**Status: implemented and verified locally for the trusted subset.** CLI and console deploy/update/rollback workflows, attributed bounded logs, capability/publication APIs and elapsed metrics pass native, PostgreSQL, browser and race checks. See the [step 8 record](workers-for-platforms-step-8-status.md), [local example](../../scripts/wfp-local-demo/README.md) and [operator notes](workers-for-platforms-operations.md). Production-like telemetry/storage qualification remains step 10.

- Correct the existing elapsed-duration metric currently presented as CPU time. Only expose CPU accounting when measured by trusted infrastructure.
- Scope log and metric queries server-side; add quotas, redaction/sampling controls and request-chain correlation.
- Add namespace/private-Worker/deployment/grant operations to the CLI, generated schemas/types and console.
- Provide multi-Worker local examples and staging fixtures matching the supported behavior. Identify emulated versus deployed resources explicitly.
- Expose effective runtime capabilities and publication state so operators can diagnose rejected features and failed rollouts.

**Done when:** a platform developer can deploy, dispatch, update, inspect and roll back a customer Worker using supported tools, and cannot view another customer's telemetry through client-selected filters.

## Step 9 — Add the Cloudflare management compatibility adapter

**Status: in progress.** A bounded multipart translator, persistent adapter state and
atomic secret/code publication pass local PostgreSQL, race and native runtime
tests. Native deploy, vars, secret and listing interoperability also pass local
checks. Core compatibility HTTP routes and upload differential/runtime tests
now pass locally behind a disabled server option. Settings/secret/tag mutations
also pass local memory/PostgreSQL and native runtime checks. Module content
GET/PUT, single-tag addition/filtering and asset session/upload/publication flows
are implemented locally. Bounded periodic staging cleanup is wired to graceful
daemon/fixture shutdown. Single-tag bulk deletion now uses atomic metadata
batches with explicit configuration reconciliation. The bounded real PostgreSQL/MinIO
asset load and restart drill now passes through the adapter and native runner.
Broader client flows and production soak remain unqualified. See
the [step 9 status record](workers-for-platforms-step-9-status.md).

The persistent asset-session service verifies manifests, scoped upload and
completion credentials, staged bytes, expiry, concurrent reservation limits and
connection reopen with memory/PostgreSQL. Its HTTP/publication flow now also
passes local tests, including real workerd asset serving and first-attachment
Worker binding. Official JavaScript SDK 7.3.0 flows pass with explicitly
constructed multipart requests; generated upload/settings defaults are rejected.
Bulk text-secret merge patches publish one complete revision, including
concurrent precondition checks, rollback-safe retries and failure recovery tests.
Wrangler 4.54.0 plain-module upload/update and namespace flows pass locally with
profile-header adaptation. Broader Wrangler and live Cloudflare portability
remain unverified.

Once the native contract is stable, translate supported Cloudflare-shaped operations into the same internal model:

- Namespace/script operations, multipart code metadata and typed bindings.
- Settings updates, secrets, tags and preservation/inheritance semantics.
- Asset manifests, scoped upload sessions and completion references where required.
- Response/error translation and explicit rejection of unsupported features.

Keep the native API intact. Do not advertise drop-in Wrangler/SDK compatibility until the relevant client flows pass contract tests. Verify the documented differences, especially native weighted deployments and effective runtime settings.

**Live validation prerequisite: blocked.** The supplied `clas.wen` account returns Cloudflare 403/10121 for dispatch namespaces, and the user has no other enabled account. See the [live comparison record](workers-for-platforms-live-comparison.md). Local implementation and contract tests can proceed; live portability remains unverified.

**Done when:** equivalent native and compatibility requests produce equivalent authorized deployment snapshots and behavior. A controlled live Cloudflare staging comparison establishes the exact portability claims; account access is required for that validation, not for the native implementation.

## Step 10 — Qualify capacity, recoverability and rollout

**Status: bounded local trusted-pilot qualification and independent review pass.**
Linux authorization, real backend/process recovery, sustained 1/2 GiB load,
physical OOM/namespace failure-domain checks and the real monitoring/alert pipeline
pass. The explicit pilot override selects native execution and physical bounds;
the normal daemon and experimental adapter remain disabled by default. The
[pilot runbook](workers-for-platforms-trusted-pilot-runbook.md) and
[step 10 record](workers-for-platforms-step-10-status.md) retain measured limits,
limitations, recovery and rollout checks. Production expansion and live Cloudflare
acceptance remain unverified; no release/deployment has occurred.

- Measure cold/warm latency, RSS, loading/eviction, deployment churn and sustained concurrency at increasing Worker counts. The PoC's small sequential sample is not a capacity limit.
- Test runner crashes, control-plane restarts, interrupted publication, revoked grants, long-lived connections and unavailable policy/artifact services.
- Confirm physical pool placement bounds resource use and failure impact independently from logical namespace membership.
- Run the security/authorization matrix against the chosen production-like sandbox, PostgreSQL and storage services. Add independent security review before an untrusted release.
- Release a disabled-by-default trusted pilot with documented capability checks, admission limits, monitoring and a rollback procedure that has been exercised.
- Enable untrusted customer code only after the untrusted gate passes. Promotion to a pilot or production is a separate deployment action, not implied by completing local tests.

Use Conventional Commits and the existing Release Please workflow; do not manually bump shared package versions or create replacement release tags. Vite-plugin changes follow its independent release path.

**Done when:** release-specific acceptance criteria are met, operators have measured limits and recovery procedures, and the supported profile is explicit in both API responses and documentation.

## Dependency and release gates

```text
Completed research + PoC
  -> 1. Supported contract
  -> 2. Runtime authorization fixes
  -> 3. Execution/sandbox decision
  -> 4. Resource model
  -> 5. Artifact publication
  -> 6. Private dispatch
  -> 7. Policies
  -> 8. Tooling and operations
  -> 10. Trusted pilot qualification

Stable native contract -> 9. Compatibility adapter -> adapter qualification
Step 3 protections + step 7 policies + full step 10 validation -> untrusted release
```

An unsuccessful untrusted execution experiment at step 3 does not prevent a trusted-only MVP. It does prevent enabling or advertising the untrusted profile. A trusted pilot also needs the step 2 boundary corrections; calling code trusted does not justify leaving cross-handler authorization gaps in the platform.

## Suggested reviewable changes

Keep changes small enough to validate independently: research/PoC evidence; telemetry confinement and attribution; runtime handler authentication/resource grants; execution experiment and decision; namespace schema/migration; namespace API and permissions; artifact publication; private dispatch/RPC; outbound/cache policy; tooling/metrics; compatibility adapter; qualification and rollout configuration.

Execution-budget and sandbox experiments can run alongside the authorization fixes once step 1 defines the expected guarantees. Tooling can follow completed native endpoints incrementally. Avoid merging runtime, schema, UI and compatibility changes as one large feature change.

**Next gates:** live Cloudflare comparison requires an entitled staging account; the user confirmed none is available. Do not request credentials again or enable billing. The [independent review record](workers-for-platforms-independent-review.md) records three findings, fixes and passing verification. CPU enforcement remains separate. Production expansion requires qualification of actual workloads and deployment-specific operations; local trusted-pilot evidence does not satisfy those gates.
