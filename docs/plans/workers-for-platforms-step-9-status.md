# Step 9 — Cloudflare management compatibility adapter

Updated 2026-10-22. Status: **local supported subset implemented; live acceptance gate open**.
The [step 9 audit](workers-for-platforms-step-9-audit.md) records requirement-level
evidence and the blocked live comparison. Upload translation, persistent management state and atomic managed
publication exist with local tests. Core HTTP routes are implemented behind an
explicit disabled-by-default server option. Settings/secret/tag mutations are
implemented locally; module content, single-tag filtering and the bounded asset
session/upload/publication flow and bounded expiry cleanup also pass local checks.
Representative SDK flows pass with explicit multipart adaptation. A bounded
real PostgreSQL/MinIO asset load and restart drill now passes through the
adapter and native runner. Production soak and broader client qualification
remain incomplete outside the enumerated flows. This is not a claim of drop-in Wrangler/SDK compatibility or
live Cloudflare portability.

The normal daemon now exposes an explicit experimental compatibility flag,
disabled by default and requiring native trusted dispatch. A real Linux daemon
fixture verifies its mutation trust acknowledgment. The Linux SDK 7.3.0 check
also found and fixed Node 22 rejecting unquoted multipart content parameters;
responses now quote them, and SDK flows pass on Node 22 as well as the earlier
Node 24 host fixture. See the [pilot runbook](workers-for-platforms-trusted-pilot-runbook.md)
and [step 10 evidence](workers-for-platforms-step-10-status.md). Live validation
remains blocked; no account entitlement, billing or deployment changed.

## Verified contract details

