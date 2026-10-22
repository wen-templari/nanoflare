# Step 3 — Execution budgets and failure boundaries

Updated: 2026-10-18. **Status: step 3 design and bounded feasibility complete for the selected restricted subset.** The [execution decision](../architecture/workers-for-platforms-execution.md) defines boundaries, supported bridge methods, warm reuse, invocation lifecycle and accounting. Real CPU enforcement remains a separate pending workstream at the user's request. Untrusted execution stays blocked; no new production runner or runtime protections are claimed.

## Decision and scope

Keep the current shared runner restricted to trusted platform code. Separate customer processes/cells are the candidate fault-containment architecture for hostile code; sharing one cell across customers is rejected for that profile. Privileged platform authentication, artifact authorization, resource/egress effects and budget state must be outside the customer cell. A workerd bootstrap shim may expose a minimal capability facade inside it, but it must hold no broad platform credentials.

Do not integrate a new customer-cell production runner merely because this PoC passes. Secure sandbox qualification, per-invocation CPU handling, capability transport, admission and operational recovery still require implementation. The new bridge experiment establishes a typed local RPC facade backed by an external HTTP effect broker. Arbitrary native capability transfer between independent processes is explicitly outside the initial subset.

[Real CPU enforcement](workers-for-platforms-cpu-enforcement.md) is explicitly split into its own workstream. Other WFP work can progress behind a trusted-only boundary; untrusted release and claims of CPU-limit support depend on that task passing its acceptance tests.

## New bounded experiment

The [Linux fixture](../../scripts/wfp-isolation-poc/README.md) uses the existing local runner image's workerd **2026-09-11**, matching the repository Dockerfile's `WORKERD_VERSION=1.20260911.1`. It resolves images to immutable image IDs, records the native binary SHA-256 and fixture hashes, and runs only disposable containers. The earlier loader PoC's macOS 2025-12-10 and 2026-10-18 reports remain unchanged.

Each customer container has a 192 MiB RAM+swap ceiling, a 0.5-core CPU rate quota, non-root UID, read-only root, no capabilities, no-new-privileges, limited PIDs, no Docker socket and only an internal Docker network. A separate fixture relay has the localhost-only host listener. This is a Docker Desktop Linux experiment on arm64, not evidence of an independently hardened VM per tenant. A process escape could read files mounted into its own container; the API-level filesystem test does not prove native escape resistance.

[Recorded results](../../scripts/wfp-isolation-poc/results-step3-linux.json):

| Probe                      | Observation                                                                                                                                       | What it establishes                                                                                              |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------- |
| CPU limit and cancellation | Requested 10 ms; workerd consumed additional CPU after client timeout; same-process peer stalled                                                  | Loader options and timeout do not enforce CPU; CPU quota is only throttling                                      |
| Independent customer cell  | Separate peer stayed responsive during spin and after forced termination; explicit replacement served requests                                    | Process/container fault containment in this bounded fixture, not automatic recovery or per-invocation isolation  |
| Memory pressure            | 192 MiB cgroup OOM killed offender with exit 137; peer survived                                                                                   | Cgroup contains this memory allocation failure; not V8 heap accounting or full memory-denial resistance          |
| Filesystem / management    | Worker API could not read a synthetic host secret, config or `/proc/self/environ`; HTTP and TCP to a proven live loopback management decoy failed | Expected capability restrictions through tested APIs; not hardened sandbox validation                            |
| `waitUntil()`              | Response completed, then background infinite loop blocked own process while independent peer survived                                             | Lifecycle lease and budgets must cover post-response work                                                        |
| Stream during termination  | First chunk arrived, final chunk was lost when the cell died; peer survived                                                                       | Cell termination interrupts that customer's streams; graceful revision drain remains separate work               |
| External budget broker     | Shared limit of 2 allowed exactly 2 effects across sequential, parallel, redirect, retry, resource, RPC, mixed and background probes              | Trusted external accounting is feasible for the synthetic transport; native/production accounting remains absent |

The external broker experiments use fixture-generated invocation leases. Customer code receives a native request-scoped facade; the raw lease header is removed before invoking it. Redirect handling at the broker transport uses manual mode so every redirect hop returns through the accounting facade. Counters and simulated effects run in the separate relay process. RPC here means a native method on the local facade that forwards an HTTP operation to the external broker; it does **not** demonstrate arbitrary native RPC capability serialization across processes.

The original counter fixture keys a budgeted loaded worker by request lease to avoid capturing another request's global outbound capability in a warm revision cache. It uses a small number of workers and synthetic effects, and leases expire after 10 seconds. The final bridge experiment below resolves warm revision reuse, revocation and bounded lease storage for the typed props/facade subset; request-specific warm global outbound remains unqualified. Durable/replicated accounting and production resource adapters remain unimplemented. The original counter fixture is not a production architecture to copy unchanged.

