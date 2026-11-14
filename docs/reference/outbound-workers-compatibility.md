# Outbound Workers compatibility contract

Contract `2026-11-14.1` describes the local outbound implementation and the evidence
needed to align it with Cloudflare. The executable [case manifest](../../scripts/wfp-client-flows/outbound/cases.json)
is the maintained inventory. It records the current qualified surface and remaining gaps; a passing local group never establishes hosted
Cloudflare equivalence. No WFP-entitled account or dashboard was available.

## Evidence levels

| Level                      | Meaning                                                                                                        |
| -------------------------- | -------------------------------------------------------------------------------------------------------------- |
| `documented`               | An identified official document or API schema states the behavior.                                             |
| `upstream-client-observed` | A pinned official client's schema or actual serialization was inspected/executed offline.                      |
| `locally-qualified`        | A named Nanoflare fixture exercises the stated local coverage. Check its run report for skipped prerequisites. |
| `hosted-unverified`        | Cloudflare runtime behavior still needs an entitled hosted comparison. This can coexist with the other levels. |

Every report identifies its contract, manifest hash, source commit, dirty state,
Node version, client versions and probe hashes. Local Go groups record passes,
failures and skipped subtests independently. PostgreSQL, workerd or client skips
must remain visible; a green parent test is not evidence that a skipped backend ran.

## Established upstream surface

