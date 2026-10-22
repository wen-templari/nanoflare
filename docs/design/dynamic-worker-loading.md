# Independent Worker publication

## Implementation status

Production `nanoflared` and `nanoflare-runner` use `runtime.DynamicManager`.
Application deployments, variable and secret revisions, and traffic changes
publish into a stable workerd dispatcher. Updating one Worker does not restart
workerd or reset another Worker's warm module state. Local and remote runner
modes use the same publication and execution contracts.

The dispatcher uses stock workerd **2026-09-11**, matching the production pin.
Worker Loader requires `--experimental`; the production launcher enables it.
The older static `runtime.Manager` remains for compatibility tests. Process
replacement is reserved for runtime upgrades and crash/session recovery. A
shared-process crash still interrupts every Worker hosted by that process;
independent publication does not provide process-level tenant isolation.

Workers for Platforms retains its existing opt-in trusted execution profile.
Its namespace dispatch, resource authorization, outbound policy, invocation
budgets, native props, nested services, logs and private-script behavior run
through stable parent bridges. Named platform RPC exports travel as descriptors
and are reconstructed as parent entrypoints; request-bound native channels never
enter cached WorkerCode. Named export calls receive their own admissions within
the original caller lifetime. HTTP error status survives the bridge explicitly.
Durable Objects remain outside this repository's existing runtime profile;
identity, storage ownership and migration must not derive from code revisions.

## Snapshots, authority and routing

`config.SnapshotWorkerCode` supplies the common compiler for static configuration
and dynamic loading, retaining Nanoflare's KV, D1, object and asset adapters,
compatibility flags, instrumentation, private DNS rewriting and egress settings.
`SnapshotWorkerCodeForLoader` also adapts legacy service-worker format. Loader
module entries use explicit `{ "js": "..." }` values; module-name collisions are
rejected instead of silently replacing entries during serialization.

A code snapshot alone is not an execution identity. `nanoflare.FreezeExecution`
also detaches the effective variables, resolved secrets, resource and service
binding declarations, deployment/asset references and compiler options. The
execution registry separates this content identity from resource authority. Each
runner session registers a fresh opaque resource grant for a snapshot. The grant
is also the Worker Loader cache key: republishing identical content after
retirement creates a fresh grant and isolate generation without reviving old
capabilities. Unchanged published revisions retain their grants and warm state.

Resource grants move through `prepared -> published -> draining -> revoked`.
Prepared candidates can compile but cannot admit invocations or access resources.
Publishing enables both; published revisions may access declared resources even
when idle. Retirement prevents fresh invocation admission. A draining grant
permits new resource operations only while at least one admitted invocation
remains. Each accepted resource operation takes its own pin until its HTTP
response finishes, protecting bundle and asset objects even after the last
invocation drains. That operation pin cannot authorize another operation.
Session expiry immediately denies new operations and admissions; revocation
removes the session's invocation pins while accepted operations can finish.

Registration uses a random caller nonce and returns both grant and snapshot
identity. Retries are idempotent within a session. The runner knows the grant ID
before sending registration, so an uncertain registration can be retired; a
session-owned cancellation tombstone rejects a registration arriving later.

Private execution control uses a runner credential separate from Worker
credentials. The control API is mounted only on the private runtime listener.
Local mode generates a credential when one is not configured; remote mode
requires the shared runner credential. Remote DTOs explicitly transfer secret
values and bundle object keys, which public deployment serialization omits.
The gateway and runner discard incoming reserved `X-Nanoflare-*` headers before
applying trusted dispatch metadata. Dynamic resource requests use a dedicated
private resource prefix so a KV key resembling a control or telemetry path
cannot reach those handlers.

Validation uses trusted dispatch metadata rather than an application pathname.
Before loading user modules, the loader installs concrete KV, D1, object and asset
facades in the base `cloudflare:workers` environment. Both imported `env` and
handler `env` refer to these revision-bound resource capabilities, including
bindings and methods captured at module scope. Resource access does not depend
on AsyncLocalStorage or callback registration style: raw WebSocket `onmessage`
and stream callbacks can use imported bindings. Worker code receives a parent
bridge capability; the opaque backend credential stays in the runner. The bridge
resolves binding names against frozen declarations and the private resource API
checks grant state and pins each accepted operation.

Service bindings retain invocation context for call lineage and returned RPC
lifetimes. User WorkerEntrypoint construction runs after admission inside that
context. WebSocket `addEventListener` callbacks preserve it, including listener
objects, removal and one-time registrations. Raw `onmessage`/`onclose`/`onerror`
handlers and ReadableStream callbacks capture their own originating context.
Accepted sockets retain the invocation through closure and asynchronous listener
work. Imported SERVICE bindings therefore use that socket or stream's caller,
never whichever unrelated request happens to be live. Native local/remote
qualification exercises service calls from raw `onmessage` and stream `pull`.
Candidate snapshots remain protected from cleanup until the pending publication
owns them, including while detached routing metadata is copied.

