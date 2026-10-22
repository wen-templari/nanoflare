# Native platform qualification

This opt-in fixture measures the implemented Nanoflare dispatch path, rather
than the earlier isolated Worker Loader PoC. It creates a disposable paid-level
organization in memory, one native platform dispatcher and private dynamic
customer Workers. Each script has a distinct platform-mode customer identity.
It uses loopback HTTP, actual native workerd, the runtime manager, revision
artifacts, fresh invocation props, deployment updates and pinned rollback.
It uses no Cloudflare account, production credentials or Internet destination.

The checked-in baseline/current reports record native workerd 2025-12-10 and
2026-10-18. `provenance-step10.json` records their SHA-256 values and the backend
source snapshot/probe hash at generation time. Future source changes must not
be presented as having produced those earlier reports without rerunning them.

## Capacity smoke qualification

Run one native suite at a time. The baseline native executable is selected by
PATH; npm wrapper executables are rejected.

```sh
NANOFLARE_RUN_CAPACITY=1 \
NANOFLARE_CAPACITY_REPORT=/absolute/path/to/results.json \
  go test ./internal/api -run '^TestPrivateDispatchCapacityQualification$' \
  -count=1 -timeout=3m -v
```

The fixture stores runtime version/binary SHA-256, host OS/architecture/logical
CPU count, point-in-time native RSS, first/repeat request latency, successful
throughput and admission rejections. It verifies every successful response's
Worker, revision and distinct request marker. Immediate repeated requests must
reuse their observed isolate identity. The report records, without assuming
either outcome, whether an isolate survives a 128-revision descriptor scan and
a later rollback.

Stages contain 1, 16, 64 and 128 Workers, with 1, 8 and 32 concurrent clients.
Each load slice lasts two seconds by default. Set
`NANOFLARE_CAPACITY_PHASE_SECONDS` to 1–15 to lengthen the slices; increase the Go
test timeout accordingly. Successful-request latency is reported separately from
429 admission rejections. Each rejected client backs off ten milliseconds; the
generator uses a bounded reusable connection pool. It does not retry rejected
requests as successes or call this an unrestricted maximum-throughput test.

The implementation retains bounded post-response authority for up to two
seconds. Those leases consume customer/namespace admission. Calibration waits
2.1 seconds for previous authority to drain and adds 25 ms pacing between first
and repeat requests. Load slices also start after a 2.1-second drain. These waits
are outside reported request latencies and slice throughput. This separates
initial loading observations from the measured admission behavior without
weakening the runtime's background authority bounds.

Finally, the fixture publishes and invokes 16 successive revisions of one
Worker, measures publication latency and RSS, rolls back to its original
artifact, verifies the original code, and requires that customer updates never
restart the platform dispatcher.

RSS is selected from native processes whose command line contains this fixture's
unique runtime configuration directory. Unrelated command lines are not printed
or retained. Samples are not peaks, and the 64-entry/32 MiB code-descriptor cache
is not a promise that native isolate memory is bounded by those values.

The reports remain local capacity observations. Memory metadata/object fixtures,
short load slices and a single macOS host do not qualify PostgreSQL/S3 behavior,
Linux pool containment, production soak, untrusted isolation, CPU enforcement or
an advertised maximum tenant count. Step 10 remains incomplete until those
applicable release gates are independently evidenced.

## Separate Linux runner recovery and memory failure

`TestPrivateDispatchLinuxRecoveryQualification` uses a separate Linux runner
container, a disposable MinIO container and an isolated schema in the supplied
disposable PostgreSQL server. It requires locally available images and never
pulls/builds an image. The current runner executable is compiled from this
checkout and mounted read-only over the image's older executable. The native
workerd executable comes from the immutable image ID recorded in the report.
The default paths/image are for Docker Desktop on arm64:

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -o /tmp/nf-qualification-runner ./cmd/nanoflare-runner

NANOFLARE_RUN_LINUX_RECOVERY=1 \
NANOFLARE_QUALIFICATION_RUNNER=/tmp/nf-qualification-runner \
NANOFLARE_TEST_POSTGRES_URL="$DISPOSABLE_POSTGRES_URL" \
NANOFLARE_LINUX_RECOVERY_REPORT="$PWD/scripts/wfp-qualification/results-linux.json" \
  go test -race ./internal/api \
  -run '^TestPrivateDispatchLinuxRecoveryQualification$' -count=1 -timeout=4m -v