The [outbound guide](https://developers.cloudflare.com/cloudflare-for-platforms/workers-for-platforms/configuration/outbound-workers/)
attaches an optional policy to a dispatch binding, passes named context through
`get()` and exposes it through the policy environment. It disables customer
`connect()` and excludes Durable Object and mTLS binding fetches. The example
forwards requests and uses `waitUntil()` for logging. Its parameter example is
inconsistent: the declaration names `params_object`, while later code reads
individual fields. Preserve the named object locally; flattening remains unverified.

The [upload API](https://developers.cloudflare.com/api/resources/workers/subresources/scripts/methods/update/)
represents the target as `outbound.worker` with `service`, `environment` and
`entrypoint`, and declares names in `outbound.params`. Those fields establish the
selector surface; they do not establish export validation timing or environment
resolution semantics.

Pinned [Wrangler 4.54.0](https://github.com/cloudflare/workers-sdk/tree/wrangler%404.54.0/packages/wrangler)
accepts `service`, `environment` and `parameters` in its outbound configuration.
Its schema disallows other fields, including `entrypoint`. The offline probe
executes the actual upload serialization block: it forwards the environment
unchanged and maps parameter strings to `{name}` objects. An outbound service
environment cannot be treated as a Wrangler deployment-name suffix on this evidence.
Named entrypoints are an API/native compatibility target; this pinned Wrangler
configuration does not expose them.

Pinned [Cloudflare SDK 7.3.0](https://github.com/cloudflare/cloudflare-typescript/tree/v7.3.0)
types expose all three worker selectors. Its default multipart serializer emits
unindexed nested binding fields. An explicit JSON metadata File preserves the
complete structure, matching the existing client fixture's adaptation. This is
local client-wire evidence, not a claim that every hosted request behaves identically.

Pinned [workerd 1.20260911.1](https://github.com/cloudflare/workerd/tree/v1.20260911.1)
provides `globalOutbound`; its Cap'n Proto comment describes global fetch routing.
That comment alone does not establish interception of Node/native HTTP clients.
The separate engine probe must verify those paths and TCP denial. A static parent
can create request-scoped capabilities for loader-hosted code; dynamic loader
entrypoints and the loader itself cannot simply be transferred across workers.

## Restriction dispositions

| Initial restriction or decision                                                    | Disposition                                                                                                                   |
| ---------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| Target service/dispatch bindings rejected; private resource facade used            | Remove through ordinary Worker execution. Do not infer blanket Cloudflare restrictions from absent documentation.             |
| Target native networking denied                                                    | Restore ordinary executor networking, independently of customer TCP denial.                                                   |
| Customer native HTTP denied                                                        | Replace with qualified interception; retain denial until a safe engine bridge passes.                                         |
| Default outbound entrypoint only                                                   | Implement API/native named fetch selector with retained metadata and execution identity.                                      |
| Omitted/production environment only                                                | Removed: persisted service environments resolve to independent immutable Worker IDs, with separate code, secrets and traffic. |
| Target DNS profile rejected                                                        | Remove as ordinary Nanoflare capability preservation; Cloudflare equivalence has not been established.                        |
| Finite JSON-only parameters                                                        | Interim transport contract; compare non-JSON/capability values before changing it.                                            |
| 16 KiB, depth 64, 64 declared names                                                | Explicit executor limits; no equivalent Cloudflare numeric guarantee established.                                             |
| Collision/reserved-name rejection                                                  | Explicit deployment policy pending upstream precedence evidence.                                                              |
| Missing names read `undefined`; extra names rejected                               | Compare independently; do not derive from the example.                                                                        |
| Snapshot at `get()`; target revision/secrets pinned at admission                   | Compare mutation and update timing; preserve isolation while unknown.                                                         |
| Same-organization ordinary targets; immutable IDs; delete/recreate does not rebind | Authority/deployment policy, not a hosted lifecycle equivalence claim.                                                        |
| Propagation into nested calls and bounded recursion                                | Compare chains and resource authority separately from direct fetch.                                                           |
| Durable Object and mTLS exclusions                                                 | Preserve documented boundary when the underlying platform capabilities exist.                                                 |

The manifest's local text describes the current implementation and explicitly
scopes engine-only proofs separately from production qualification. When changing a restriction, update its local text,
coverage and report together. Do not erase the distinction between upstream
requirements, internal capability consistency and unresolved hosted behavior.

## Executable qualification

From the repository root, validate the inventory and inspect both installed
pinned clients without network access:

```sh
node scripts/wfp-client-flows/outbound/qualify.mjs \
  --wrangler /absolute/path/to/wrangler \
  --sdk /absolute/path/to/cloudflare \
  --report /tmp/outbound-contract.json
```

The arguments name package directories, not CLI files. Version mismatch fails.
The probe reads the actual Wrangler schema, evaluates only its isolated dispatch
upload block and invokes the SDK's actual multipart serializer with a fetch
function that permits only its `data:,` feature-detection request. It does not
load credentials or contact Cloudflare. Omitted clients are recorded as skipped.
Install the exact versions outside the repository as described in the
[existing client guide](../../scripts/wfp-client-flows/README.md).

Add `--local --race` to run non-native Go groups. Add `--native` to also run the
engine, production runtime and Wrangler workflow groups, serially. Supply
`NANOFLARE_TEST_WORKERD`, `NANOFLARE_TEST_WRANGLER_PATH`, and optionally a disposable
`NANOFLARE_TEST_POSTGRES_URL` according to that guide. `--native` requires `--local`.
Missing prerequisites are reported as skipped, never converted into a pass.
Use `--groups admission,persistence,sdk-runtime,sdk-postgres,wrangler-runtime,wrangler-postgres`
to run an explicit subset; omitted groups remain visibly skipped.

The optional deployment backend runs the same disposable fixture against either
management API. It requires explicit environment variables; it never discovers
credentials, enables billing, or modifies existing named Workers. It creates a
random namespace, customer, policy and dispatcher, verifies metadata readback,
invokes named object/scalar/array parameters, missing parameters and policy block,
and deletes its resources in `finally`. The hosted mode enables workers.dev only
on its new dispatcher. Other manifest cases remain unverified by this backend.

```sh
# Set NF_OUTBOUND_API_URL (including /client/v4), NF_OUTBOUND_ACCOUNT,
# NF_OUTBOUND_TOKEN, and NF_OUTBOUND_INVOKE_URL explicitly before execution.
# Hosted invocation template: https://{script}.YOUR_SUBDOMAIN.workers.dev
node scripts/wfp-client-flows/outbound/qualify.mjs \
  --backend hosted --report /tmp/outbound-hosted.json
# Run the identical fixture against Nanoflare with its API and invocation template:
node scripts/wfp-client-flows/outbound/qualify.mjs \
  --backend local --compare /tmp/outbound-hosted.json --report /tmp/outbound-local.json
node --test scripts/wfp-client-flows/outbound/backend.test.mjs
```

Use an existing entitled test account; no hosted execution has been performed.
Resource IDs are excluded by recording exact namespace/service identity match
booleans, while statuses, response bodies, parameter ordering and values are
compared unchanged. A difference fails the command; a case absent from either
report is `not-compared`. The backend tests validate request construction,
comparison and cleanup with offline mocks, not Cloudflare runtime behavior.
A wider side-effect/lifetime comparison remains pending. Do not use local
emulator success to fill in missing Cloudflare observations.

`TestWorkerdOutboundNativeHTTP` separately establishes native fetch and node:http
interception, binary POST body/authorization preservation, buffered cross-origin
302 method/credential handling and customer TCP denial on static and loaded hosts
with pinned workerd. Restricted descriptors are anonymous per invocation; the
proof does not claim warm isolate reuse. Streaming request bodies retain workerd's
native redirect replay limitation; the redirect case explicitly buffers its body.

## Remaining comparison cases

The manifest covers selectors, imported environment access, nested objects,
scalars/arrays, non-JSON values, missing/extra names, collisions, sizes, mutation,
JS/native HTTP, TCP, redirects, services/nested dispatch, exceptions, cancellation,
background work, revocation, weighted target updates and persistent recovery.
All locally implemented cases have named executable groups or recorded browser
evidence. The Durable Object/mTLS boundary remains a documented exclusion for
underlying platform capabilities not implemented here. Unknown hosted expectations
remain comparison gaps and are not converted into parity claims.

Service environments now have a persisted selection model. Full CPU interruption
and untrusted execution isolation are broader WFP dependencies, not claims made
by this outbound contract. The [alignment plan](../plans/workers-for-platforms-outbound-workers.md)
tracks the implementation sequence and console work.

## Current local qualification

`TestOutboundWorkersEndToEnd` now exercises ordinary target execution across
static/dynamic hosts and local/remote runners: target secrets/KV, service fetch and
RPC using the helper's own secret authority, named fetch export, own dispatch
binding, and target DNS profile. DNS assertions inspect trusted broker context;
customer-controlled query/header values cannot select a different profile. Target
networking is independent of the customer's TCP restriction. Restricted customer
and ordinary target descriptors are request-scoped; warm-isolate reuse is not
claimed by this implementation.

`TestOutboundPublicationPersistenceRecovery` injects an interrupted outcome after
activation, recreates the service and recovers the identical idempotent result. It
checks stored selectors/parameters, exact sealed secret references, deletion and
recreation without identity rebinding, historical retries after newer publication,
and failed route-publication rollback. Memory retains repository state across
service recreation; PostgreSQL closes/reopens its pool in the same isolated schema.
PostgreSQL coverage is established only by a report without its backend skip.

The real pinned SDK runtime workflow also uploads an outbound policy with a named
`Policy` fetch export, publishes its secret, reads the dispatcher binding back via
`scriptAndVersionSettings.get`, invokes nested parameters through both handler and
imported environment access, verifies a 403 block, and deletes the policy through
the compatible script endpoint. Ordinary script deletion uses the existing
`workers:write` authority, requires `force=true`, and rejects mutation preconditions.
The same fixture has explicit memory and PostgreSQL workflow groups.

Cloudflare distinguishes [deprecated Service Environments](https://developers.cloudflare.com/workers/wrangler/deprecations/)
from [modern Wrangler environments](https://developers.cloudflare.com/workers/wrangler/environments/),
which deploy distinct named Workers. An explicit deployed target name therefore
works for modern deployments. This does not establish that the legacy
`outbound.worker.environment` field should be rewritten into a suffixed name;
legacy service selection now resolves through a persisted relationship rather
than suffix rewriting. The eight-way memory/PostgreSQL runtime matrix and pool
reopen tests are recorded in `reports/environments-2026-11-14.json` under the
qualification fixture. Actual pinned Wrangler legacy deployment, secret management, outbound readback
and invocation pass on memory and PostgreSQL with race checks. Hosted Cloudflare
comparison remains unperformed.

Native restart coverage uses `TestOutboundRunnerRestart` and
`TestOutboundRunnerPostgresRestart`. Each kills a dedicated workerd child and
verifies automatic replacement with the same stored policy, named selector, own
secret/KV bindings and fresh request parameters. Remote cases additionally close
the runner HTTP server and runtime manager, recreate both at the same address,
assert a different boot identity, and run production `ReconcileRuntime` before
invoking again. This exercises in-process runner initialization and reconciliation,
not an OS-level daemon executable restart. Memory and PostgreSQL run the same
static/dynamic and local/remote variants.

`TestOutboundSharedBudgetAndDepth` qualifies the production Go admission/effect
APIs against memory and PostgreSQL. Customer admission, policy admission, customer
KV effects and policy forwarding consume exactly one shared root budget; exhausting
it returns HTTP 429 on the forwarding endpoint. A declared recursive ordinary
service binding reaches depth eight and rejects depth nine with HTTP 429. Customer
and target resource grants stay separate, and root cancellation cancels existing
effect contexts and revokes retained nested authority. These are explicit local
effect-budget/depth policies, not evidence of Cloudflare CPU interruption parity.