## Final capability-transport and reuse experiment

The [bridge harness](../../scripts/wfp-isolation-poc/bridge.py) and [results](../../scripts/wfp-isolation-poc/results-step3-bridge.json) complete the remaining bounded investigation on the same pinned Linux runtime:

- 36 sequential/concurrent invocations with two synthetic authenticated subjects shared exactly one revision-cached Worker instance. Fresh native props carried the correct facade/identity into every request. Forged incoming lease/revision headers were replaced; no raw lease appeared in the customer request.
- A saved facade was rejected in a later request context. Raw global fetch, unknown RPC methods and undeclared resource targets were denied. This proves the typed facade subset, not arbitrary cross-process native RPC transport.
- An open v1 stream completed with its v1 authority while later invocations resolved to v2 in a second instance.
- Post-response work used the original lease; late effects received 410 after its background deadline. A foreground deadline revoked authority independently of CPU interruption.
- Client stream cancellation and explicit revocation terminated leases. A forged transport credential received 401, and replaying a revoked lease with the correct fixture transport credential received 410.
- A full set of 16 live leases caused the next request to receive 503. Terminal audit retention stayed at its configured cap of 64 entries after additional invocations.

The bridge uses global outbound null and passes request-scoped capabilities in entrypoint props, avoiding the prior request-unique loader key. It verifies one warm instance within a revision and two instances across an update; it does not establish eviction/capacity under unlimited revisions. The shim uses one narrow transport credential for the one customer cell in this fixture. Native escapes, production key distribution and isolation between different cells are not certified by this test.

The main isolation/counter suite also passed again after adding the reusable relay-source hook; [final results](../../scripts/wfp-isolation-poc/results-step3-final-linux.json) preserve the earlier report as historical evidence. All fixture containers and networks were cleaned up.

## Accounting policy for subsequent integration

Use one trusted invocation lease and atomic remaining budget shared by all permitted effect paths. Reserve before an effect, without refund for a failed attempt; reject before any effect when exhausted. Retry attempts and redirect hops each consume a unit. Concurrent operations share the same counter rather than each reading a stale balance. Nested service/resource/RPC calls inherit the root budget and may receive narrower child budgets; they cannot reset it.

Count user HTTP fetch, granted KV/database/object operations and service/RPC effects at their trusted effect boundary. Define DB batch charging, cache-hit charging and nested call double-counting explicitly before production integration. Byte, memory, concurrency and wall deadlines are independent budgets, not inferred from subrequest count. A stream's internal chunks are not separate fetch calls, but its lifetime and byte limits remain enforced. Background effects use the original invocation's remaining budget and bounded background deadline. TCP/CONNECT/WebSocket or other paths without accounting must be denied for a strict profile.

Keep infrastructure authentication, authorization lookup, telemetry delivery and protocol bookkeeping in a separately bounded platform budget. Do not let customer code label its own effects as infrastructure traffic. This accounting policy is a Nanoflare proposal; do not claim Cloudflare billing/counting parity.

## Capability record and integration gates

The [machine-readable record](../../scripts/wfp-isolation-poc/capabilities.json) is an explicit assessment of the experiments, not a production endpoint or automatic admission control.

- CPU runtime task: enforce and measure actual invocation CPU; see the separate acceptance specification.
- Sandbox task: choose and qualify the Linux/VM boundary against native escape and management/filesystem/network access; Docker controls alone do not qualify the untrusted profile. Upstream explicitly requires an additional secure sandbox for potentially malicious code ([security guidance](https://github.com/cloudflare/workerd/blob/v1.20260911.1/README.md#warning-workerd-is-not-a-hardened-sandbox)).
- Runner integration: separate process placement, startup/memory admission, automatic replacement/backoff, draining leases, cancellation and bounded background lifetime. Same-customer concurrent invocation blast radius must be documented.
- Budget integration: connect all real resource/egress/service/RPC paths to the same trusted counter; prove no bypass, including redirects/retries/cache, TCP and nested dispatch. Corporate egress policy is still open.
- Runtime policy/API integration: reject unsupported requested protections explicitly. No public WFP execution profile/limits API is implemented by this PoC. Audit custom JSON unmarshalling when adding that API so unknown/unsupported limits are not silently discarded.
- Capability transport/reuse: the typed facade/props subset and response-stream/background lifecycle now have bounded evidence and a selected design. Implement that protocol with real grants and cell authentication. General native RPC transfer, WebSockets and request-specific warm global outbound remain deferred; production eviction and capacity still need qualification.

The runtime/source pin here qualifies only these experiments. Production runtime updates must rerun the capability assessment; changing a version string does not establish enforcement. Namespace/artifact/private-dispatch work may continue for trusted code while these dependencies are developed separately.
