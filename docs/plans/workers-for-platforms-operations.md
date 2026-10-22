# Trusted platform pilot: visibility and migration

The platform feature remains disabled by default. These notes describe local
operational behavior; production storage, capacity and rollout qualification
remain step 10 work. Untrusted execution and real CPU enforcement are unavailable.

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

The [monitoring qualification fixture](../../scripts/wfp-qualification/README.md)
verifies actual native logs through Vector and Loki into the authorized output
API, pinned-secret redaction, customer separation and resistance to Docker/raw
text producer forgery. It also exercises the real Prometheus scrape/token file,
collector outage/drop reporting and collection recovery after Vector restart.
Production retention, sizing, runner resource alerts and deployment-specific
access/placement still need their own configuration and checks.
Production Compose disables anonymous Grafana access and binds Grafana,
Prometheus, data-store/admin consoles, Tempo and the Traefik admin dashboard to
loopback. Configure operator credentials and an authenticated access path before
using these tools remotely. The development Grafana remains a trusted local
anonymous view on loopback. These bindings do not change public Worker ingress
or the authenticated control API.

## Capability checks and release gates

The remote runner's `/healthz` reports process liveness, including an empty
runner after restart; it does not establish loaded-generation readiness.
Authenticated `GET /v1/status` returns a random, non-cacheable process `boot_id`.
The daemon polls it every five seconds and reconciles a fresh/changed runner
from durable publication intent and active artifact snapshots. Failures log
`remote runner reconciliation` and retry without acknowledging the boot.
Unchanged boot IDs do not trigger periodic generation publication. Native-child
failure within a live runner uses the runtime manager's existing restart loop.
These intervals are retry policies, not recovery-time SLAs.

Startup and restart reconciliation hold the repository publication guard across
intent recovery, snapshot loading and publication. Concurrent deployment,
deletion and rollback cannot be undone by a stale recovery snapshot. Run the
same-version control plane and runner; an older runner without `/v1/status`
cannot support automatic boot recovery and will produce status errors. During
an incident, verify actual authenticated dispatch plus the pinned revision and
asset after runner health returns. If reconciliation keeps failing, inspect
PostgreSQL/object storage availability and operator logs; restarting the daemon
also performs guarded startup reconciliation. Keep traffic unavailable when
authoritative artifacts/policy cannot be recovered.

The [Linux failure fixture](../../scripts/wfp-qualification/README.md) verifies
separate capped-runner failure, recovery and pinned rollback with real
PostgreSQL/MinIO. Its 256 MiB limit is deliberately small and is not a pilot
capacity recommendation. The regular Compose files still need explicit
deployment-specific resource bounds and measured headroom before pilot rollout.

The [process/storage crash drill](../../scripts/wfp-qualification/README.md)
also kills the actual control-plane process and its disposable PostgreSQL/MinIO
servers. It checks recovery from both an incomplete `building` operation and a
fully `stored` immutable artifact, plus persisted rollback and idempotent replay.
Process health alone is not a storage readiness check. An unavailable PostgreSQL
server can reject authentication as well as dispatch; inspect dependency health
before treating every 401 during an outage as an invalid user credential.

Runtime asset/object I/O now carries invocation cancellation/deadlines into the
MinIO transport. Each complete MinIO operation, including retry and body read,
also has a default 30-second bound (`MinIOConfig.OperationTimeout` can select a
different positive bound for an embedded store). Other storage providers must
implement `ContextObjectStore` to carry cancellation into their own I/O; the
legacy synchronous interface remains compatible with existing fixtures. A client
timeout does not establish that a remote write was rolled back. Resolve ambiguous
mutations using durable state and the documented idempotency mechanism.

Inspect `/v1/organizations/{orgID}/workers/{workerID}/capabilities` before relying
on features. Dynamic Workers report shared trusted isolation, unavailable CPU
accounting/limits, a 30-second wall budget, a two-second background window,
100 shared effects and depth eight. Wall expiration revokes broker authority;
it cannot forcibly interrupt CPU-bound JavaScript in this profile.

Do not promote this local pilot as production-safe untrusted execution.
Cloudflare portability remains unverified while no entitled staging account is
available. See the [live comparison record](workers-for-platforms-live-comparison.md)
and [implementation plan](workers-for-platforms-implementation.md).

## Selected physical pilot profile

The [pilot runbook](workers-for-platforms-trusted-pilot-runbook.md) packages the
explicit Compose override and its measured limits: separate 512 MiB control and
2 GiB runner containers, no extra swap, 128 tasks each and native executable
selection. The ordinary daemon stays disabled by default. The experimental
Cloudflare adapter has its own disabled-by-default flag and requires native
trusted dispatch; its trust acknowledgment remains mandatory.

Authenticated runner status includes supported Linux cgroup v2 measurements.
The daemon's five-second poll caches a copy; the exporter marks a sample older
than twenty seconds unavailable and omits old resource usage rather than showing
zero usage. It distinguishes unavailable measurements from measured unlimited
bounds. The reader resolves its process's own cgroup, not the entire host's
mount root. Memory includes the agent, runtime descendants and charged cache;
PID count includes threads. None of these are per-customer or CPU measurements.

`nanoflare_runner_memory_current_bytes`, optional peak/finite limit,
`nanoflare_runner_pids_current`, optional finite PID limit and
`nanoflare_runner_oom_kills_total` are available only with a fresh supported
snapshot. `nanoflare_runner_resources_available` and sample age diagnose missing
or stale data. Rules in `docker/prometheus/nanoflare-alerts.yml` cover 80% memory/
PID pressure, unavailable/stale data, missing physical bounds, observed OOM kills
and collection loss. They are loaded in production/development Prometheus;
configure notification routing separately. The OOM counter resets with cgroup
recreation, and a short-lived event between samples is not a complete audit trail.

The one-GiB comparison reached its limit despite correct responses. The selected
two-GiB bounded run has measured headroom for its small fixture, not a guaranteed
maximum application count. The separate two-GiB forced OOM drill confirms failure
impact crosses logical namespaces on that same physical runner and control
metadata remains outside the failure. See the runbook before admitting actual
workloads or expanding beyond the local trusted pilot.