The Go gateway remains the traffic-selection authority, including weighted
traffic and preferred-revision routing. Its selected revision travels through
an authenticated dispatcher endpoint. The runner's committed snapshots implement
that routing decision; they do not introduce an independently editable traffic
allocation. Stable parent bridges resolve service targets and forward HTTP/RPC,
including returned capabilities, within the admitted execution context.

## Publication and recovery

The optional `PublicationValidator.ValidatePublication` contract hydrates and
validates candidates before activation. It includes DNS-profile validation and
actual runtime compilation. Invalid candidates leave persisted active traffic
and the prior running revisions intact.

Each Worker has a monotonic publication generation, desired weighted traffic and
a published-generation acknowledgment. In-memory storage updates them under its
repository mutex; PostgreSQL commits them in the same transaction as deployment
activation or traffic mutation. Configuration publication uses the existing
repository-wide publication guard. A rollback creates a newer generation rather
than restoring an old generation number.

Runtime publication acknowledges the generation after successful publication.
If publication or acknowledgment is interrupted, `Service.ReconcilePublications`
rebuilds from **current repository routing**, never from a stale operation's
traffic. It also completes interrupted vars rollback intent and pending vars
outcomes while preserving idempotency-key behavior. Failed traffic publication
restores both repository routing and runtime configuration.

Startup binds and serves the private runtime API before reconciliation, then
opens the public listener. This lets snapshot registration and admission use the
private API during recovery without exposing partially reconstructed routing to
public requests. The process supervisor reconstructs committed snapshots after
a workerd crash. Losing the runner control session revokes its resource authority
and stops the old runtime. Recovery opens a new session, registers fresh grants,
validates the current snapshots and republishes routing. Neither session loss
nor a process crash can revive grants from the old session.

## Execution lifetimes

An invocation is retained until its root response or upgraded connection closes
and its tracked background work completes. This includes nested `waitUntil`,
service calls and returned RPC capabilities. WebSockets and streaming responses
keep their admitted revision while later requests can use a replacement.

Generated invocation wrappers acknowledge start before user handlers execute and
completion after the tracked work finishes. Returned RPC methods use monotonic
invocation epochs: repeated acknowledgments are idempotent, and a late completion
from an earlier epoch cannot complete a newer method call. A call that never
acknowledges start can be abandoned without canceling already-started work.
Start/completion notifications retry transient transport and server failures.

Runner admissions use caller-chosen monotonic sequence numbers within a session.
A repeated admission resolves to the same live pin; the session watermark
rejects canceled or superseded late requests without retaining per-request
tombstones. Failed or uncertain releases remain queued for retry. Runner sessions
have a 60-second TTL, renewed by heartbeat; a 15-second janitor expires abandoned
sessions and releases their pins. The private listener's shutdown also revokes
its sessions. These mechanisms provide cleanup when a runner disappears instead
of depending solely on a JavaScript stream callback.

Returned capability facades forward public prototype methods, getters and
`Symbol.dispose` hooks while retaining their originating admission. Named
entrypoint constructors remain lazy. Native props retain supported non-plain
values such as dates and typed arrays across the parent bridge. RPC exposure
continues to use prototype members rather than arbitrary own instance fields.

This does not establish a general memory bound or force immediate workerd isolate
collection. Retired snapshot metadata can be removed when unreferenced, and
bundle/asset cleanup protects admitted executions, including shared object keys.
Storage garbage-collection policy and runtime memory-retention measurements are
separate work.

## Verified native-runtime coverage

`TestWorkerdDynamicPublicationEndToEnd` exercises the production Service,
DynamicManager, private execution/resource APIs and public Go gateway with the
pinned native binary. Both local and actual HTTP runner variants verify:

- Alpha deployment, vars and secret changes preserve Beta's warm counter and
  keep the same workerd process; invalid Alpha candidates leave active routing
  unchanged.
- Remote validation/prepare/commit, bundle object-key and secret transport, and
  trusted dispatch metadata work through `runner.Client` and `runner.Server`.
- Raw WebSocket property handlers and stream `pull` callbacks use imported KV.
  Two old sockets retain the old revision across replacement; closing one leaves
  the other usable, while a new socket uses the replacement.
- Retired `waitUntil` work uses its original variables, secret and KV authority
  after the replacement drops the KV binding. The old resource grant rejects
  operations once its final socket and background work drain.
- Interrupted-publication reconciliation uses current routing and acknowledges
  its generation without resetting another Worker.
- A real workerd process stop triggers one replacement process and restores the
  current environment and routing. Replacing the execution-control listener
  while workerd remains alive revokes the old session and triggers recovery with
  fresh grant identities.

