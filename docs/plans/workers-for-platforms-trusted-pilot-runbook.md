# Trusted platform pilot: qualification and operations

Updated 2026-10-22. This is the implemented **trusted_shared_workerd** pilot,
qualified locally on Linux arm64 with native workerd 2026-09-11. It does not
qualify untrusted customer code, real CPU enforcement or hosted Cloudflare
portability. CPU enforcement remains the user's separate workstream; the live
Cloudflare comparison requires an entitled account, which is unavailable.

## Select the profile

Normal daemon execution leaves native platform dispatch and the compatibility
adapter disabled. The explicit `docker/compose.trusted-pilot.yml` override enables
native trusted dispatch and selects the native executable bundled in current
images. Build the current source images before using it; an older image without
`/usr/local/bin/workerd-native` must not silently fall back to the npm launcher.

For a disposable local source stack, the existing local-image override can be
combined with the pilot override:

```sh
docker compose -f docker/compose.yml -f docker/compose.e2e.yml \
  -f docker/compose.trusted-pilot.yml build nanoflared nanoflare-runner
# Supply this staging environment's credentials/configuration before starting.
docker compose -f docker/compose.yml -f docker/compose.e2e.yml \
  -f docker/compose.trusted-pilot.yml up -d
```

Use an isolated staging environment and operator-managed secrets. Set the
secret-encryption key, control/runner/Traefik credentials and matching metrics
credential/file as described in the [operator notes](workers-for-platforms-operations.md).
Configure Grafana's operator credentials before allowing remote access. The
Compose development credentials are local examples, not production credentials.
This task has not deployed the stack, published images, changed versions or
created release tags. Public release uses the existing Release Please workflow.

| Physical component                 | Memory  | Additional swap | PID/task limit | Restart        |
| ---------------------------------- | ------- | --------------- | -------------- | -------------- |
| Control plane                      | 512 MiB | 0               | 128            | unless stopped |
| Runner and its runtime descendants | 2 GiB   | 0               | 128            | unless stopped |

These are separate container failure boundaries. Logical namespaces do not
choose another physical pool: customers using this runner share its memory and
failure impact. There is no per-customer physical memory guarantee, and the
64-entry/32 MiB descriptor cache is not a native isolate eviction bound.

The override sets `NANOFLARE_PLATFORM_DISPATCH_ENABLED=true` and
`NANOFLARE_WORKERD_EXECUTABLE=/usr/local/bin/workerd-native`. Command-line flags
take precedence. The experimental management adapter needs its own explicit
`--enable-cloudflare-compatibility` or
`NANOFLARE_CLOUDFLARE_COMPATIBILITY_ENABLED=true`; enabling it without native
platform dispatch fails before startup. It retains the required
`X-Nanoflare-Execution-Profile: trusted_shared_workerd` acknowledgment for control
mutations. See the [adapter reference](../architecture/cloudflare-management-adapter.md).

