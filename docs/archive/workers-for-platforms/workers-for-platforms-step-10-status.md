# Step 10 — capacity, recovery and rollout qualification

> Historical record. Status statements and proposed designs below describe the work at the time.
> See the [documentation index](../../README.md) for the maintained contract and
> [remaining work](../../plans/workers-for-platforms-remaining-work.md) for current gates.

Updated 2026-10-22. Status: **bounded local trusted-pilot qualification passes;
independent review completed with all three findings resolved; live/production-expansion gates remain open**.
This record separates bounded local observations from production-like release
qualification. Real CPU enforcement remains a separate user workstream, and
untrusted execution remains unavailable. No release or deployment has occurred.

## Native capacity smoke fixture

`TestPrivateDispatchCapacityQualification` exercises the integrated native
dispatcher/runtime manager, private dynamic Workers, revision artifacts,
request-bound props, revision churn and rollback. It has explicit opt-in and
native-binary checks. The [fixture instructions](../../../scripts/wfp-qualification/README.md)
define workload, timing, client pooling/backoff and report limitations.

The baseline run uses native workerd 2025-12-10 on one macOS arm64 host with 12
logical CPUs and Go 1.25.0. It uses one namespace, a distinct customer per script,
memory metadata/object fixtures and two-second load slices at 1/8/32 concurrent
clients. Successful responses must match their Worker, revision and unique
request marker. Admission 429 responses are counted separately, with ten-ms
client backoff. Calibration is paced and waits for earlier background authority
to drain; these waits are excluded from measured request latency.

Baseline observations:

| Customer Workers | Native RSS sample (MiB) | First-request p95 (ms) | Immediate-repeat p95 (ms) | Successful requests/s across tested concurrency |
| ---------------- | ----------------------- | ---------------------- | ------------------------- | ----------------------------------------------- |
| 1                | 85.8                    | 10.88                  | 2.22                      | 15.9–16.0                                       |
| 16               | 131.8                   | 8.24                   | 4.24                      | 63.8–64.0                                       |
| 64               | 205.5                   | 10.71                  | 5.02                      | 63.6–64.0                                       |
| 128              | 299.7                   | 10.34                  | 5.86                      | 63.7–63.9                                       |

The successful rates reach current admission policy, not a CPU/engine throughput
maximum. Leases retain bounded background authority after response completion
and count toward 32/customer and 128/namespace admission. A two-second authority
window explains the observed approximate 16/customer and 64/namespace rates in
this fast-response fixture. Many attempted requests return 429 under overload;
the report retains their actual counts instead of presenting them as successes.
The initial generator also exposed local client-port exhaustion when using an
aggressive rejection loop and the default small idle pool. The final fixture
uses explicit reusable connection bounds and rejection backoff; no runtime
admission rule was weakened to improve the result.

The same fixture also passes on native workerd 2026-10-18:

| Customer Workers | Current-runtime RSS sample (MiB) | First-request p95 (ms) | Immediate-repeat p95 (ms) |
| ---------------- | -------------------------------- | ---------------------- | ------------------------- |
| 1                | 118.4                            | 22.36                  | 2.72                      |
| 16               | 233.1                            | 11.93                  | 5.71                      |
| 64               | 393.4                            | 11.31                  | 5.50                      |
| 128              | 526.1                            | 10.76                  | 6.12                      |

The current runtime samples about 75% more native RSS at 128 Workers in this
single run. Successful rates remain about 15.9/customer or 63.7–63.8/namespace
requests/s under the same admission policy. Its 16 revisions publish with p50
2.563 ms, p95 5.666 ms and maximum 7.944 ms; churn RSS samples about 528.8 MiB.
It also retains the revisited/rolled-back isolate identity and launches the
dispatcher once. These sequential single-host samples are not a controlled
causal performance comparison or a demonstrated runtime regression; they show
that memory headroom cannot be assumed portable across runtime versions.