```

Required local images: `nanoflare-e2e/nanoflare-runner:local` and
`minio/minio:RELEASE.2025-04-22T22-12-26Z`. Override the runner image with
`NANOFLARE_QUALIFICATION_RUNNER_IMAGE` and native executable path with
`NANOFLARE_QUALIFICATION_WORKERD` when qualifying another local architecture.
An incompatible binary/image or unavailable cgroup v2 fails qualification.

The runner has verified cgroup v2 limits of 256 MiB RAM, zero additional swap
and 128 PIDs, runs as UID/GID 65532 with all capabilities dropped, a read-only
root and bounded writable tmpfs directories. The host control API/PostgreSQL
remain outside this cgroup. This small limit is chosen to **force failure**;
it is not recommended production sizing. The test retains 32 MiB typed-array
pages in successive dynamic Workers until native pressure produces a cgroup
`oom_kill` increment, with a bounded number of Worker attempts derived from the configured cap. That count is not a safe
tenant count or a throughput benchmark.

Before pressure it verifies actual PostgreSQL KV and MinIO assets, kills the
runner process tree, observes unavailable dispatch and an empty restarted runner,
reconstructs service state, reconciles active snapshots and rolls back a pinned
revision/asset. It then verifies automatic native-child recovery after OOM and
automatic control-plane reconciliation after a full runner restart, keeping
the service/client alive. A fixed loopback-published runner port preserves the
configured address across restarts. The synthetic runtime broker listens on an
ephemeral host port reachable through `host.docker.internal`; it exposes only
the authenticated runtime-dispatch routes, no control management route or real
credential. MinIO uses synthetic disposable credentials and a loopback port.

Only uniquely named resources labeled `codex.task=wfp-step10` are removed, along
with the test's schema. No host directories, existing containers, credentials or
production Workers are modified. A forced controller termination may require
cleanup of that run's labeled resources. Run native qualification suites serially.
This does not kill/restart the control-plane process, PostgreSQL server or MinIO
server, inject network failures, establish hardened sandboxing, qualify the
monitoring pipeline or measure production admission/headroom. Those gates remain
open. The checked-in Linux report and its separate provenance bind this exact
observation to the source/executable hashes; earlier macOS capacity provenance
remains unchanged.

## Control-process and storage-server crash drill

`process_recovery.py` starts an actual host-native `nanoflared` process, a
separate Linux runner, and **its own** PostgreSQL and MinIO containers. It does
not use the shared PostgreSQL test server, user configuration or existing
credentials. It never pulls/builds an image. With the same local runner/MinIO
images as above plus `postgres:17-alpine`:

```sh
go build -trimpath -o /tmp/nf-qualification-control ./cmd/nanoflared
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -o /tmp/nf-qualification-runner ./cmd/nanoflare-runner
python3 scripts/wfp-qualification/process_recovery.py \
  --control /tmp/nf-qualification-control \
  --runner /tmp/nf-qualification-runner \
  --report scripts/wfp-qualification/results-process.json
```

The runner uses verified 512 MiB RAM, zero additional swap and 128 PID bounds.
This cap accommodates the small recovery fixture; it is not a capacity
recommendation. All published service ports bind loopback except the synthetic
runtime callback, which must be reachable through `host.docker.internal`.
The callback uses fixture-only capabilities/credentials. The daemon runs from a
temporary working directory with a minimal explicit environment, without loading
the repository's `.env` or user credentials. Its generated files and container
data are disposable. The fixture changes only its own organization's usage level
to paid through its own PostgreSQL server, to avoid free-tier admission affecting
the recovery test. No real account or billing is changed.

Assertions cover actual HTTP management/dispatch with persistent KV, pinned
secret/code/asset pairing, control-process SIGKILL/restart, PostgreSQL server
SIGKILL/restart, MinIO server SIGKILL/restart and a brief paused-object-server
transport stall. It then establishes exact durable publication stages through
read-only SQL observations: while object storage is paused, the `building`
operation is killed and must recover to `failed` with the old revision active
and a 409 retry; while the runner is paused, the `stored` operation is killed
and must finish publication with the same durable deployment ID after restart.
Rollback and replay of an older successful operation must also survive restart
without undoing the rollback.

The Python client's two-second outage deadline only establishes that no success
was observed while the dependency was unavailable. It is not proof that backend
work stopped, or a server response-time SLA. Separate
`TestMinIOCancellationStopsStalledTransport`,
`TestMinIOOperationDeadlineIncludesBodyRead`, `TestMinIOOpenHasOperationDeadline`
and `TestRuntimeStorageCancellationReachesBackend` verify actual S3 transport
cancellation/deadlines and runtime propagation on memory/PostgreSQL. MinIO uses
a 30-second complete-operation budget (including retries/body reads) unless its
embedding configuration supplies another positive timeout; earlier runtime
invocation deadlines and cancellation take precedence. This does not undo a
storage write that a server already committed before cancellation.

The fixture removes only its uniquely named resources labeled
`codex.task=wfp-step10-process`, stops its own control process, and deletes its
temporary directory. It records cleanup outcome, binary/image identities and
observations in the report. Forced controller termination may require cleaning
up that run's labeled resources and owned process. A real multi-host partition,
packet loss, every publication crash boundary, monitoring pipeline, soak and
production capacity remain outside this drill. The checked-in process report
has its own provenance; do not overwrite historical report provenance after
unrelated source changes.

## Linux monitoring pipeline

`monitoring.py` runs the current Linux daemon and runner with actual Vector,
Loki, Prometheus and Grafana, plus disposable PostgreSQL/MinIO. It requires the
local images used above and `timberio/vector:0.46.1-alpine`, `grafana/loki:3.5.0`,
`prom/prometheus:v3.5.0`, `grafana/grafana:12.0.2` and `node:24-slim`. No image is
pulled and no user credential or existing application configuration is used.

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -o /tmp/nf-qualification-control-linux ./cmd/nanoflared
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -o /tmp/nf-qualification-runner ./cmd/nanoflare-runner
python3 -B scripts/wfp-qualification/monitoring.py \
  --control /tmp/nf-qualification-control-linux \
  --runner /tmp/nf-qualification-runner \
  --report scripts/wfp-qualification/results-monitoring.json
```

