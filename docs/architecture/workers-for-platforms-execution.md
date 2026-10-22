# Workers for Platforms execution decision

Date: 2026-10-18. Status: selected architecture and initial supported subset for implementation. Step 3's design/feasibility gate is complete; production untrusted execution is unavailable. This decision does not implement a new production runner or certify Docker as a secure tenant sandbox. [CPU enforcement](../plans/workers-for-platforms-cpu-enforcement.md) remains a separate workstream.

## Execution and authority boundaries

Continue the existing shared workerd pool for trusted platform-controlled code. An untrusted profile must place different customer trust domains in separate cells. A cell means an independently stoppable process inside a separately qualified OS/VM sandbox, with independent memory and admission limits. Docker containers proved bounded CPU/memory fault containment in the local experiment; the hardened sandbox provider is a release dependency, not selected or certified by this experiment.

The cell key is organization plus customer trust-domain ID; when an organization is itself the customer, that organization is the domain. A logical dispatch namespace or environment does not constitute a physical trust boundary. Multiple scripts/revisions from one domain may use separately admitted cells. Never put mutually untrusted customers in the same process to save loader overhead. A cell kill can still interrupt concurrent requests from its own domain, which is an explicit failure unit until runtime CPU interruption provides a narrower guarantee.

Keep platform authentication, namespace resolution, immutable artifact authorization, resource effects, egress decisions, budget state and management access outside the cell. A small bootstrap shim inside workerd may load verified code and present local native facades. Treat the shim and its credentials as compromised if the cell suffers a native escape. Credentials therefore grant only that cell/domain's transport scope; never install app signing roots, global registry credentials, Docker sockets or control-plane management tokens in customer cells.

The effect broker must intersect authenticated cell identity, the exact revision/resource grant, the active invocation lease, declared operation scope and remaining budgets. A caller-selected header, URL, namespace or revision cannot enlarge any of them. The step 2 revision grant is necessary but is not a replacement for invocation lifetime/budget authorization. Accepted effects may finish after revocation; cancellation is best effort for in-flight I/O and cannot undo committed writes.

## Supported bridge subset

Use a typed local `WorkerEntrypoint` facade passed in per-invocation native props. Implement each permitted facade method by a defined authenticated request to the external broker. Credentials are applied by the cell transport configuration, and an opaque invocation handle is held by the facade, not copied into the customer's request headers or static environment. The external broker owns identity and counters and validates the handle on every effect.

The proof verifies `identify()` and a resource `fetch(Request)` facade across the process boundary using JSON requests/responses for synthetic effects. It also verifies the response stream between cell and caller. It does not serialize arbitrary RPC objects or preserve arbitrary Cloudflare native capabilities across processes. Reject unknown declared methods/capabilities rather than attempting to stringify them or downgrade authority into plain headers. Real KV, database, object and service adapters must define their typed protocols before being enabled for a strict profile.

The first strict subset keeps global outbound null. Request-scoped resources/outbound operations use the passed facade. Warm global `fetch()` with arbitrary per-request outbound context is **not** established by this experiment; do not advertise that combination. Existing trusted platform Workers can retain their documented networking behavior. TCP, CONNECT, WebSockets, durable objects and shared cache semantics are not included in this new strict bridge subset and require separate qualification. This is an intentional Nanoflare subset, not Cloudflare WFP API equivalence.

## Reuse and bounded admission