Sixteen successive revisions publish with p50 2.745 ms, p95 4.600 ms and maximum
8.791 ms in this memory-backed run. RSS after churn samples about 318.4 MiB.
The platform dispatcher launches once and does not restart for customer churn.
Original code executes after rollback. Immediate repeats, a revisit after a
128-revision scan and rollback retain the observed original isolate identity.
Crossing the 64-entry code-descriptor LRU therefore does **not** establish a
64-isolate resident-memory bound. A code-descriptor cache bound is not a native
isolate eviction or process RSS guarantee.

Evidence: [baseline report](../../../scripts/wfp-qualification/results-step10-baseline.json)
and [current report](../../../scripts/wfp-qualification/results-step10-current.json),
plus `/tmp/nf-step10-capacity-baseline.log` and
`/tmp/nf-step10-capacity-current.log`. Each report includes binary SHA-256,
host/runtime identity, successful latency quantiles, attempted/rejected counts,
point-in-time native RSS and correctness outcome. It passes its bounded fixture;
it is not a production soak, peak-RSS measurement or maximum tenant capacity.
The [provenance record](../../../scripts/wfp-qualification/provenance-step10.json)
binds these reports to their backend source snapshot and probe checksum.

An initial post-probe full-suite run hit the existing 30-second Wrangler fixture
deadline on its first namespace-list command. The process was terminated by the
fixture; its targeted rerun passed in about 2.2 seconds. No root cause is inferred
from that one retry. Evidence: `/tmp/nf-step10-initial-full.log` and
`/tmp/nf-step10-wrangler-recheck.log`. This is recorded separately from the
successful capacity probes rather than hiding the failed verification run.
The complete rerun, including PostgreSQL/native runtime and SDK/Wrangler fixtures,
passes in `/tmp/nf-step10-initial-full-recheck.log`.

## Linux physical failure boundary and runner recovery

The opt-in `TestPrivateDispatchLinuxRecoveryQualification` now exercises the
current runner executable in a separate Linux arm64 container with native
workerd 2026-09-11, real PostgreSQL KV and a disposable MinIO asset store.
It verifies actual cgroup v2 memory/swap/PID controls: 256 MiB RAM, no additional
swap and 128 PIDs. The process runs as UID/GID 65532 with dropped capabilities,
read-only root and bounded tmpfs writes. The host control API and PostgreSQL are
outside the runner cgroup. These are fixture bounds, not qualified pilot sizing.

Killing the runner returns unavailable dispatch (observed 502), while metadata
remains available. A fresh runner is live but has no generation (503). Durable
reconciliation restores the current revision, PostgreSQL KV value and MinIO
asset, and a pinned rollback restores the original revision/asset. Native memory
pressure from retained 32 MiB typed arrays in successive dynamic isolates causes
a cgroup OOM kill. The control metadata path remains available. Automatic native
child recovery and automatic control-plane boot recovery both restore dispatch.
The number of pressure Workers is a deliberately forced failure observation,
not an admission limit or supported tenant count.

This drill found and fixed a real recovery gap: a restarted remote runner had
no generation, and process health alone did not restore it. The runner now offers
authenticated non-cacheable process boot identity. The daemon polls every five
seconds and reconciles fresh/changed identities, retrying failures without
acknowledgment. Recovery and startup snapshot/publication hold the repository
publication guard, preventing stale snapshots from undoing concurrent deletion
or traffic changes. Unchanged runner identity does not cause periodic writes.
Native child failure continues to use the manager's existing restart loop.

Evidence: [Linux report](../../../scripts/wfp-qualification/results-step10-linux-recovery.json),
its [provenance](../../../scripts/wfp-qualification/provenance-step10-linux-recovery.json),
`/tmp/nf-step10-linux-recovery-race.log` and
`/tmp/nf-step10-reconciliation-race.log`. The latter covers memory/PostgreSQL
publication/deletion serialization plus authenticated boot status, retry and
shutdown behavior. The fixture reconstructs in-process service state in its
explicit recovery phase; it does **not** kill the control-plane process or
PostgreSQL/MinIO servers. The separate process drill below addresses actual
server crashes and two durable publication phases; wider failure/partition
coverage and deployment-specific monitoring/placement remain release gates.
The full Go suite after the recovery fix passes with PostgreSQL, baseline native
workerd, SDK 7.3.0 and Wrangler 4.54.0 enabled:
`/tmp/nf-step10-recovery-full.log`. The disposable Linux containers were removed
and the schema cleaned after qualification. No production deployment changed.