The fixture uses a dedicated Docker bridge, loopback-published admin/control
ports and synthetic credentials. It reads the actual Compose Vector user and
entrypoint through configuration resolution in a clean environment with
`--env-file /dev/null`; service credentials from the user's environment are
not substituted. The production Vector source/transform/sink config is used,
with Docker log ingestion restricted to this fixture's forgery container so
unrelated host logs are never collected. The actual production Prometheus
config is used with a one-second fixture scrape interval. The scrape assertion
covers the `nanoflared` job, not the unrelated Traefik job. Grafana uses the
production anonymous-access setting and provisioned Loki data source with a
fixture-only operator password.

It verifies the Linux socket directory (`0:65532`, `0710`) and socket
(`0:65532`, `0660`), rejects a different UID/GID even with a read-only socket
volume mount, and proves a trusted GID 65532 producer can connect through that
mount and send an event to Loki. Membership in the producer group authorizes
identity submission; this is not a defense against another trusted producer or
an untrusted process given that group. Dynamic Workers still use the restricted
shared trusted profile, with untrusted execution unavailable.

Actual native dispatch emits two customers' logs with pinned secrets. Assertions
follow them through the authenticated broker, Unix forwarder, Vector, Loki and
organization-authorized output API. They verify redaction, customer separation
and that Docker JSON/customer marker text cannot assign authoritative producer
identity. Prometheus reads a `0400` token file owned by its real UID 65534 and
successfully scrapes the daemon container; unauthenticated/user-token exporter
requests fail. CPU-named Worker metrics remain absent. The fixture also checks
production admin-port loopback bindings and actual Grafana rejection of
anonymous global-data-source access.

Stopping Vector must preserve successful dispatch while increasing
`nanoflare_log_forwarder_dropped_total`. After restart, a new unique event must
reach Loki, while an event emitted during the outage remains dropped. The
separate `TestVectorForwarderBoundsStalledWriteAndJoins` race test verifies that
an accepting/non-reading collector cannot stall writes or shutdown indefinitely.
The forwarder is intentionally lossy and does not promise complete log delivery.

Containers, volumes and network are uniquely named and labeled
`codex.task=wfp-step10-monitoring`; the fixture removes only those resources and
its temporary data. The report records image/binary identities, observations and
cleanup outcome. Production retention, pool-resource alerts, sizing and extended
load remain outside this monitoring drill. Preserve its provenance separately
from older recovery and capacity reports.

## Selected Linux pool and sustained load

The current trusted pilot is described in the
[pilot runbook](../../docs/plans/workers-for-platforms-trusted-pilot-runbook.md).
Build the pinned runtime base with its architecture-independent native alias,
compile the Linux API test binary, and supply the already-installed isolated
`cloudflare@7.3.0` package directory. The driver never installs a package, pulls
an image or contacts a Cloudflare account:

```sh
docker build --pull=false --target runtime-base \
  -t nanoflare-qualification/runtime:step10-native .
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go test -c -o /tmp/nf-api-linux.test ./internal/api
python3 -B scripts/wfp-qualification/linux_pool.py \
  --probe /tmp/nf-api-linux.test \
  --image nanoflare-qualification/runtime:step10-native \
  --sdk /absolute/path/to/isolated/node_modules/cloudflare \
  --memory 2g --seconds 15 \
  --report scripts/wfp-qualification/results-linux-pool.json \
  --capacity-report scripts/wfp-qualification/results-linux-capacity.json
```

The initial image build may fetch the exact Dockerfile-pinned public dependency;
it is separate from the no-download test driver. The selected runtime is native
workerd 2026-09-11. Linux arm64 is the qualified architecture. The Dockerfile maps
Node's `x64` spelling to the package's `linux-64` name; other host/architecture
qualification is not inferred from the arm64 run.

