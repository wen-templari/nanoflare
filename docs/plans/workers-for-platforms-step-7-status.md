# Step 7 — Outbound, cache and resource policy status

Status: **implemented and verified locally for the disabled-by-default trusted fetch subset**. This record does not qualify untrusted execution, real CPU interruption or a production rollout. Capacity and production-like storage qualification remain step 10 work.

## Database policy implemented

User SQL and migrations pass through a lexical statement gate before SQLite executes them. Supported commands are SELECT/WITH, INSERT/UPDATE/DELETE/REPLACE, ordinary CREATE/ALTER/DROP, EXPLAIN and a small metadata/foreign-key/user-version PRAGMA allowlist. The gate counts statements without treating quoted strings, quoted identifiers or comments as statement boundaries. ATTACH/DETACH, VACUUM, extension/filesystem functions, non-allowlisted PRAGMAs, raw transaction commands, virtual tables and trigger programs are unavailable in this initial subset. Table-valued PRAGMA calls follow the same allowlist. This is a supported SQL subset, not a general SQL parser.

Each operation uses the same dedicated SQLite connection for limits and execution. The engine's attached-database limit is zero; SQL length is 100 KiB, value length 10 MiB, expression depth 100, trigger depth 16 and worker threads two. Trusted schema is disabled. Busy timeout is 500 ms. Tests bypass the lexical gate to check that the actual connection cannot attach another tenant file.

Queries and migrations have a five-second wall deadline, including time queued for one of 32 concurrent operations across database files. Requests contain at most 100 statement entries. Returned rows are capped at 10,000 and serialized row data at 4 MiB across the whole operation, including batches. Batches roll back on result-limit errors. These are initial implementation bounds, not measured capacity or CPU accounting.

The runtime database path propagates request cancellation into SQLite. Dispatch effects additionally derive cancellation from the trusted ingress/parent invocation lifetime, so an already admitted query can be interrupted when the invocation ends. Effect admission still checks current resource grants, frozen bindings and the shared root statement budget.

## Outbound policy implemented

Private dynamic Workers retain native `globalOutbound: null`, including native raw TCP denial. Their global HTTP(S) `fetch` is a bootstrap-installed adapter using `AsyncLocalStorage` and an opaque per-invocation RPC broker. The internal runtime adds `nodejs_als` to the loader flags. Module-initialization fetch has no invocation and fails. Code descriptors retain stable revision/digest keys; neither a broker nor request-specific credentials enter cached loader configuration. Customer `WorkerEntrypoint` fetch methods use the same invocation context.

Every outbound request and redirect hop consumes a unit of the root/lease subrequest budget and revalidates the caller chain. Go derives the operation context from ingress/parent cancellation and the wall deadline. Client-supplied internal headers are stripped, the source transport bearer never reaches the origin, and customer Authorization is forwarded only as explicitly supplied. Cross-origin redirect hops remove Authorization and Cookie. Redirects have a 20-hop limit; unsupported URL credentials, WebSocket upgrades and custom `cf` fetch options are rejected. Native custom WFP outbound/CPU/limit dispatch options remain unavailable.

HTTP network authorization precedes corporate proxy/NO_PROXY routing. Every DNS answer must satisfy the operator network allow policy; mixed allowed/denied answers fail closed. One authorized literal address is used for the actual direct connection or corporate proxy absolute URL/CONNECT. HTTP Host and TLS server-name/certificate validation retain the original destination. The proxy never chooses a second destination DNS answer. DNS resolution is bounded at five seconds. Public policy excludes private, loopback, link-local, multicast, unspecified and selected reserved/translation ranges. Explicit administrator CIDRs and the `local`/`private` categories may authorize private destinations. NO_PROXY never grants authorization.

The existing corporate adapter now receives the same configured network allow policy in both daemon and standalone runner. Its raw TCP CONNECT path authorizes and pins the destination before either direct dialing or proxy tunneling. Dynamic customer raw TCP remains unavailable regardless of administrator allow rules. Corporate credentials and extra CA roots stay in the trusted Go service.

Authorized HTTP operations have 64 concurrent slots per transport, held until the response body closes; waiting observes caller cancellation. The initial transport deliberately disables connection reuse across requests to make destination pinning unambiguous. It selects the first authorized DNS answer and does not implement fallback to another answer. These choices need step 10 latency/capacity qualification. HTTP streaming forwards chunks and cancellation closes the upstream operation. Outbound request bodies are limited to 10 MiB; responses stream under the invocation deadline.

## Cache policy implemented

The private dynamic profile reports the Cache API as unavailable through explicit errors. Bootstrap installs this policy before user modules evaluate and hardens native cache prototype operations as well as the global access point. There is no shared zone cache or persistent revision cache contract. This is separate from the platform's immutable code-descriptor cache and resource bindings. Native tests verify default cache access, named cache access and direct prototype calls fail explicitly on both tested workerd versions.

## Resource isolation and sharing

Memory and PostgreSQL tests run simultaneous old/new invocation authorization against different KV, database, bucket and asset snapshots. A lease cannot switch to a newer binding or another revision's asset ID. Two platform customers can deliberately share a resource only with explicit grants for each customer: tests write/read actual shared KV values, a SQLite table and object bucket data. Private customer grants cannot be acquired by a different customer's deployment or effect.

Revoking one customer's current grants blocks KV writes, database execution and object reads from both of its pinned revisions, while another customer's explicit sharing grant remains valid. Revision-local assets remain attached to their selected artifact; resource-grant revocation is not an artifact deletion operation. All effects still inherit invocation lifetime and budgets.

Bucket key normalization occurs within the user key's own root before the authorized bucket prefix is joined, preventing a traversal key from reading, deleting or overwriting another bucket. Runtime object path values are decoded exactly once; percent signs and literal encoded delimiters retain their keys. Regression tests cover both behaviors. Presigned URL effects remain unavailable to dynamic Workers.

## Validation and following steps

Completed validation:

- Full Go suite with PostgreSQL and native local/remote runners: `/tmp/nf-step7-final-full2.log`.
- Race checks covering service, database, egress and API, using the native workerd executable: `/tmp/nf-step7-final-race-native.log` (all passed).
- Complete native dispatch/outbound/cache/cancellation scenarios on workerd 2026-10-18: `/tmp/nf-step7-outbound-current.log`; installed 2025-12-10 is included in the full suite.
- Memory/PostgreSQL sharing, frozen revision, asset identity and current revocation matrix; bucket traversal and once-only URL decoding regressions are included in the final full/race suites.
- Network address classification, mixed/rebinding DNS, redirect/NO_PROXY denial, TLS identity and literal-IP corporate CONNECT, raw TCP policy denial and admission cancellation tests are included in the final suites.
- Whitespace/diff checks passed. These are transient local logs, not live Cloudflare or production evidence.

An earlier race invocation picked the npm workerd wrapper and stalled during raw TCP test cleanup. Its test processes were stopped and its orphaned test runtimes removed. The final race evidence above uses the native executable directly, matching the full suite. No wrapper-based partial run is counted as a successful check.

Step 8 adds tools, effective capability reporting and customer telemetry. Step 9 local compatibility work may proceed, but its live Cloudflare comparison is blocked by account entitlement; see the [live comparison record](workers-for-platforms-live-comparison.md). Step 10 must qualify these bounds, failure modes, storage services and rollout procedures. CPU enforcement and untrusted execution remain separate release gates.