## Actual control-process and storage-server failures

The new opt-in [process drill](../../../scripts/wfp-qualification/process_recovery.py)
uses the current host-native daemon executable, a separate Linux runner and its
own PostgreSQL/MinIO containers. It manages Workers through actual authenticated
HTTP, not an in-process service replacement. The runner has verified cgroup v2
bounds of 512 MiB RAM, no additional swap and 128 PIDs; this small recovery
fixture does not establish capacity or a recommended pilot limit.

The [process report](../../../scripts/wfp-qualification/results-step10-process-recovery.json)
records successful assertions for:

- SIGKILL of the actual daemon and restart from durable metadata/artifacts,
  preserving active code, pinned secret/asset bytes and PostgreSQL KV.
- SIGKILL/restart of the actual PostgreSQL server, unavailable dispatch while it
  is down, and successful reuse of the control process's existing connection pool
  after crash recovery. The observed outage response is 401 because authentication
  itself depends on PostgreSQL; this is not a hosted Cloudflare error parity claim.
- SIGKILL/restart of the actual MinIO server with pinned asset recovery, plus a
  brief paused-server transport stall. No request returns success during the
  observed outage; the two-second Python client deadline is not a backend SLA.
- SIGKILL during durable `building`, observed through PostgreSQL while object
  writes are stalled: recovery marks the incomplete operation failed, retains the
  previous active revision and returns 409 for the failed idempotency key.
- SIGKILL during durable `stored`, observed while remote runner preparation is
  stalled: recovery completes the new artifact's publication and preserves its
  deployment ID on replay. This differs intentionally from incomplete building.
- Pinned rollback, daemon restart and replay of an earlier successful operation:
  the persisted rollback remains active rather than being undone by that retry.

The storage stall investigation also found that MinIO calls used background
contexts, so a client timeout could leave storage work running. Runtime asset and
object handlers now pass invocation/request cancellation through a context-aware
store interface into the actual S3 transport. MinIO operations, including retry,
startup checks and body read, also have a default 30-second bound; an earlier
caller deadline/cancellation wins. Existing synchronous fixture providers remain
compatible, while other I/O providers need to implement `ContextObjectStore`.
Cancellation cannot undo a write that storage already committed.

`/tmp/nf-step10-storage-context-race.log` verifies memory/PostgreSQL handler
cancellation for assets and object GET/HEAD/PUT/DELETE, prevents a late mutation
after cancellation of its preflight HEAD, and checks cancellation on actual
stalled SDK HTTP transports for GET/HEAD/PUT/DELETE/LIST. A separate body-stall
test verifies operation timeout after headers, and a startup-stall test verifies
bounded object-store initialization. The external process drill alone does not
prove these backend cancellation guarantees.

Evidence also includes `/tmp/nf-step10-process-recovery-final.log`, the
[process provenance](../../../scripts/wfp-qualification/provenance-step10-process-recovery.json)
and a post-change [Linux OOM/recovery rerun](../../../scripts/wfp-qualification/results-step10-linux-storage-context.json)
with its own [provenance](../../../scripts/wfp-qualification/provenance-step10-linux-storage-context.json).
Historical macOS and initial Linux report provenance is preserved. The fixture
confirms cleanup of its own processes/containers and temporary data. It does not
qualify real multi-host partitions, packet loss, every publication crash boundary,
production admission/headroom, soak or the monitoring pipeline.
Post-change full-suite verification passes in
`/tmp/nf-step10-storage-context-full.log`, with PostgreSQL, baseline native
workerd, the official SDK and Wrangler fixtures enabled. The new process and
Linux reports record passing outcomes and their source snapshots separately
from historical capacity/recovery evidence.

