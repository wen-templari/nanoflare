# Workers for Platforms remaining work

Updated 2026-10-22. Native trusted dispatch, the documented compatible management
subset, dispatcher uploads, CLI/console tooling and official example workflow
are implemented. Local bounded qualification and independent code review are
complete for their recorded scope. This does not establish production or
untrusted-code readiness. The [documentation index](../README.md) links the
maintained contract; [historical records](../archive/workers-for-platforms/README.md)
retain the implementation and test evidence.

## Live Cloudflare comparison — blocked externally

The available account returned HTTP 403 / Cloudflare 10121 for namespace access;
no WFP-entitled staging account is available. Do not repeatedly request credentials
or enable billing. When an entitled account becomes available, compare supported
requests, responses, asset flows and errors against the same pinned local cases.
Until then, keep live portability and exact hosted error semantics unverified.
See the [prerequisite record](../archive/workers-for-platforms/workers-for-platforms-live-comparison.md).

## Broader client compatibility

SDK 7.3.0 default uploads support scalar/list metadata and asset completion
tokens. Asset routing configuration is supported. Binding object arrays still require
explicit JSON metadata. Default settings support covers
compatibility date, compatibility flags and tags; bindings and explicit empty
lists require a JSON settings part. Wrangler 4.54.0 qualification covers
selected management requests directly, with administrator-configured execution consent. Full provisioning,
secrets/assets CLI flows and other versions need implementation/verification.
The ordinary Worker adapter also supports service/current-traffic probes,
disabled workers.dev state, secret reads/mutations and forced service deletion.
A pinned Wrangler runtime workflow is qualified with namespace secret/customer
cleanup adaptations; ordinary asset sessions and broad CLI parity remain open.
Pagination, compound filters and further module/binding formats are explicit
extensions, not behavior inferred from the current route subset.

Acceptance for each extension: document its wire behavior, add targeted
regression coverage and exercise the real pinned client. See the
[client fixtures](../../scripts/wfp-client-flows/README.md). Official application
acceptance does not by itself qualify upstream deployment tooling.

## CPU enforcement and untrusted execution

Actual CPU accounting/interruption is [scoped separately](workers-for-platforms-cpu-enforcement.md)
and not implemented. Hardened customer isolation, interruption/failure blast
radius, capability transport and adversarial qualification must also pass before
untrusted execution is offered. Wall/effect deadlines and shared container
quotas are not substitutes. Custom CPU/outbound/limit options and other deferred
runtime features require explicit implementation before acceptance.

## Production expansion and operations

Recorded Linux capacity, shared-pool OOM recovery, process/storage failures,
monitoring and combined asset-storage drills establish bounded local behavior.
Production expansion still requires representative applications, sustained soak,
revision/memory growth and retention sizing, deployment-specific DNS/TLS/secrets,
backup/recovery practice and operator alert routing. Determine capacity from
actual workload and admission policy, not the smoke fixture's Worker count.

Acceptance: retain runtime/build provenance and workload/failure evidence for
the selected environment, verify real dispatch after recovery and rollback, and
resolve deployment-specific operational findings. See the
[pilot runbook](../guides/workers-for-platforms-operator.md) and
[qualification evidence](../../scripts/wfp-qualification/README.md).