The driver uses an internal-only network and a disposable PostgreSQL container.
Its selected authorization expression is recorded in the report; **no selected
checks may skip**, including SDK flows. It mounts only the public SDK package and
owned client fixture scripts, never user profiles or credentials. Matrix and
capacity run in separate non-root, read-only-root containers with bounded tmpfs,
128 tasks and equal RAM/swap-total bounds (no additional swap). Result volumes
are owned by the fixture and removed afterward. Captured container output belongs
only to these disposable tests. `--matrix-only` / `--capacity-only` select a phase;
`--memory 1g` reproduces the tighter-cap comparison.

The report retains source snapshot and test-binary identities, phase exit status,
pass/skip counts, cgroup bounds, whole-probe peak memory and memory events. Capacity
also retains the detailed Go probe report and its checksum. The whole probe
includes the Go control fixture, so its memory peak is not native-only RSS. Linux
RSS uses each owned process's `statm` resident-page count, multiplied by the actual
page size, with a proc directory handle to prevent PID reuse between reads.
Kernel RSS accounting is approximate; see the
[Linux proc reference](https://docs.kernel.org/filesystems/proc.html). It is not
peak RSS or a guarantee that the descriptor LRU evicts native isolates.

The 1 GiB run completed but repeatedly reached its memory limit; retain that
comparison rather than presenting it as a healthy pool size. The selected 2 GiB
run had roughly 1,103 MiB whole-probe peak and no memory-limit/OOM events. Both
use fifteen-second slices at 1/16/64/128 Workers and 1/8/32 clients. Known admission
policy, retained two-second authority and initial bursts explain the successful
rates. There is no general safe tenant count or CPU throughput claim.

For forced failure at the selected pool cap, the existing Go recovery fixture
accepts `NANOFLARE_QUALIFICATION_MEMORY_MIB=2048` with the selected image/native
executable. Allowed fixture sizes are 256, 1024 or 2048 MiB; the bounded pressure
attempt limit scales with the cap. It verifies the actual kernel OOM count through
authenticated runner status, and a peer in another namespace must lose its native
isolate with the same shared process while control metadata remains available.
Only the runner cgroup changes size; its MinIO fixture retains a separate bound.

The selected-profile monitoring fixture now resolves the pilot Compose override,
uses its native-path/environment opt-in and physical bounds, and loads the actual
alert file. It verifies a real collection-loss alert, and a paused runner's stale
measurements become unavailable without blocking control metadata. Use the new
runtime base image/native alias and current Linux daemon/runner binaries. The
experimental daemon adapter is explicitly enabled only in that fixture, where
mutations still require the trusted execution header; the pilot override itself
leaves the adapter off.

Alert expressions have separate `promtool test rules` coverage in
`runner-alerts.test.yml`. Mount it under `/tests/` and the production rules at
`/etc/prometheus/nanoflare-alerts.yml` in the pinned Prometheus image. The tests
cover threshold boundaries, pressure, OOM/log loss, stale/unavailable and missing
bounds, and counter reset. Alertmanager notification routing is operator setup;
no notification is sent by these fixtures.

New cleanup uses container removal with anonymous-volume removal, in addition to
owned named-volume/network cleanup. Older fixture `cleanup_verified` fields did
not establish that image-declared anonymous volumes were removed. No global
volume prune is used; only provably attributable, unreferenced task resources may
be cleaned. Historical runtime observations and their provenance remain intact.

## Combined adapter asset storage/load check

Build the current host daemon and Linux arm64 runner, then run:

```sh
go build -o /tmp/nf-assets-control-host ./cmd/nanoflared
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/nf-assets-runner-linux ./cmd/nanoflare-runner
python3 scripts/wfp-qualification/asset_storage.py \
  --control /tmp/nf-assets-control-host \
  --runner /tmp/nf-assets-runner-linux \
  --report scripts/wfp-qualification/results-asset-storage.json
```

This reuses the recovery fixture's disposable PostgreSQL/MinIO/native-runner
setup and explicitly enables the experimental adapter. Existing local images
are required; no external account or download is used. Four concurrent customer
sessions each upload 1 MiB mapped to two paths with a duplicate hash. The drill
checks completion replay, explicit/default content type, native SHA-256 and
length, daemon/MinIO restart and serving after exactly four staging records are
removed. Cleanup removes only fixture-owned resources, including anonymous
volumes. No existing database or deployment is used.

The report records binary, image, native and fixture-source identities. This
bounded correctness/load test does not establish production soak, tenant
capacity, hosted portability, empty asset support or cross-session deduplication.
The runner is bounded at 512 MiB/no additional swap/128 tasks; the daemon runs
on the host, so this is separate from the container-bounded pilot measurement.