If outbound traffic must use a corporate proxy, the base Compose configuration
forwards the explicit proxy, CA and no-proxy settings to both services. Mount
configured CA files read-only in **both** the daemon (dynamic outbound) and
runner (static outbound); see the [proxy setup example](../../README.md#corporate-proxy-egress).
The no-proxy list selects routing after destination authorization; it does not
grant access to otherwise denied addresses.

## Admission and measured headroom

The current authority/admission limits are 32 live leases per customer, 128 per
namespace and 1,024 globally, with 100 shared effects, nesting depth eight,
30-second wall authority and at most two seconds of post-response authority.
That retained authority consumes admission even for fast responses. In the
simple fixture, sustained rates approach roughly 16 requests/s per customer or
64 per namespace; 15-second slices average about 17/68 because they include an
initial burst. This is admission policy, not engine CPU throughput. Plan for
429 backpressure rather than assuming Cloudflare-scale throughput from this
pilot. CPU limits/accounting remain unavailable and are rejected when requested.

The integrated Linux fixture loads 1/16/64/128 distinct customer Workers and runs
1/8/32 clients in fifteen-second slices, then churns 16 revisions and rolls back.
The 1 GiB comparison completed but reached its hard memory bound, recorded
71,973 `memory.events max` events and dropped to about 54.8 successful requests/s
in the final slice. It has inadequate headroom for this fixture.

The selected 2 GiB run peaks at about 1,103 MiB for the **whole probe**, with no
memory-limit or OOM events and no incorrect/error responses. The native RSS
sample at 128 Workers is about 929 MiB; after churn about 973 MiB. Source cache
scans and rollback retain the observed original native isolate. These small
modules, memory-backed control fixtures and bounded slices do not establish a
safe maximum Worker count, long-term soak or general application headroom.
Paid organization Worker counts remain logically unlimited; size and exercise
actual applications before admitting them. Large heaps or accumulating warm
revisions can still exhaust the shared pool.

The [Linux matrix](../../scripts/wfp-qualification/results-step10-linux-pool.json)
passes 89 tests/subtests without skips, including native local/remote resource
flows, memory/PostgreSQL authorization, publication/recovery and the real SDK
7.3.0 on Node 22. The [2 GiB report](../../scripts/wfp-qualification/results-step10-linux-pool-2g.json)
and [capacity detail](../../scripts/wfp-qualification/results-step10-linux-capacity-2g.json)
retain the observations. The separate real-process drills cover PostgreSQL/MinIO
crashes and interrupted publication; do not treat memory fixtures as S3 capacity
measurements. Broader workloads and multi-host failure/retention qualification
are needed before production expansion.

## Preflight and visibility

- Confirm the intended image/runtime versions and Linux cgroup v2 bounds. Inspect
  actual deployment limits; a configured value alone is insufficient evidence.
- Check the supported capabilities and trusted profile through the native API.
  Keep untrusted execution unavailable and reject unsupported CPU/cache/transport
  options rather than substituting elapsed timing.
- Verify a real authenticated dispatch, pinned secret/asset values and authorized
  customer output. `/healthz` is process liveness, including an empty runner.
- Verify Prometheus's authenticated scrape and operator-only Grafana/Loki access.
  Check `nanoflare_runner_resources_available=1`, fresh sample age, finite memory
  and PID limits and the collection-loss counter.

Runner status is authenticated. The daemon polls it every five seconds and
exports supported physical cgroup measurements; unavailable or older-than-20s
samples do not become zero memory usage. The rules in
`docker/prometheus/nanoflare-alerts.yml` flag unavailable/stale data, missing finite
bounds, sustained memory/PID usage above 80%, observed OOM kills and log loss.
They are loaded into Prometheus; configure the deployment's Alertmanager routing
separately if notifications are desired. These are process/cgroup counters, not
per-Worker usage or billing records. A recreated cgroup resets its counters.

The monitoring drill exercises actual daemon → Unix socket → Vector → Loki →
authorized output, secret redaction, producer-forgery denial, readable protected
Prometheus credentials and Grafana authentication. It also verifies a real log
loss alert and stale sampling during a paused runner while metadata stays
available. Collection is lossy: an event emitted while the collector is stopped
is dropped, and a new event after restart is collected.

## Recovery and rollback

1. Remove a repeatedly failing application's ingress or roll it back through the
   authorized platform/native deployment workflow. A shared pool can fail again
   if the same high-memory or CPU-bound trusted code is immediately dispatched.
2. A native-child failure triggers the manager's restart loop. A full runner
   restart changes its boot identity; the daemon reconciles durable publication
   intent and active snapshots. Wait for actual successful dispatch after health
   returns, not only the process-liveness endpoint.
3. If boot reconciliation keeps failing, inspect runner authentication, PostgreSQL
   and object storage. Policy/artifact unavailability fails closed. Restarting the
   control plane performs guarded startup recovery; it must use the same durable
   store and secret-encryption key. Do not bypass authorization to restore traffic.
4. Roll back using the existing pinned deployment/traffic operation. Verify code,
   selected secrets and assets together. Do not redeploy old source with today's
   mutable secret values or replay a successful old operation to alter traffic.
5. For interrupted publication, incomplete `building` recovers to failed and the
   old active revision remains; use a new idempotency key for a fresh attempt.
   A complete `stored` intent can finish publication after restart with its same
   deployment identity. Inspect the durable publication stage before retrying an
   operation whose outcome was unknown.

The selected 2 GiB OOM drill deliberately retains touched 32 MiB arrays across
customer isolates until the cgroup kills the native engine (59 Worker attempts
in the recorded run). Control metadata stays available, authenticated telemetry
reports the actual OOM count, and automatic native/runner recovery restores the
pinned revision, PostgreSQL KV and MinIO asset. This is a failure-containment and
recovery check, not a recommended tenant count or zero-downtime guarantee.
Control-process/PostgreSQL/MinIO SIGKILL, persisted rollback and idempotent replay
are separately exercised by the [process drill](../../scripts/wfp-qualification/README.md).