## Linux monitoring and collector permissions

The opt-in [monitoring fixture](../../../scripts/wfp-qualification/monitoring.py)
runs actual Linux daemon/runner processes with PostgreSQL, MinIO, Vector 0.46.1,
Loki 3.5.0, Prometheus 3.5.0 and Grafana 12.0.2. Its
[report](../../../scripts/wfp-qualification/results-step10-monitoring.json) records
passing assertions, immutable image/binary identities and successful cleanup.
Its [provenance](../../../scripts/wfp-qualification/provenance-step10-monitoring.json)
binds the fixture and runtime/configuration snapshot; older report provenance
is preserved.

This qualification found and fixed missing production wiring: the daemon and
runner did not mount the Unix collector, the daemon did not receive the metrics
credential from Compose, and the production scrape target addressed the host
instead of the daemon container. Production now uses its container target and
development has a separate host target. The test uses a private token file
readable by Prometheus UID 65534, while unauthenticated and user-token requests
to the global exporter fail.

The production Vector entrypoint/user are read from actual Compose configuration
in a clean environment. Actual Linux directory/socket mode checks establish
`0:65532` with `0710`/`0660`, respectively. A different UID/GID is denied even
when given the read-only volume; a trusted GID 65532 producer can connect and
deliver an event. Only trusted producers may hold this group/mount. These checks
qualify collector permissions, not a hardened untrusted execution sandbox.

Native customer logs traverse the authenticated broker, forwarder, Unix socket,
Vector, Loki and authorized output API. Both customers retain their own identity,
pinned secrets are redacted, and Docker JSON/customer marker text cannot claim
another producer identity. Docker log ingestion is restricted to one disposable
forgery container in this fixture; unrelated host logs are not collected.

The real Prometheus scrape succeeds with its protected credential file and
container service name. Production admin ports now bind loopback, and anonymous
Grafana access is disabled. Actual Grafana rejects unauthenticated global data
source requests while the fixture operator can read the provisioned Loki source.
Operator access/credentials still need deployment-specific configuration.

Stopping Vector preserves dispatch and increases the new daemon-side
`nanoflare_log_forwarder_dropped_total` counter. After restarting it, a new unique
event reaches Loki while the outage event stays dropped. A separate race test
exercises an accepting/non-reading collector: writes have a real deadline,
shutdown joins the forwarder, and repeated shutdown is safe. This corrects the
earlier connect-only timeout, which did not bound a stalled write.

Evidence: `/tmp/nf-step10-monitoring-final.log`,
`/tmp/nf-step10-vector-race-final.log` and full-suite
`/tmp/nf-step10-monitoring-full.log` (PostgreSQL, baseline native workerd and
official SDK/Wrangler fixtures enabled). Vector validates against the pinned
image, and both Compose files pass configuration validation. Production retention,
pool-resource alerts, sustained capacity and selected-profile rollout qualification
remain open; passing this bounded pipeline fixture does not settle those items.

## Selected Linux pilot: limits, sustained load and alerts

The current [pilot override](../../../docker/compose.trusted-pilot.yml) explicitly
selects native workerd and enables trusted native dispatch. The ordinary daemon
remains disabled by default; the experimental compatibility adapter needs its own
opt-in and trusted-mutation acknowledgment. Current images bundle a stable
`/usr/local/bin/workerd-native` path, with the package's arm64/64 architecture
naming handled at build time. Only Linux arm64 is qualified by these runs.

The [Linux authorization report](../../../scripts/wfp-qualification/results-step10-linux-authorization-final.json)
passes 89 tests/subtests without skips under the selected native runtime,
PostgreSQL and actual SDK 7.3.0 on Node 22. It includes local/remote native dispatch,
resource sharing/revocation, root/instance capability isolation, atomic managed
publication/recovery and adapter HTTP contracts. It caught Node 22 rejecting
unquoted multipart parameters in content responses. Quoted parameters fix that
wire compatibility bug; SDK flows pass without excluding the failing check.