`TestWorkerdDynamicLoaderContracts` additionally exercises the generated wrappers
and parent bridges: HTTP/RPC and nested returned capabilities; WorkerEntrypoint
instrumentation and duration attribution; KV, SQLite-backed D1, object and asset
operations; legacy service-worker fetch/cron; private DNS and configured egress,
including raw TCP; concurrent invocation isolation and stable resource grants;
exceptions; background work;
and static/dynamic upstream-disconnect behavior. Its object backend is an
in-memory object-store fixture, not a live MinIO deployment.

Two unsupported runtime transfers are explicitly tested as rejected: passing a
dynamically loaded entrypoint directly as another Worker's binding, and passing
a native KV namespace directly across the loader boundary. Both require stable
parent capability bridges.

Closing the actual client transport closes the upstream transport in both static
and dynamic cases. A JavaScript `ReadableStream.cancel` callback was not observed
in either diagnostic stream. The transport result is qualified; the callback is
not, and the lifetime implementation must not use that callback as its only
completion signal.

Focused Go tests cover transaction rollback, generation acknowledgments, weighted
traffic recovery, vars idempotency, resource scoping, grant generations, canceled registrations,
operation pins through response completion, session expiry, admission
sequence cancellation, lifecycle epochs, retryable retirement and release,
reserved-path isolation, gateway/runner header handling and cron lease headers.
The native local/remote publication test has also passed under the race detector.
The full Go suite, race checks for the changed runtime/API/config/runner/service
packages and command, and `go vet ./...` passed with the pinned native binary.
PostgreSQL integration is included when the connection setting below is supplied.
The merge qualification also runs `TestStableRuntimePrivateDynamicDispatch` in
both local and remote modes, plus regressions for cron preservation, streaming
flush/cancellation and redirect handling. Native publication checks validate
scoped log delivery and secret redaction. Publication uses current main's durable
artifact and prepare/commit contracts; startup reconciles deployment publications
and interrupted variable updates through the same guarded path.

## Running the checks

Use an already installed native production-pinned binary. The opt-in gates do
not download workerd; they skip when the variable is absent and fail for a
missing or wrong-version binary when explicitly enabled. The loader contract
client also requires Node.js 22 or later.

```sh
export NANOFLARE_TEST_WORKERD=/absolute/path/to/workerd
export PATH="$(dirname "$NANOFLARE_TEST_WORKERD"):$PATH"

go test ./internal/api -run 'TestWorkerdDynamic(PublicationEndToEnd|LoaderContracts)' -count=1 -v
go test -race ./internal/api -run 'TestWorkerdDynamicPublicationEndToEnd|TestStableRuntimePrivateDynamicDispatch' -count=1
go test ./...
```

Putting the pinned native binary on `PATH` also makes older real-runtime tests
use the production version instead of an unrelated installed workerd wrapper.
`NANOFLARE_LOADER_REPORT` can retain the loader fixture's machine-readable report.
That report describes its enumerated contracts; a fixture-only qualification
field is not a production rollout verdict.

PostgreSQL durability tests require `NANOFLARE_TEST_POSTGRES_URL` and an instance
where the test can create an isolated schema. They exercise persisted publication
generations/traffic, acknowledgment, transactional failure and durable vars
operations. Merge qualification used an isolated PostgreSQL 17 instance;
ordinary runs without that setting skip the database integration tests.

```sh
NANOFLARE_TEST_POSTGRES_URL=postgres://... \
  go test ./internal/database -run '^TestPostgresVarsActivationAndDurableRetries$' -count=1 -v
```

## Deployment isolation regressions

The cron scheduler is retained across publications; updating its future job list
does not cancel existing scheduled requests. Retirement calls run outside the
admission mutex so a delayed cleanup cannot stall other Workers. The resource
bridge flushes response chunks, propagates client cancellation, and returns
redirects without following them. The immutable artifact secret snapshot is
preserved during explicit rollback and later crash recovery.

## Merge qualification — 2026-10-22

Integrated with main `7407b14` (Cloudflare-format configuration). Qualified on
macOS arm64 with native workerd 2026-09-11 and an isolated PostgreSQL 17 instance.

- `go test -race ./... -count=1 -timeout 180s`: passed with the native runtime
  and PostgreSQL integration gates enabled.
- `go vet ./...`: passed.
- Native local/remote publication and Workers for Platforms scenarios: passed.
- Loader contract matrix: 34/34 passed, including RPC getters and disposal hooks.
- API JSON matches fresh generated OpenAPI structurally; schema typecheck passed.
- Regression coverage includes empty asset bindings, in-flight cron preservation,
  candidate admission waiting without blocking peers, raw WebSocket/stream service
  callbacks, native props types, redirects, streaming cancellation, secret
  redaction, runner-session isolation and PostgreSQL activation transactions.

The experimental Worker Loader and shared-process failure domain remain the
same explicit trusted-runtime constraints; deployment isolation does not imply
CPU, memory, or process isolation between tenants.
