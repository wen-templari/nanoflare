# Separate workstream: real CPU time limits

Status: scoped, not implemented. Split from step 3 at the user's request on 2026-10-18. This document is a handoff specification, not a newly created Codex chat.

## Why this is separate

The open-source server in workerd `v1.20260911.1` supplies a `NullIsolateLimitEnforcer`; its invocation limit hooks and subrequest hooks are no-ops, and the dynamic entrypoint resolver receives limits without enforcing them. This is a runtime implementation gap, not a missing Nanoflare timeout setting. See the [tagged server implementation](https://github.com/cloudflare/workerd/blob/v1.20260911.1/src/workerd/server/server.c%2B%2B#L3271), [invocation hooks](https://github.com/cloudflare/workerd/blob/v1.20260911.1/src/workerd/server/server.c%2B%2B#L4581) and [dynamic entrypoint resolver](https://github.com/cloudflare/workerd/blob/v1.20260911.1/src/workerd/server/server.c%2B%2B#L5260).

The Linux proof measured workerd CPU from `/proc/1/stat`, independently of request duration. The runtime continued consuming CPU after a request timeout despite `cpuMs: 10`. A container CPU quota throttles CPU consumption rate; it does not stop one invocation after a CPU budget. A parent process kill contains the failure but interrupts every request in that process.

## Required contract with the platform work

Keep the API/design seam stable while runtime implementation proceeds:

- Invocation identity comes from a trusted issuer and binds organization, Worker, immutable deployment, effective policy and lifecycle lease. Customer headers/globals cannot select identity or effective budgets.
- CPU budget and wall deadline are separate. CPU measurements come from trusted runtime/OS instrumentation; waiting for I/O or a timer must not count as elapsed CPU. Define whether startup, compilation and runtime overhead have separate budgets or are charged; none may be silently unlimited.
- Cover synchronous JavaScript, microtasks, WebAssembly, relevant native work and permitted background work. Keep accounting alive through the invocation's defined stream/RPC/background lifetime; a response is not necessarily completion.
- Budget exhaustion interrupts executing code and reports a stable limit-exceeded outcome. Measurements and outcomes must be attributable to the correct invocation under concurrency and cannot be supplied by customer JavaScript.
- Choose and document the actual interruption unit. If termination destroys an isolate or process, explicitly identify other affected requests and recover/replace it before admitting further work. Do not promise per-invocation isolation from a process-wide kill.
- CPU enforcement composes with the external shared subrequest counter. Nested dispatch/RPC and retries cannot reset a parent budget. Decide which worker owns each CPU slice, how child budgets are capped, and which trusted platform work is separately accounted.
- Expose runtime build/version and supported capability information to the runner. Reject CPU-limited/untrusted profiles unless the configured runtime has passed the qualification suite. No ignored limits, fabricated CPU telemetry or timeout fallback.

An embedded/runtime fork with real isolate/invocation limit hooks is a candidate. A process-per-invocation design is another candidate, with concurrency, streaming, startup cost and capability-transfer tradeoffs. Evaluate rather than assume equivalent behavior.

## Acceptance evidence

1. An infinite JS loop and a WASM loop are interrupted by actual CPU consumption before a generous wall deadline. Include measured CPU, termination reason and bounded overshoot/tolerance.
2. An I/O-bound invocation can exceed the same wall duration without exhausting its CPU budget; configured wall limits still apply independently.
3. Concurrent invocations and separate tenants cannot steal/reset accounting. Explicitly test two requests using the same isolate.
4. Cover `waitUntil()`, loops starting after the response, streaming producers, cancellation, nested RPC and exceptions. Release or revoke invocation leases consistently on termination and replacement.
5. Verify next-request recovery, peer availability and no surviving background side effects after termination/revocation. If a failure unit is broader than an invocation, record its exact blast radius.
6. Pin executable and source provenance. Run repeatable adversarial tests on the actual deployment OS/runtime build, including repeated exhaustion and overhead measurements.

## Dependency boundary

This task blocks advertising or releasing untrusted execution with CPU guarantees. It does not block continuing the namespace/resource model, artifact lookup, private dispatch, external subrequest accounting, tooling or a clearly trusted-only MVP. Sandbox hardening and other release gates remain independent requirements even after CPU enforcement passes.