Cache code by immutable Worker/revision and effective static policy, never by invocation ID. Do not capture invocation-bound facades in the WorkerCode environment, global outbound closure or global state. Create a new native entrypoint props object/facade for each invocation; stale native capabilities must fail on use in another request. This follows the separation between WorkerCode and entrypoint props in the [pinned Worker Loader API](https://github.com/cloudflare/workerd/blob/v1.20260911.1/src/workerd/api/worker-loader.h).

The bridge test observes one instance across 36 sequential/concurrent calls and a second instance only when changing revision. That resolves the per-request loader-key growth problem for the typed facade subset. It is not evidence of runtime eviction or production capacity.

Production admission must bound cells, live invocation leases, active revision cells, draining revision cells, retained audit data, queue depth, message sizes and total memory. Resolve a new revision for future invocations while existing leases keep their original revision/grants. Retire an old generation after its last foreground/stream/background lease closes; if it exceeds its deadline, revoke and stop only that domain's affected generation. Reclaim retired loader state by recycling the cell/generation, rather than assuming a nonexistent loader eviction guarantee. If active plus draining allocations exhaust the configured budget, queue within a fixed limit or reject admission/publication; never silently discard a live revision.

The fixture enforces at most 16 leases, retains at most 64 terminal audit records, and keeps at most two loaded revisions during its update test. These are test settings. Production limits and startup/capacity qualification are separate runner work.

## Invocation lifecycle

The trusted issuer authenticates the caller and selects an authorized immutable deployment before allocating a lease. Fix the trust-domain, Worker/revision, allowed effects, effective policy, remaining budgets and deadlines at admission. Customer code receives none of the authority to change that selection.

| State    | Meaning                                                             | Transition                                                          |
| -------- | ------------------------------------------------------------------- | ------------------------------------------------------------------- |
| Admitted | Identity, snapshot and resources reserved                           | Start only after runtime readiness and grant checks                 |
| Running  | Request body/handler, RPC and response producer may execute         | Keep lease through permitted I/O, not just response headers         |
| Draining | Foreground stream has ended; permitted background work may continue | Original budgets remain, with a shorter bounded background deadline |
| Terminal | Completed, cancelled, expired, explicitly revoked or runner lost    | Reject new effects; release admission and bounded retained state    |

All deadlines are capped by the root wall deadline. Response headers do not finish an invocation. Pin the selected revision until request upload, response production and declared RPC work finish or are cancelled; then permit only bounded background work. Never refresh a lease because a customer requests a new effect. The fixture's response-EOF transition and 200 ms background window are measured examples; streaming uploads, full-duplex protocols and arbitrary RPC return streams are not qualified.

Client disconnect revokes the lease and cancels the corresponding upstream transport. An explicit control-plane revocation, cell loss or deadline also revokes it. A replacement process creates new leases; it does not revive old ones. No automatic replay of writes on runner recovery is allowed. Idempotency for retried effects belongs to the particular resource protocol and must be explicit.

The external deadline stops admission/effects and tears down the transport; it cannot interrupt an infinite JavaScript loop. Real interruption belongs to the separate CPU/runtime task. Until then a controller may terminate an unhealthy cell, with the documented same-domain blast radius. Keep wall-time failures distinct from CPU exhaustion in telemetry and errors.

## Subrequest accounting contract

Use one external, atomic root counter per invocation and narrower child budgets where needed. Authorize and reserve before an effect. Failed authorized attempts, retries and redirect hops consume units without refunds. Reject exhausted budgets before executing the effect. Parallel calls cannot each spend a stale balance. A nested dispatch or RPC method consumes its entry unit, and each downstream resource effect consumes another root unit; internal forwarding of that same effect does not charge it twice.

For the proposed strict profile, charge one unit per HTTP attempt/hop, KV operation, object-store operation or paginated page, service request and RPC method invocation. Charge a DB batch for each SQL statement, reserving its statement count before executing the batch. Parse/validate at the trusted executor; do not accept a customer-supplied count. Reject unsupported multi-statement forms until correct accounting and transaction semantics exist. HTTP cache hits still consume the user fetch unit; transport bookkeeping and a cache fill for the same attempt are not additional logical effects. A separate cache API remains unavailable until its policy is defined and tested.

Stream chunks do not mint subrequests, but streams have independent byte/backpressure/lifetime limits. Background work uses the original remaining counter and background deadline. Keep platform authentication, policy lookup, DNS/protocol work and telemetry in separately bounded infrastructure budgets; the caller cannot label an operation as infrastructure. Unsupported TCP/tunnels or other unmetered effect routes remain denied. This is a Nanoflare policy proposal, not a claim of Cloudflare accounting or billing parity.

The synthetic experiments cover sequential/parallel calls, redirects, retries, resource calls, typed RPC, mixed calls and post-response work. Production adapters, nested cross-Worker chains and cache policy still need integration tests before enabling them.

## Errors and release gates

The future native API must report an unavailable execution profile/capability explicitly, for example a stable `unsupported_execution_profile` or `unsupported_capability` error, rather than accepting ignored limits. Transport errors distinguish authorization denial, expired/revoked lease, budget exhaustion, admission overload and runtime failure. Exact public API status/error schemas belong to implementation; fixture 401/403/410/429/503 statuses are local protocol evidence.

Step 3 is complete as an architecture and bounded-feasibility decision for this restricted subset. Step 4 can implement the trusted-only organization/namespace/script/resource model using these invocation and revision seams. Untrusted execution remains blocked by real CPU enforcement, a qualified hardened sandbox, production cell/broker integration, complete resource/egress policy, lifecycle/operational recovery tests and capacity qualification. WebSockets, general cross-process RPC and request-scoped warm global outbound are explicitly deferred features, not hidden prerequisites for the restricted subset.

Evidence: [failure-domain and counter results](../../scripts/archive/wfp-isolation-poc/results-step3-final-linux.json), [warm bridge/lifecycle results](../../scripts/archive/wfp-isolation-poc/results-step3-bridge.json), and the [step 3 status record](../plans/workers-for-platforms-step-3-status.md). No production deployment is represented by this decision.