The current [dispatch script upload reference](https://developers.cloudflare.com/api/resources/workers_for_platforms/subresources/dispatch/subresources/namespaces/subresources/scripts/methods/update/)
and [multipart metadata guide](https://developers.cloudflare.com/workers/configuration/multipart-upload-metadata/)
define metadata plus module parts. The adapter must translate their wire
representation into the native complete snapshot and reuse native authorization
and publication, rather than introducing an independent runtime path.

The upload reference now distinguishes the **latest uploaded revision** from the
revision serving traffic. `inherit` defaults to latest, permits an optional
binding rename, and script upload accepts only the `latest` version selector.
Missing inheritance is dropped by default; `bindings_inherit=strict` makes it an
error. The adapter must persist its latest-upload pointer independently from
weighted traffic and rollback. Selecting the active native revision as the
inheritance source would be incorrect.

The [bindings guide](https://developers.cloudflare.com/cloudflare-for-platforms/workers-for-platforms/configuration/bindings/)
and upload reference describe type-based `keep_bindings`. Preservation and
explicit replacement must be resolved before publishing the native snapshot.
Existing native resource grants remain mandatory. Tags are metadata, not
customer identity or authorization. Cloudflare account IDs map to authenticated
Nanoflare organization IDs; Cloudflare credentials do not authenticate to
Nanoflare.

## Implemented locally

`internal/cfcompat/upload.go` parses bounded multipart uploads and translates
plain text, JSON, KV, D1, R2, fetch service and secret binding intent. Bucket and
service names require a caller-provided scoped resolver. Candidate secret values
are separated from public metadata and excluded from JSON serialization.

Translation resolves retained types, explicit replacement, renamed inheritance,
strict/unresolved inheritance, tag replacement/preservation and retained asset
snapshots. Omitted compatibility date uses the documented API default
`2021-11-02`; native validation still checks the effective runtime ceiling.
Completed asset-upload tokens now resolve through the scoped persistent service
described below; the parser cannot accept token claims as asset bytes directly.

Inputs are bounded to 10 MiB, 256 parts, 1 MiB metadata and 128 effective
bindings. Module part names determine identity; filenames cannot override it.
Only UTF-8 JavaScript modules are accepted by the current native source model.
Duplicate JSON keys, case aliases, duplicate parts/bindings, path traversal,
encoded parts, unknown fields and unsupported options are rejected. CPU intent,
classic scripts, arbitrary RPC entrypoints and unsupported resource types cannot
be silently accepted as implemented capabilities.

`internal/cfcompat/upload_test.go` verifies a complete native snapshot against an
independent expected request, secret omission from serialized results,
preservation/replacement/rename behavior, caller snapshot immutability, scoped
resolver failures, unsafe metadata, multipart identity and size boundaries.
The isolated suite passes with the race detector:
`go test -race ./internal/cfcompat` (`/tmp/nf-step9-upload-translation.log`).
These tests prove translation behavior, not authenticated endpoint equivalence.

## Persistent state and atomic publication foundation

`internal/nanoflare/management.go` and
`internal/database/postgres_management.go` add Worker-scoped management state to
both repositories: organization identity, optimistic revision, latest uploaded
artifact, typed binding metadata and tags. The PostgreSQL migration is additive
and cascades with Worker deletion. Immutable artifacts retain the management
metadata needed to reconcile retries. State getters return independent copies.

`ManagedDeployForOrg` publishes a complete selected secret set with code and
metadata through the native publication pipeline. Inline secret values are
bounded, encrypted into the artifact and excluded from stored request metadata.
A domain-separated keyed fingerprint supports retries without persisting
plaintext or exposing a simple secret-value hash. Binding metadata must match
the actual native binding names/types, variable values and KV/D1 identifiers;
secret binding metadata cannot include values. Resource ownership and grants
still use native checks. This path intentionally does not perform sequential
live secret mutations or publish intermediate revisions.

The latest-upload pointer advances when the complete artifact is stored, before
activation. If activation fails, traffic returns to the previous revision while
the fully stored upload remains the latest inheritance source. A metadata commit
failure blocks activation; restart recovery reconciles both a failed commit and
an unknown committed outcome. A newer upload supersedes an older pending
publication rather than allowing recovery to overwrite it or prevent startup.
Completed request retries cannot undo a later traffic rollback.

Evidence:

- `internal/api/managed_publication_test.go` passes with memory and PostgreSQL:
  complete code/secret pairing; secret rename from latest upload after rollback;
  stale/concurrent writer rejection; changed-secret idempotency conflict;
  organization isolation; metadata copy/secret rejection; activation failure;
  commit/unknown-outcome restart recovery; superseded pending uploads; deletion.
  Race evidence: `/tmp/nf-step9-managed-final-race.log`.
- Full Go suite with PostgreSQL and baseline native workerd passed after initial
  integration: `/tmp/nf-step9-managed-full.log`. Subsequent recovery changes pass
  the targeted artifact/publication race suite above.
- Extended native local/remote dispatch integration passes on workerd 2025-12-10
  and 2026-10-18: `/tmp/nf-step9-managed-native.log` and
  `/tmp/nf-step9-managed-native-current.log`. It verifies matching code/secret
  updates and pinned rollback without restarting the dispatcher.

The foundation now supports the opt-in compatibility prototype described below.
Native mutation interoperability and core compatibility HTTP checks pass locally.
Connection-pool reopen is verified below; process and database-server
crash/restart qualification remains step 10 work.

## Native mutation interoperability

`internal/nanoflare/management_native.go` keeps existing native operations usable
on managed Workers. Ordinary native deploys preserve the latest uploaded secret
set and tags, rebuild binding metadata and advance the upload pointer. The old
mutable saved-secret store cannot resurrect values removed by a complete managed
upload. Secret listings read the managed snapshot and preserve creation/update
timestamps; older artifacts without those timestamps fall back to revision time.

Native secret edits derive matching secret snapshots for every active code
revision while preserving each traffic weight. If rollback leaves the latest
uploaded code inactive, it receives its own derived snapshot without becoming
an active target. If no code is active, the edit records a configuration-only
publication and does not activate code or claim runtime readiness. Runtime
publication and retry recovery retain the existing complete-artifact contract.

Native vars edits retain their explicit serving-base semantics: they derive from
the requested active revision, including its pinned secrets, then become a new
latest upload. Typed JSON strings, nulls and large integers survive metadata
projection. Retry/startup recovery repairs a metadata commit failure without
rewinding a later upload. Legacy Workers skip this management reconciliation;
the existing pending-vars recovery fixture exposed and verified that boundary.

Evidence:

- Memory/PostgreSQL race tests cover split traffic, inactive latest uploads,
  stale legacy secrets, native deploy/secret/list/vars interoperability, secret
  timestamps, JSON fidelity, vars metadata commit recovery and inactive secret
  configuration without activation: `/tmp/nf-step9-interop-final-race.log`.
- The native local/remote test now exercises managed upload, rollback, native
  authenticated secret edit, native deployment, authenticated vars edit and
  secret deletion. It passes on workerd 2025-12-10 and 2026-10-18:
  `/tmp/nf-step9-interop-native.log`, `/tmp/nf-step9-interop-native-current.log`.
- Final full Go/native/PostgreSQL suite passed:
  `/tmp/nf-step9-interop-full.log`.
- `TestPostgresManagedSnapshotSurvivesConnectionReopen` closes the original SQL
  pool, opens a new one (rerunning migrations), creates a fresh service and
  verifies that native deployment preserves the persisted selected secret set,
  tags and optimistic upload state: `/tmp/nf-step9-interop-reopen.log`. The
  PostgreSQL server remains running; this is not a server crash test.

## Core HTTP adapter prototype

`internal/api/server_cfcompat.go` implements the following local operations under
`/client/v4/accounts/{Nanoflare organization ID}/workers/dispatch/namespaces`:

| Operation                                                            | Local status                                                                        |
| -------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| Namespace list/create/get/delete by name                             | Implemented; nonempty deletion is rejected                                          |
| Customer script multipart upload/upsert, details and delete          | Implemented                                                                         |
| Customer script collection listing                                   | Implemented with one `tags=tag:yes` or `tag:no` filter; pagination rejected         |
| Customer script deletion by tag                                      | Implemented with atomic metadata deletion and explicit reconciliation state         |
| Settings, bindings, tags and secret-name reads                       | Implemented; secret values excluded                                                 |
| Settings PATCH, secret PUT/DELETE, bulk secret PATCH, tag PUT/DELETE | Implemented locally                                                                 |
| Module content GET/PUT                                               | Implemented; settings retained on replacement                                       |
| Asset manifests/upload sessions/completion tokens                    | Implemented locally with bounded verified bytes and first-attachment Worker binding |
| Retain native assets with `keep_assets`                              | Implemented with scoped reads and integrity checks                                  |

The option is `Server.SetCloudflareCompatibilityEnabled(true)`. The disposable
fixture exposes it with `--enable-cloudflare-compatibility`; it is not enabled in
the normal daemon. Control authentication is mandatory even for embeddings which
otherwise allow legacy unauthenticated native control APIs. Accounts map to local
organization IDs, and control bearer credentials are Nanoflare sessions/PATs/OAuth tokens,
not Cloudflare credentials. The file upload endpoint instead accepts a scoped
Nanoflare asset-upload JWT. Each control route uses the native scopes before resource
lookup; unsupported queries and operations fail explicitly.

Every mutation requires
`X-Nanoflare-Execution-Profile: trusted_shared_workerd`. This explicit local
profile acknowledgment avoids interpreting Cloudflare's trust/cache setting as a
sandbox guarantee. The [namespace reference](https://developers.cloudflare.com/api/resources/workers_for_platforms/subresources/dispatch/subresources/namespaces/methods/create/)
describes different `trusted_workers` behavior; this adapter rejects that input
and omits the optional response field. Customer-mode namespaces require
`X-Nanoflare-Customer-ID` for new scripts; existing membership cannot be changed
through that header. Native platform-kind scripts are excluded from this adapter.

Multipart requests are decoded separately from snapshot resolution. An optional
`Idempotency-Key` identifies the normalized original request, including scope,
modules, metadata, customer identity and strict inheritance. Its persisted
identity is keyed when secret encryption is configured. Replay happens before
resolving inheritance or reading retained assets against a later snapshot. Thus
renamed inheritance can be retried, regenerated multipart boundaries are harmless,
changed requests conflict, and an old retry cannot undo a native rollback. Without
an explicit key, each upload is a new operation.

Namespace and script results use Cloudflare-shaped success/error envelopes;
script details use the documented wrapper. Error codes remain generic adapter
codes, not verified Cloudflare-specific mappings. Unknown fields, CPU options,
invalid/future compatibility dates and unsupported routes are rejected. Startup
latency is not fabricated, and runtime readiness remains explicitly unverified.
The adapter does not implement Cloudflare account APIs or billing semantics.

Upsert creates a private native Worker identity before native publication. A
later native validation/storage/publication failure can leave that undeployed
identity available for inspection or deletion, as with native create-then-deploy.
No public hostname, resource grant or intermediate secret rollout is created.
This difference requires live comparison before any broader portability claim.

Evidence:

- Authenticated native and compatibility HTTP requests produce equivalent code,
  variables, KV bindings and pinned secret values on memory and PostgreSQL.
  Tests cover envelopes, scopes/account isolation, disabled/unauthenticated
  configurations, strict decoder failures, secret redaction, retry/body conflict,
  renamed inheritance, rollback, metadata reads and retained asset repair.
  Race evidence: `/tmp/nf-step9-http-final-race.log`.
- The full Go/PostgreSQL/native baseline passed after HTTP integration:
  `/tmp/nf-step9-http-full.log`; subsequent profile/date/read-projection cases
  pass the targeted race suite above.
- Native local/remote dispatch now includes actual compatibility HTTP upload and
  retry after rollback. It passed on baseline workerd 2025-12-10 in the full suite
  and workerd 2026-10-18 in `/tmp/nf-step9-http-native-current.log`.

## Settings, secrets and tags

`internal/api/server_cfcompat_mutations.go` translates supported edits into
complete immutable managed publications. Settings PATCH accepts one bounded
multipart `settings` JSON part. Omitted fields retain their latest uploaded
values; supplied binding/flag/tag lists replace those values, and null is
rejected. Inherited bindings resolve strictly. Code and retained assets remain
pinned. Secret PUT/DELETE preserves other bindings and returns names/types only.
Both settings and secret edits publish the latest uploaded code at 100% traffic.
These preservation and activation choices remain local contracts pending live
Cloudflare comparison.

Tag PUT/DELETE records configuration-only revisions without changing serving
traffic, including after rollback. Encoded tag names such as `group%2Fa` are
decoded as a single identifier. Tag edits require `workers:write`; secret edits
require both `secrets:write` and `deployments:write`; settings edits also require
`workers:read` because their response exposes configuration. Native ownership
and resource grants remain enforced. Snapshot mutations conservatively require
healthy retained asset storage and valid current grants, including tag edits.

Mutation responses expose an ETag. Optional `If-Match` rejects stale snapshots
with 412. Keyed request identity is checked before resolving current state, so
retries recover their original result even after rollback or later edits and do
not reactivate it. Changing a request under the same key returns a conflict.
The adapter rejects non-secret binding collisions, unsupported native classic
scripts/dispatch bindings, secret-key operations and CPU settings. Bulk secret
PATCH is supported as described below.

Evidence:

- Memory and PostgreSQL HTTP mutation tests pass under the race detector:
  `/tmp/nf-step9-mutations.log`. They verify field retention/replacement,
  exact large JSON integers, secret redaction, scope checks, stale preconditions,
  retries, encoded tag deletion and unchanged serving traffic for tag edits.
- Full Go/PostgreSQL/baseline native suite passes:
  `/tmp/nf-step9-mutations-full.log`.
- Native local/remote dispatch verifies tag edits after rollback, secret edits,
  settings inheritance and secret deletion on workerd 2026-10-18:
  `/tmp/nf-step9-mutations-native-current.log`.
- Multipart settings parser race tests reject truncation, duplicate/extra parts,
  duplicate headers/JSON keys, encoded payloads, unsupported fields, oversized
  metadata/framing and reader failures:
  `/tmp/nf-step9-settings-parser-race.log`.

## Module content and tag filtering

The [content replacement reference](https://developers.cloudflare.com/api/resources/workers_for_platforms/subresources/dispatch/subresources/namespaces/subresources/scripts/subresources/content/methods/update/)
defines module-selection metadata separately from full upload configuration.
The adapter accepts module multipart content with `main_module` only; settings,
bindings, tags, secrets and retained assets come from the latest uploaded native
snapshot. Conflicting module headers, classic source and configuration fields
are rejected. Content PUT requires `deployments:write`, uses the same optional
precondition/idempotency rules and publishes through native validation.

Content GET requires `workers:read`, returns multipart source with the main
module header and `Cache-Control: no-store`, and excludes settings, secret
snapshots and asset references. It reads latest uploaded source even when traffic
has rolled back. Exact live response formatting remains unverified.

The current [API examples](https://developers.cloudflare.com/cloudflare-for-platforms/workers-for-platforms/reference/platform-examples/)
document script collection listing, so this route is no longer described as a
local extension. The [tags guide](https://developers.cloudflare.com/cloudflare-for-platforms/workers-for-platforms/configuration/tags/)
documents eight tags per script, single-tag addition, and include/exclude
filters. The adapter enforces that limit for explicit tag edits/uploads,
implements single-tag PUT without duplicate insertion, and accepts one
`tags=tag:yes` or `tag:no` list filter. The same filter now selects bulk deletion.
Compound filters and pagination remain rejected; no broader list compatibility
is claimed without source/live evidence.

Evidence: `/tmp/nf-step9-content-race.log` covers content configuration/secret
retention, latest source after rollback, multipart projection, deployment-only
write scope versus read denial, changed-request conflicts, tag limits/addition
and include/exclude/invalid filters with memory and PostgreSQL. The full native
baseline suite passes in `/tmp/nf-step9-content-full.log`; its dispatch test
verifies replaced code executes with retained settings and no removed secret.
The same dispatch test passes on workerd 2026-10-18:
`/tmp/nf-step9-content-native-current.log`.

## Persistent asset-session foundation

`internal/nanoflare/asset_upload.go` stages manifests and verified bytes in
memory/PostgreSQL repositories, independently from deployed artifacts. The
disabled-by-default adapter now connects this service to session initiation,
base64 multipart file uploads and completion metadata in full script uploads.

Sessions target an owning organization, immutable namespace ID, script name and
customer identity. Existing scripts also pin the native Worker ID, invalidating
credentials when that Worker is deleted/recreated. New-script sessions avoid
creating undeployed Worker identities merely to stage bytes. Native managed
publication binds the completion to the actual new Worker ID while holding the
publication locks, verifies the candidate's exact assets against the session and
retains that binding on later publication failure. Recreating the same script
name cannot reuse an earlier Worker's bound completion.

Each manifest permits 128 canonical absolute paths and at most 10 MiB total
asset bytes, counting duplicate-content paths toward that total. At most 16 live
sessions per organization are reserved under the repository publication lock,
including across Service instances. Upload and completion JWTs use separate
purposes and a signing key derived from the configured secret key. Upload tokens
expire after one hour; completing all files produces a distinct completion token
valid for one hour. Neither token grants control API or runtime access.

All provided files are verified before a batch is stored. Size, manifest
membership and a supported 32-character wire hash must match. The implementation
accepts truncated SHA-256 of raw bytes, as in the WFP examples, and truncated
SHA-256 of base64 bytes plus the extension without its dot, as in the direct-upload
example. A full internal SHA-256 digest and the original wire hash are checked
again when resolving completed assets. These are explicitly accepted local hash
forms; Wrangler's actual hash/client behavior still needs qualification.

Retries with identical bytes and content type recover the same completion;
changed content or content type cannot overwrite staged data. Session state uses
optimistic revisions. PostgreSQL has an additive table with a composite owning
namespace foreign key and cascade deletion. Staged bytes currently reside in
the bounded session JSON payload rather than the final artifact object store.
The daemon and local fixture run expiry cleanup at startup and once per minute.
Each pass removes at most 256 expired staging records, with a five-second
database deadline. PostgreSQL uses indexed expiry selection and row-lock skipping;
live reservation counts exclude expired records without deleting unbounded rows
on initiation. Cleanup shutdown cancels and joins database work before repository
closure. Storage/load qualification and final native artifact quota enforcement
are not proven by the session foundation alone.

The HTTP flow follows the current
[direct-upload reference](https://developers.cloudflare.com/workers/static-assets/direct-upload/):

1. `POST .../scripts/{name}/assets-upload-session` uses control authentication,
   `workers:write`, `deployments:write` and the trusted-profile acknowledgment.
   Platform-mode namespaces also require the new script's customer header.
2. `POST /client/v4/accounts/{org}/workers/assets/upload?base64=true` uses only
   the delegated upload JWT. It cannot call control routes or publish code. It
   accepts bounded standard-base64 multipart parts named by manifest hash,
   preserving supplied content types. Partial batches return 200; completion and
   identical completed-upload retries return 201 with a completion JWT.
3. Full script upload supplies `assets.jwt` and an explicit assets binding.
   Supported routing config translates to native asset configuration; token bytes
   do not enter the artifact or public management metadata. Native publication
   performs its normal quota/grant/runtime checks. Completed-request replay runs
   before staging lookup, so token expiry cannot break retries or pinned rollback.

Limits/differences: every initiation creates a new bounded session; there is no
cross-session deduplication yet. Staging uses bounded database JSON and periodic
expiry cleanup. Large cleanup backlogs still require load qualification. An explicit assets binding is required because the
native runtime otherwise introduces an implicit `ASSETS` capability not present
in management metadata. Empty native asset files and `application/null` served
content-type omission remain unsupported; these paths fail instead of claiming
Cloudflare equivalence. The SDK qualification below requires caller adaptation;
Wrangler asset flows and live Cloudflare comparison remain unverified.

Evidence:

- `/tmp/nf-step9-assets-service-race.log` verifies memory/PostgreSQL scope
  isolation, token purposes/tampering, content integrity, detached reads, expiry,
  fresh-service retries, manifest validation and concurrent reservation limits
  across two Service instances. Memory tests also verify partial uploads,
  extension-based wire hashes and rejection of changed content types on retries.
- `TestPostgresAssetUploadSurvivesConnectionReopen` verifies a new pool/service
  can finish an upload and another new pool/service can resolve its completion;
  namespace deletion cascades staged bytes and revokes completion. This remains
  a connection reopen test, not a PostgreSQL server crash test.
  Evidence: `/tmp/nf-step9-assets-reopen.log`.
- Full Go/PostgreSQL/baseline native suite passes:
  `/tmp/nf-step9-assets-foundation-full.log`. The legacy migration test now
  removes the new staging table when reconstructing its pre-platform schema;
  its existing preservation assertions remain intact.
- Authenticated asset HTTP and parser race tests pass on memory/PostgreSQL:
  `/tmp/nf-step9-assets-final-race.log`. Coverage includes partial completion,
  base64/MIME ambiguity, account/script/customer scope, upload-versus-control
  credentials, failed publication without activation, expired-token request
  replay, retained assets after expiry, rollback and deleted/recreated Workers.
- The full native suite executes code that fetches assets from a completed HTTP
  upload in local and remote runner modes:
  `/tmp/nf-step9-assets-http-full.log`. The same dispatch test passes on workerd
  2026-10-18: `/tmp/nf-step9-assets-native-current.log`.
- Cleanup race tests cover memory/PostgreSQL batch limits, live-session
  preservation, canceled SQL work, invalid limits, periodic removal and graceful
  worker shutdown: `/tmp/nf-step9-cleanup-race.log`. Full local suite:
  `/tmp/nf-step9-cleanup-full.log`. Native dispatch now expires and removes the
  actual upload session, then verifies its deployed asset still serves.
  The same cleanup/serving check passes on workerd 2026-10-18:
  `/tmp/nf-step9-cleanup-native-current.log`.

## Official JavaScript SDK qualification

`scripts/wfp-client-flows/sdk.mjs` uses the official `cloudflare@7.3.0` package
installed in an isolated temporary directory. `TestCloudflareSDKClientFlows`
starts a disposable loopback API with fixture credentials and runs the real SDK
against both memory and PostgreSQL. The script rejects non-fixture network
requests and supplies explicit local credentials, ignoring any Cloudflare login.
See [reproduction instructions](../../scripts/wfp-client-flows/README.md).

Verified operations: namespace create/list/get/delete, script details and raw
multipart content read, secret put/get/list, tag put/list/delete, explicit JSON
settings parts, asset session initiation, delegated base64 asset upload, script
publication with completed assets, bindings iteration and script deletion. SDK
page iterators consume the adapter envelopes successfully. These checks exercise
management transport; real native execution remains the separate dispatch suite.

The SDK's generated default upload is **not compatible with this adapter**. The
captured request uses `metadata[main_module]` and `files[]` parts with outer
`application/javascript`, rather than a JSON `metadata` part and named modules.
Default settings serialization similarly emits bracketed fields. Both requests
return 422. The test preserves that evidence and demonstrates caller-supplied
multipart bodies through SDK request options for script uploads, and a JSON File
part for settings. This is a JavaScript adaptation, not a claim that the SDK's
ordinary generated TypeScript parameter shapes work unchanged.

Evidence: `/tmp/nf-step9-sdk-flows.log` passes under the race detector with the
pinned package on memory/PostgreSQL. The suite explicitly skips when the SDK
directory is absent; an ordinary green Go run alone does not prove SDK behavior.
The full Go/PostgreSQL/baseline native suite also passes with the SDK fixture
enabled: `/tmp/nf-step9-sdk-full.log`.
The fixture uses no Cloudflare service, so it cannot establish whether Cloudflare
accepts the SDK-generated defaults. Live comparison remains blocked by account
entitlement. The narrower Wrangler qualification below has its own adaptation.

## Wrangler request qualification

`TestWranglerLocalRequestQualification` runs installed Wrangler 4.54.0 against
disposable loopback APIs on memory and PostgreSQL. It uses a temporary project,
explicit fixture credentials, isolated configuration and a test-only preload
that blocks non-loopback Node connections. The user's Wrangler credentials and
shell home variables are unchanged. This qualifies management requests; native
execution remains covered by the separate dispatch suite.

Wrangler namespace list/get/create/delete, module upload and a second deployment
with updated source/plain-text variables pass when a test-only loopback proxy
adds `X-Nanoflare-Execution-Profile: trusted_shared_workerd`. Direct writes fail
without this required acknowledgment. No production proxy is enabled or claimed.
The fixture inspects the resulting immutable native code/variable snapshots and
cleans up its script through the native API before CLI namespace deletion.

The test exposed two supported wire forms that the adapter previously rejected:
JSON metadata as a plain multipart field without a Content-Type header, and
`excludeScript=true` on script upload. Both now work. JSON still passes the same
exact-key/duplicate-key/unsupported-capability validation. Modules still require
their supported MIME types. Only the source-excluding projection is accepted;
`excludeScript=false` and duplicate query values fail. Source remains available
from the authenticated content endpoint.

The captured CLI also probes ordinary `/workers/scripts/{name}/settings` and,
after a rejected request, `/user/tokens/verify`. Unsupported probes stay 404;
the fixture does not fabricate Cloudflare account/token information. Secret/asset
CLI flows, preservation of an existing configuration, forced script deletion,
namespace rename, other Wrangler versions and direct unadapted writes remain
unqualified. See the [client fixture instructions](../../scripts/wfp-client-flows/README.md).

Evidence: `/tmp/nf-step9-wrangler-flows.log` passes with the race detector on
memory/PostgreSQL, including exact JSON validation for plain metadata fields.
The full Go suite passes with PostgreSQL, baseline native workerd and both pinned
client fixtures enabled: `/tmp/nf-step9-wrangler-full.log`. The network guard has
a separate test proving non-loopback socket attempts are rejected before use.

## Bulk secret merge patches

`PATCH .../scripts/{name}/secrets-bulk` follows the documented
[secret merge-patch contract](https://developers.cloudflare.com/api/typescript/resources/workers_for_platforms/subresources/dispatch/subresources/namespaces/subresources/scripts/subresources/secrets/methods/bulk_update/)
for `secret_text`: a `secrets` object maps binding names to named secret objects
or null. Omitted names retain their latest uploaded values; null deletes only
that secret, including an idempotent no-op for a missing secret. A null entry
cannot remove a non-secret variable. Add/update entries must match their map key
and cannot overwrite non-secret bindings. Empty strings are valid secret values.

The whole patch produces one complete immutable code/configuration/secret
revision through `ManagedDeployForOrg`. It uses the latest upload after rollback,
requires `secrets:write` plus `deployments:write`, and supports the same optional
If-Match and keyed retry behavior as single-secret edits. The result maps all
remaining secret names to name/type metadata; plaintext values are excluded.
As with other compatibility secret edits, successful publication activates the
latest uploaded code at 100% traffic.

Input is bounded to a 1 MiB JSON body and 128 patch entries, with native effective
binding/secret limits checked before publication. Duplicate keys, case aliases,
null `secrets` maps, malformed values and conflicting binding names fail before
any publication. `secret_key` and `version_tags` remain rejected; they are not
treated as ordinary text secrets or script tags.

Evidence:

- `/tmp/nf-step9-bulk-secrets-race.log` passes on memory/PostgreSQL. It verifies
  exact resulting code/secret snapshots, one revision/configuration write for
  multiple changes, retained secrets after rollback, empty values, redaction,
  stale preconditions, retries after rollback, changed-request conflicts and
  no state change for invalid patches. Simultaneous requests with the same
  precondition publish exactly one complete matching secret pair; the loser
  receives a conflict or stale-precondition response. Publication failure leaves
  serving traffic unchanged; retrying a failed key cannot activate it, while a
  new request can recover.
- The pinned official JavaScript SDK fixture now exercises `secrets.bulkUpdate`
  without manual request adaptation. The full Go/PostgreSQL/baseline native and
  pinned-client suite passes: `/tmp/nf-step9-bulk-secrets-full.log`.
- Native dispatch now verifies a bulk-updated pair and retained variable values
  in actual local/remote Worker execution. Current-runtime validation is recorded
  separately in `/tmp/nf-step9-bulk-secrets-native-current.log`.

## Tag-selected deletion and account routing

`DELETE .../namespaces/{namespace}/scripts?tags=tag:yes` (or `tag:no`) selects
current customer scripts by latest uploaded tags. Platform-kind scripts are
excluded. Selection/publication share the same locks as tag edits and rollback;
the repository repeats owning organization, immutable namespace and kind checks
and deletes the complete selected metadata batch atomically. Other namespaces,
accounts, platform scripts and shared KV/database/object resources remain.

The route requires `workers:write`, the trusted-profile header and one explicit
tag filter. Per-script customer, precondition and idempotency headers are
rejected rather than ignored. Retries select the current matching set; they are
not a replay of an earlier selection. The local result reports deletion count,
metadata outcome and configuration refresh acknowledgment. This result shape is
a documented local extension, not a live-verified Cloudflare response mapping.

After metadata commit, deleted runtime cache entries are invalidated and native
configuration is refreshed. A refresh failure returns 503 with the committed
count and `nanoflare_config_refreshed: false`. Even an empty retry refreshes
configuration, so a committed deletion can reconcile after a writer failure.
Unknown storage commit outcomes return `nanoflare_deletion_state: unverified`
without inventing a count. Object cleanup follows native best-effort deletion;
orphan storage and server-failure qualification remain step 10 work.

The new isolation test exposed a missing adapter account-routing check:
organization-scoped PAT authentication resolves the token's own organization,
while native handlers separately enforce URL organization equality. The adapter
had omitted that second check, allowing a request naming a different account to
operate in the token's own account. The adapter now rejects a resolved-account
mismatch before namespace lookup or mutation. Regression tests cover namespace
reads/creates, script upload/settings reads and bulk deletion with scoped PATs.
The delegated asset route retains its separate JWT/account check.

Evidence:

- Memory/PostgreSQL race tests pass:
  `/tmp/nf-step9-delete-race.log`. Coverage includes latest tags after rollback,
  cross-account/namespace/platform isolation, independent read/write scopes,
  invalid filters/headers without side effects, committed writer failures,
  empty retry reconciliation and unknown commit outcomes without false counts.
- An actual PostgreSQL delete trigger fails during the batch; the transaction
  restores every selected Worker, deployment, active traffic row and artifact.
  Removing the trigger permits the complete batch:
  `/tmp/nf-step9-delete-rollback.log`.
- Full Go/PostgreSQL/baseline native and pinned-client suite passes:
  `/tmp/nf-step9-delete-full.log`. The dispatch test removes a previously cached
  asset-serving customer Worker by tag and verifies dispatch returns 404 without
  restarting the platform dispatcher. Current-runtime results are recorded in
  `/tmp/nf-step9-delete-native-current.log`.

The [supported endpoint reference](../architecture/cloudflare-management-adapter.md)
now records routes, scopes, wire forms, retry semantics, limits, errors and
explicit differences. Compatibility routes remain outside native generated
OpenAPI; that document continues to describe the native control API.

## Remaining implementation and verification

1. Current documented single-tag list/delete behavior is implemented. Keep
   unsupported pagination/compound filters explicit until their exact contract
   is established. Step 10 records the completed bounded process/server failure drills.
2. Extend concurrent mutation and recovery qualification beyond the existing
   upload/publication race tests and HTTP retry checks.
3. Keep the endpoint/error reference aligned with any additional adopted client flows.
   The pinned Wrangler fixture qualifies only its documented plain-module
   subset with profile-header adaptation.
   Retain the explicit multipart adaptation boundary demonstrated by the pinned
   JavaScript SDK fixture; qualify any proposed client helper against real requests.
   Preserve scope checks, truthful runtime state and explicit rejection of
   unsupported operations. Test concurrent HTTP mutations and recovery.
4. Bounded asset storage/load qualification now passes with four concurrent
   adapter sessions, real PostgreSQL/MinIO, native dispatch reads, restart and
   staging removal; see the asset qualification addendum below. The pinned SDK
   also exercises asset session/upload/publication. Explicit native differences
   (empty files, served-header omission, required assets binding and cross-session
   deduplication) remain documented; production soak and broader clients are
   unqualified rather than implied by this bounded result.
5. Keep differential native/adapter and real-workerd checks aligned with the
   supported subset as remaining operations change. Current pinned client
   fixtures pass only with their documented adaptations. Update capability
   documentation and generated API references before broader rollout claims.
6. Perform the controlled live comparison once an entitled staging account is
   available. The user has no enabled account; do not request credentials again
   or enable billing. See the [live comparison record](workers-for-platforms-live-comparison.md).

Step 10 bounded local trusted-pilot qualification and the requested independent
subagent review have since completed; see the [step 10 record](workers-for-platforms-step-10-status.md)
and [review findings and fixes](workers-for-platforms-independent-review.md).
Live comparison remains blocked. No release or deployment has been performed.

## Asset storage/load qualification addendum — 2026-10-22

The [combined asset drill](../../scripts/wfp-qualification/asset_storage.py) closes
the earlier gap between adapter upload tests and independent native MinIO recovery
tests. Four customer sessions upload concurrently through the real daemon's
compatibility HTTP routes and persist metadata in PostgreSQL. Each uploads 1 MiB
of distinct bytes mapped to two manifest paths by one hash. Completion retries
retain their credential identity; explicit content types survive, and omitted
part types become `application/octet-stream`. Native customer Workers fetch both
paths from MinIO and report exact SHA-256/length/type, with four reads in flight.

The same reads pass after killing/restarting the daemon and restarting MinIO.
Deleting exactly four staging sessions in the fixture's own database leaves
published immutable assets readable. The [report](../../scripts/wfp-qualification/results-asset-storage.json)
records observations, binary/image/native identities, physical runner limits and
verified fixture cleanup. This is a bounded 4 MiB distinct / 8 MiB manifest-byte
correctness/load check, not production capacity or extended soak. Earlier
validation tests cover manifest limits and unsupported contracts; this drill
does not claim empty-file or cross-session deduplication support.

## Managed Sites compatibility follow-up — 2026-10-22

The third-party Managed Sites design uses the documented dynamic-dispatch pattern:
an ordinary platform dispatcher passes a named `WorkerEntrypoint` RPC stub through
per-request props to a private namespace script. The native runtime already tests
that pattern, including concurrent context separation and cross-request reuse
rejection. This does not establish full acceptance of the application's generated
modules, publication marker, asset routing or production control service.

The D1 wire mismatch is fixed locally. Uploads and settings edits accept
`database_id`, retain the deprecated `id` alias, reject conflicting nonempty
identifiers, and emit the current spelling in newly translated/native-projected
metadata. Retained legacy metadata works with preservation and renamed inheritance.
Parser and HTTP lifecycle tests cover these paths and verify that an ungranted
database cannot replace the confirmed binding. PostgreSQL cases remain opt-in
through `NANOFLARE_TEST_POSTGRES_URL`.

Remaining integration work is explicit:

1. Ordinary Worker multipart dispatcher upload is now implemented locally at
   `PUT /client/v4/accounts/{account}/workers/scripts/{name}`; see the addendum
   below. Live provider/client acceptance remains open. Routing/TLS remain
   separate configuration, not side effects inferred from a multipart upload.
2. Exercise the actual Managed Sites dispatcher, control adapter and generated
   publication wrapper through Nanoflare's public serving path. Cover first
   publication with authenticated candidate admission, expected marker and artifact
   checksum, stable-script update, lost upload response reconciliation, secret
   preservation, rollback, and Worker-first/SPA asset routing. Explicitly check the
   asset limits and empty-file difference before declaring their uploader portable.
3. Configure an immutable Site-to-customer mapping and scoped resource grants for
   platform-mode namespaces. Maico must send Nanoflare's explicit profile/customer
   headers, use native resource adapters, and implement route authorization,
   capability grants and current revocation checks in its own control service.
4. Keep untrusted Site execution unavailable until CPU enforcement and a qualified
   hardened failure boundary pass their separate release gates. An API schema fix
   does not upgrade the shared trusted runtime into an untrusted hosting profile.
5. Validate the selected client/runtime flows against an entitled Cloudflare
   staging target before claiming cross-provider portability. The existing live
   comparison gate remains blocked; no new credentials or billing are requested.

Validation for the D1 change: `go test -race ./internal/cfcompat
./internal/nanoflare ./internal/api` passes with the native workerd 2025-12-10
binary selected directly. PostgreSQL cases were skipped because the test URL was
not configured. The initial wrapper-based broad run stalled in child-process
cleanup and was stopped; its test-owned runtime processes were terminated. The
first native run hit an address-in-use failure in
`TestReproEgressHangsOnHTTP2Upstream`; that targeted test and the complete rerun
both passed. This evidence does not include a live Cloudflare or Maico deployment.

## Ordinary dispatcher upload — 2026-10-22

`server_cfcompat_ordinary.go` adds ordinary module upload and settings inspection.
Uploads reuse the namespace adapter's complete snapshot translator, management
state, secret preservation, request identity and recoverable native publication.
Only the ordinary route enables `dispatch_namespace` translation. It resolves
names in the authenticated account, and checks `namespaces:dispatch` before
inheritance or replay in addition to the existing upload scopes. Namespace customer
uploads cannot acquire namespace-wide lookup through this path.

Ordinary script identity is a domain-separated SHA-256 of account and script name,
using the existing Worker primary key. Creation is serialized with the repository
publication/deletion lock; concurrent upserts cannot create separate identities.
It survives service recreation without a new schema or interpreting nonunique
native display names as Cloudflare script identities. New ordinary Workers have
no hostname. The upload receipt exposes `nanoflare_worker_id` for separate native
routing/administration. Existing matching native display names remain separate.

Native management metadata now validates and projects namespace bindings as well
as resource bindings. Native managed-secret edits also reject namespace binding
name collisions. Unsupported dispatch options, ordinary asset sessions and other
ordinary API operations remain explicit errors. The trusted execution restriction
and live Cloudflare acceptance gate are unchanged.

The new parser and HTTP tests cover ordinary-only binding authority, account and
namespace isolation, required scopes/profile, secret retention, latest upload
settings, rollback-safe keyed retries, service recreation and concurrent first
uploads. The existing local/remote native dispatch fixture now uploads its
ordinary dispatcher through the compatible multipart endpoint and routes its named
RPC capability through a real platform-owned CONTROL service binding. It retains
its resource, concurrency, cross-request RPC and runner-recovery checks.

Final validation passes with the native workerd 2025-12-10 executable and a
separate disposable PostgreSQL 17 server:

```sh
go test -race ./internal/cfcompat ./internal/nanoflare ./internal/api
```

Both memory/PostgreSQL HTTP cases and local/remote native dispatch cases run in
that suite. A separate targeted run also passes the native secret/code editing
interop checks on both repositories. Retained namespace bindings are compared
with their original native IDs before publication; parser tests reject silent
rebinding after namespace name reuse. Renaming a namespace therefore requires an
explicit updated binding on the next compatible upload.

The first expanded native fixture assumed the dispatcher was the first Worker
in a pool; adding CONTROL exposed that assumption. The fixture now selects the
dispatcher by Worker ID for its restart/credential checks. That initial broad run
also encountered an unrelated egress fixture port-bind conflict. The corrected
targeted run and subsequent complete runs pass. Test databases/containers and
native-path fixtures were cleaned up. No live Cloudflare comparison, Maico
production deployment, release or untrusted-profile activation was performed.