The sustained fixture uses fifteen-second slices for all twelve combinations of
1/16/64/128 Workers and 1/8/32 clients, then churns sixteen revisions and rolls
back. Every successful response must return the correct Worker, revision and
unique marker; admission 429s are counted separately. This is longer bounded load,
not production soak or a maximum engine-throughput measurement.

| Workers | Native RSS at 2 GiB cap (MiB) | First-request p95 (ms) | Repeat p95 (ms) | Successful requests/s across slices |
| ------- | ----------------------------- | ---------------------- | --------------- | ----------------------------------- |
| 1       | 359.8                         | 14.70                  | 4.81            | 17.1                                |
| 16      | 525.0                         | 12.04                  | 5.96            | 68.2                                |
| 64      | 735.5                         | 10.90                  | 6.22            | 68.2                                |
| 128     | 928.7                         | 11.60                  | 6.24            | 68.2–68.3                           |

The initial [1 GiB comparison](../../../scripts/wfp-qualification/results-step10-linux-pool.json)
completed without errors/OOMs but reached its bound: 71,973 `memory.events max`
events and about 54.8 requests/s in its last high-concurrency slice. That leaves
inadequate headroom. The selected [2 GiB run](../../../scripts/wfp-qualification/results-step10-linux-pool-2g.json)
peaks at about 1,103.1 MiB for the **whole probe** (Go fixture plus native engine),
with zero memory-limit events, zero OOMs and zero incorrect/error responses.
Its [capacity detail](../../../scripts/wfp-qualification/results-step10-linux-capacity-2g.json)
records revision churn p95 4.855 ms and roughly 972.9 MiB native RSS after churn.
The original isolate survives the scan/rollback and the dispatcher launches once.
Native memory eviction therefore remains a process-lifecycle concern, not a
property established by the 64-entry descriptor cache.

Current policy holds bounded post-response authority for up to two seconds,
counting toward 32/customer and 128/namespace admission. These fifteen-second
slices include an initial burst, explaining approximate 17/68 averages versus
longer-run policy rates near 16/64. This is not CPU throughput. The override
selects 2 GiB/128 tasks for the runner, no additional swap, with a separate
512 MiB/128-task control container. Counts/bytes are physical failure bounds;
there is no safe maximum customer/Worker count established for arbitrary code.

The [2 GiB OOM report](../../../scripts/wfp-qualification/results-step10-pilot-oom-recovery.json)
forces an actual native cgroup kill with retained 32 MiB arrays (59 Worker
attempts in this run). Control metadata remains available. A peer in a different
logical namespace loses its native isolate with the same physical process,
confirming namespace membership is not the failure boundary. Authenticated
runner telemetry reports the actual OOM counter. Automatic native-child/runner
reconciliation restores pinned code, PostgreSQL KV and MinIO assets after the
failure. The same code can exhaust the pool again if immediately dispatched;
operators must remove bad ingress or roll back the application.

Authenticated runner status now samples its own unified cgroup, rather than the
host mount root. It reports memory/current peak, finite memory/PID bounds, task
count and OOM kills. The daemon exports only a fresh supported snapshot; stale,
unavailable and unlimited are distinct. No per-Worker memory or CPU measurement
is fabricated. Rules flag unavailable/stale data, missing bounds, sustained 80%
pressure, observed OOMs and log loss. Promtool tests cover healthy thresholds,
pressure, missing/unavailable data and counter reset.

The [selected-profile monitoring report](../../../scripts/wfp-qualification/results-step10-pilot-monitoring.json)
uses actual Compose pilot bounds/environment/native selection, protected
Prometheus credentials and loaded alert rules. It verifies a real log-loss alert
and a paused runner becoming unavailable in the exporter while control metadata
continues working, then successful sampling/dispatch after resume. It also tests
the normal daemon's experimental adapter: mutations fail without the trust
acknowledgment and succeed with it. An adapter-only startup fails before backend
initialization. The adapter remains off in the pilot override.

