# Workers for Platforms operator guide

For the person running Nanoflare's servers. Application developers can start
with the [integration guide](workers-for-platforms.md) and usually do not need
this page.

Updated 2026-10-22. The current profile is opt-in `trusted_shared_workerd`.
Customers share a workerd failure boundary; CPU interruption and hardened
untrusted execution are unavailable. Bounded local qualification does not
establish production capacity or Cloudflare portability.

This guide covers enabling the feature, monitoring it and recovering service.
Detailed experiment results stay in the [qualification records](../archive/workers-for-platforms/workers-for-platforms-step-10-status.md).

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
credential/file using the metrics instructions below and your deployment configuration.
Configure Grafana's operator credentials before allowing remote access. The
Compose development credentials are local examples, not production credentials.
Public release uses the existing Release Please workflow.

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
mutations. See the [adapter reference](../reference/cloudflare-management-api.md).

If outbound traffic must use a corporate proxy, the base Compose configuration
forwards the explicit proxy, CA and no-proxy settings to both services. Mount
configured CA files read-only in **both** the daemon (dynamic outbound) and
runner (static outbound); see the [proxy setup example](../../README.md#corporate-proxy-egress).
The no-proxy list selects routing after destination authorization; it does not
grant access to otherwise denied addresses.

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

## Authenticated Prometheus scrape

Production daemon wiring requires a separate operator bearer credential for
`/metrics`. Set `NANOFLARE_METRICS_TOKEN` (or `--metrics-token`) on nanoflared.
An empty credential disables the endpoint with 404; an incorrect credential
returns 401. Organization and runtime tokens do not authorize the global exporter.
Legacy embedded development APIs without control authentication retain their
legacy unauthenticated behavior; do not expose those as production control APIs.

Generate a random credential into an operator-managed file outside the repository
with restrictive permissions. Configure the same value on the daemon and set
`NANOFLARE_METRICS_TOKEN_FILE` to that file for Docker Compose. Prometheus reads
it from `/run/secrets/nanoflare-metrics-token`. The checked-in empty example is
deliberately unusable. Restart/recreate the monitoring service after changing
the mount configuration, then verify its scrape target and a rejected request
without the operator credential. The supplied Prometheus host port is loopback.
The production Compose target is `nanoflared:8080`; the separate development
config targets the host-run daemon. Compose passes `NANOFLARE_METRICS_TOKEN` to
the daemon, but the operator must still supply the matching token file. That file
must be readable by Prometheus's UID 65534, without making it world-readable.
The Linux fixture verifies owner `65534:65534`, mode `0400`; an equivalent
operator-managed ownership/group arrangement is also possible. A host file at
mode `0600` owned by an unrelated UID is not readable by the default container.

`nanoflare_log_forwarder_dropped_total` reports daemon-side attributed events
lost when its local collector cannot accept them or its queue is full. It is an
operator counter, not a customer billing record; restart resets it. Collection
remains lossy. A collector that accepts a connection but stops reading has a
bounded write deadline, and forwarder shutdown joins that operation.

## Resource limits and alerts

The current admission bounds are 32 live leases per customer, 128 per namespace
and 1,024 globally. Each invocation has a 30-second root wall deadline, at most
two seconds of background authority, 100 shared effects and nesting depth eight.
Background authority occupies admission slots even after a fast response.
Handle 429 responses with backpressure and bounded retries. These are policy
bounds, not CPU limits or a throughput guarantee.

The pilot's 2 GiB runner passed its bounded local workload; the 1 GiB comparison
reached its memory limit. Size the pool using actual applications, heap sizes,
revision churn and sustained load. Neither a descriptor cache nor a tested
Worker count guarantees native isolate memory headroom.

Authenticated runner status reports supported Linux cgroup v2 resource usage.
The daemon polls every five seconds; samples older than twenty seconds are marked
unavailable and old usage is omitted rather than reported as zero.
`nanoflare_runner_resources_available` and sample age identify missing data.
Memory and PID metrics cover the runner and runtime descendants; they are not
per-customer accounting. The OOM counter resets when its cgroup is recreated.

`docker/prometheus/nanoflare-alerts.yml` contains rules for stale/unavailable
samples, missing finite bounds, memory/PID pressure above 80%, OOM kills and log
loss. Configure the deployment's Alertmanager notification routing separately.
Keep Grafana, Loki and data-store/admin consoles restricted to operators. The
production Compose host bindings are loopback; configure credentials and an
authenticated access path for remote administration.

## Customer output

Use organization-authorized Worker output endpoints. The backend validates
returned Worker/revision identities and reapplies filters; a client-selected
filter cannot select a different customer. Limits are 1,000 lines, 512 bytes of
search text, a 48-hour range, 16 concurrent queries and a ten-second deadline.

Authenticated invocation transport supplies log identity. Customer console
messages carry hashed correlation identifiers rather than bearer capabilities.
Known pinned secrets and runtime credential patterns are redacted before
collection. Optional `--worker-log-redact-patterns` accepts a JSON array of
regular expressions. `--worker-logs-enabled=false` disables collection and
`--worker-log-sample-percent` controls sampling (0–100). Per-Worker and invocation
quotas bound collection; this is intentionally lossy telemetry. Oversized
messages are dropped whole to avoid leaking a partially truncated secret.
Initialization logs outside an invocation context are unavailable; this is not
complete exception or module-initialization tracing.

Production Vector accepts structured records over a Unix collector. Customer
text and Docker log markers cannot promote themselves into Worker identity.
The production Compose socket directory is owned by `0:65532`, mode `0710`,
and the socket uses `0:65532`, mode `0660`. Only trusted UID 0/GID 65532 producers
should mount it. Daemon/runner mounts are read-only, so producers can connect
without replacing the socket. Membership in that group authorizes submission
of producer identity; do not give it to another service or an untrusted process.
These permissions and a different UID's failed connection are verified against
the pinned Linux Vector image. Vector's [socket source options](https://vector.dev/docs/reference/configuration/sources/socket/)
describe the socket mode setting; configuration validation and the actual
mode/connection checks establish behavior for the repository's pinned version.
The separate development Vector
configuration enables a loopback TCP collector for trusted local processes; it
is not an authenticated boundary for arbitrary local users or customer code.
Loki and the global monitoring infrastructure must remain operator-only.

## Duration metric migration

Replace `nanoflare_worker_cpu_time_milliseconds_p90` in external dashboards and alerts
with `nanoflare_worker_duration_milliseconds_p90`. The API uses `duration_p90_ms`
and `cpu_accounting_available: false`. Duration includes handler elapsed time;
dynamic dispatch includes admission, code loading, I/O and response streaming.
It must not be interpreted as CPU usage. The old CPU-named metric is removed.
Across Workers, use a maximum of per-Worker p90 values when showing the highest
Worker p90; their sum is not an aggregate percentile.

Dynamic admission/completion/cancellation counters are process-lifetime values,
bounded to 4,096 Worker identities. Restart resets them; they are not durable
billing records. Ingress requests and customer dispatches are different events.
Publication history separates configuration publication from verified runtime
readiness. The runner version is reported as unreported until actually attested.

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

Runner `/healthz` reports process liveness, including an empty runner.
Authenticated `/v1/status` supplies a fresh boot identity; daemon reconciliation
requires the same-version runner and durable artifacts. Look for `remote runner
reconciliation` errors and check runner authentication and storage. An unavailable
PostgreSQL server can also reject authentication: a 401 during an outage does not
always mean the user credential changed.

Object/asset I/O carries invocation cancellation and a default 30-second complete
MinIO-operation bound. A timeout does not prove that a remote write rolled back;
reconcile durable state before retrying an ambiguous mutation.

See the [qualification drills](../../scripts/wfp-qualification/README.md) for
repeatable recovery/monitoring checks, and [remaining work](../plans/workers-for-platforms-remaining-work.md)
for production soak, client portability and isolation gates.