Evidence logs: `/tmp/nf-step10-linux-authorization-final.log`,
`/tmp/nf-step10-linux-pool.log`, `/tmp/nf-step10-linux-pool-2g.log`,
`/tmp/nf-step10-pilot-oom-recovery.log`, `/tmp/nf-step10-pilot-monitoring.log`,
`/tmp/nf-step10-alert-rules.log`, `/tmp/nf-step10-pilot-flags-race.log` and
`/tmp/nf-step10-final-pilot-full.log`. The full suite enables PostgreSQL,
baseline native workerd, SDK and Wrangler fixtures. Historical reports/source
snapshots remain separate from these later changes. New fixture cleanup removes
image-declared anonymous volumes as well as containers; earlier `cleanup_verified`
fields did not establish removal of such anonymous volumes. Unattributed old
volumes are left untouched rather than globally pruned.

## Release acceptance audit

| Original step 10 requirement                                                                        | Evidence for this trusted local pilot                                                                                                                                                     | Decision / remaining scope                                                                                                     |
| --------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| Measure cold/warm loading, RSS, concurrency and churn                                               | Native macOS baseline/current reports plus Linux 1/2 GiB fifteen-second-slice comparisons, exact response checks, churn/rollback and cgroup peak/events                                   | Measured for the bounded fixture; actual applications and production expansion need their own capacity/soak qualification      |
| Confirm physical placement/bounds and failure impact independently of logical namespace             | Explicit separate 2 GiB runner / 512 MiB control bounds, enforced PID/swap controls, actual OOM, separate namespace peer sharing the failed native process, metadata outside that failure | Implemented for one shared trusted runner; no per-customer physical isolation or native isolate-eviction guarantee             |
| Test runner/control-plane crashes, interrupted publication and unavailable policy/artifact services | Actual runner/native OOM and daemon/PostgreSQL/MinIO kill/restart; incomplete building and complete stored recovery; paused storage/runner; pinned rollback/idempotency                   | Local scenarios pass; broader crash boundaries and multi-host partitions remain production-expansion work                      |
| Revocation/long-lived requests and resource authorization                                           | Native streaming/cancellation/revocation matrices, memory/PostgreSQL grants, SQL/egress policy checks and actual S3 transport cancellation; Linux matrix passes without skips             | Passes the supported fetch/typed-facade subset; unsupported transports/cache remain explicitly unavailable                     |
| Security/authorization on chosen profile                                                            | Linux native arm64 selected runtime, PostgreSQL HTTP/capability/publication matrix, real SDK and separate real-storage/monitoring drills                                                  | Independent review completed and findings resolved; untrusted profile remains rejected and needs separate CPU/containment work |
| Operator visibility and documented limits/recovery                                                  | Actual Unix permissions, scoped/redacted logs, protected Prometheus/Grafana, physical pool/staleness metrics, tested alert rules and real collection-loss alert                           | Local pipeline passes; deployment-specific credentials, retention and notification routing remain operator setup               |
| Disabled-by-default pilot and exercised rollback                                                    | Native/adapter flags remain explicit; packaged trusted override, actual environment/native-path checks, durable publication/rollback/restart drills and pilot runbook                     | Local candidate is implemented; no public release/deployment occurred or is implied by these tests                             |

The [pilot runbook](../../guides/workers-for-platforms-operator.md) records the
selected limits, admission/backpressure, capability checks, recovery and rollback.
These results do not promote the prototype into a production-safe untrusted
platform or establish hosted Cloudflare equivalence. Live comparison remains the
external step 9 acceptance gate because the user has no entitled account. Real
CPU enforcement remains separate. The requested [independent subagent review](workers-for-platforms-independent-review.md)
completed after local implementation/qualification. All three findings were fixed
and re-reviewed, then verified with the full suite and the Linux authorization
matrix (89 passes/subtests, no skips, no OOM; approximately 160.6 MiB whole-probe
peak). This later matrix covers the fixes; earlier capacity/recovery/monitoring
reports retain their original source identities and were not rerun as production
soak tests. See the [post-review evidence](../../../scripts/wfp-qualification/evidence-step10-reviewed.json).
