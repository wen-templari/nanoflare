# Cloudflare-shaped management adapter: supported local contract

Updated 2026-10-22. This reference describes the implemented trusted-profile
prototype, including its local differences. The normal daemon leaves the
adapter disabled. Opt in with `--enable-cloudflare-compatibility` or
`NANOFLARE_CLOUDFLARE_COMPATIBILITY_ENABLED=true`, together with native platform
dispatch (`--enable-platform-dispatch` or its environment setting). Conflicting
flags fail before startup. The trusted-pilot Compose override enables native
dispatch only; the experimental adapter needs its own explicit opt-in.
Embeddings may use `Server.SetCloudflareCompatibilityEnabled(true)` and the
disposable demo also exposes the flag. None of these options removes the
mandatory trusted execution acknowledgment on control mutations. Live Cloudflare comparison is
[blocked by account entitlement](../plans/workers-for-platforms-live-comparison.md).

The adapter is served separately from the generated native OpenAPI document.
This reference and the authenticated adapter/client fixtures describe its wire
contract. Native API schemas continue to describe native routes.

## Authentication and identity

The namespace control prefix is:

```text
/client/v4/accounts/{Nanoflare organization ID}/workers/dispatch/namespaces
```

Control requests require Nanoflare session, personal-access or OAuth bearer
credentials. The URL account must match the resolved credential organization;
an organization-scoped PAT cannot redirect a request into its own account under
another account's URL. Cloudflare credentials are not Nanoflare credentials.
The adapter requires configured control authentication even in embeddings that
permit legacy unauthenticated native control routes.

Every control mutation also requires:

```text
X-Nanoflare-Execution-Profile: trusted_shared_workerd
```

This acknowledges the native trusted execution profile. Cloudflare's
`trusted_workers` input is rejected because its cache/request-context semantics
are different. Native platform-kind scripts are excluded from the namespace routes.
Customer scripts remain private and keep immutable namespace/customer identity.
For new scripts or new-script asset sessions in a platform-mode namespace,
`X-Nanoflare-Customer-ID` selects the customer. On an existing script the header,
if supplied, must agree with its existing identity. Tags never confer grants or
customer authorization.

The delegated asset file endpoint described below uses an asset JWT in place of
control credentials. It can stage verified bytes and cannot publish code.

## Control routes and required scopes

Paths in this table are relative to the namespace prefix above. `{namespace}`
and `{script}` identify names, not native immutable IDs.

| Method and path                                            | Required native scopes                                | Behavior                                                                              |
| ---------------------------------------------------------- | ----------------------------------------------------- | ------------------------------------------------------------------------------------- |
| GET `/`                                                    | `namespaces:read`                                     | List namespaces                                                                       |
| POST `/`                                                   | `namespaces:write`                                    | Create from JSON `{ "name": "..." }`                                                  |
| GET `/{namespace}`                                         | `namespaces:read`                                     | Namespace details and customer-script count                                           |
| DELETE `/{namespace}`                                      | `namespaces:write`                                    | Delete an empty namespace; nonempty returns 409                                       |
| GET `/{namespace}/scripts`                                 | `workers:read`                                        | Customer scripts, optionally filtered by one tag                                      |
| DELETE `/{namespace}/scripts?tags=...`                     | `workers:write`                                       | Delete the current matching customer-script selection                                 |
| PUT `/{namespace}/scripts/{script}`                        | `workers:write`, `deployments:write`, `secrets:write` | Multipart upload/upsert into one complete native revision                             |
| GET `/{namespace}/scripts/{script}`                        | `workers:read`                                        | Details wrapper and script summary                                                    |
| DELETE `/{namespace}/scripts/{script}`                     | `workers:write`                                       | Delete the private native Worker                                                      |
| GET `/{namespace}/scripts/{script}/settings`               | `workers:read`                                        | Selected uploaded configuration, with secret values omitted                           |
| PATCH `/{namespace}/scripts/{script}/settings`             | `workers:read`, `deployments:write`, `secrets:write`  | One multipart `settings` JSON part; omitted fields retained, supplied lists replaced  |
| GET `/{namespace}/scripts/{script}/bindings`               | `workers:read`                                        | Typed binding metadata without secret values                                          |
| GET `/{namespace}/scripts/{script}/content`                | `workers:read`                                        | Multipart module source and main-module header                                        |
| PUT `/{namespace}/scripts/{script}/content`                | `deployments:write`                                   | Replace modules while retaining configuration and pinned secrets/assets               |
| GET `/{namespace}/scripts/{script}/secrets`                | `workers:read`                                        | Secret names/types                                                                    |
| GET `/{namespace}/scripts/{script}/secrets/{name}`         | `workers:read`                                        | One secret's name/type                                                                |
| PUT `/{namespace}/scripts/{script}/secrets`                | `secrets:write`, `deployments:write`                  | JSON `name`, `type: secret_text`, `text`                                              |
| DELETE `/{namespace}/scripts/{script}/secrets/{name}`      | `secrets:write`, `deployments:write`                  | Delete one selected secret                                                            |
| PATCH `/{namespace}/scripts/{script}/secrets-bulk`         | `secrets:write`, `deployments:write`                  | One JSON merge patch and one revision; omitted secrets retained, null entries deleted |
| GET `/{namespace}/scripts/{script}/tags`                   | `workers:read`                                        | Tag strings                                                                           |
| PUT `/{namespace}/scripts/{script}/tags`                   | `workers:write`                                       | Replace tags from a JSON array                                                        |
| PUT `/{namespace}/scripts/{script}/tags/{tag}`             | `workers:write`                                       | Add one tag without duplicates                                                        |
| DELETE `/{namespace}/scripts/{script}/tags/{tag}`          | `workers:write`                                       | Remove one tag                                                                        |
| POST `/{namespace}/scripts/{script}/assets-upload-session` | `workers:write`, `deployments:write`                  | Stage a bounded manifest, returning upload JWT and hash buckets                       |

The single-tag filter is `tags=tag:yes` for inclusion or `tags=tag:no` for
exclusion. Both listing and deletion use latest uploaded tags, independently of
traffic rollback. Pagination, repeated/compound filters and unknown query
parameters are rejected. Full script PUT also accepts
`bindings_inherit=strict` and `excludeScript=true`. Source is always excluded from
script summaries and can be read through the authenticated content endpoint.

## Ordinary Worker dispatcher uploads

The adapter also supports these account-level routes:

| Method and path                                                            | Required native scopes                                                       | Behavior                                                                             |
| -------------------------------------------------------------------------- | ---------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| PUT `/client/v4/accounts/{organization}/workers/scripts/{script}`          | `workers:write`, `deployments:write`, `secrets:write`, `namespaces:dispatch` | Multipart module upload/upsert using the same immutable managed publication pipeline |
| GET `/client/v4/accounts/{organization}/workers/scripts/{script}/settings` | `workers:read`                                                               | Inspect latest uploaded configuration, with secret values omitted                    |

These routes require the same adapter opt-in, account authorization and trusted
execution acknowledgment. This dispatcher-oriented upload subset conservatively
requires `namespaces:dispatch` even when an upload omits dispatch bindings, and
checks it before keyed replay or inheritance. Customer identity headers are rejected.

Upload metadata may include:

```json
{
  "main_module": "worker.js",
  "compatibility_date": "2025-12-10",
  "bindings": [
    { "name": "DISPATCHER", "type": "dispatch_namespace", "namespace": "sites" },
    { "name": "CONTROL", "type": "service", "service": "control-adapter" }
  ]
}
```

Namespace names resolve only within the authenticated organization and become
immutable native namespace IDs. Namespace-script uploads continue rejecting
`dispatch_namespace`, including through preservation/inheritance. Dispatch outbound
options and arbitrary service entrypoint selection remain unsupported and rejected.
The `CONTROL` target must already be deployed; ordinary static service bindings
can call the target's default `WorkerEntrypoint` RPC methods.

Ordinary script names use a deterministic, domain-separated account/name Worker
identity, enforced by the existing Worker primary key and publication lock. They
must use the same 1–63 character lowercase name subset as namespaces. Existing
native Workers with matching display names are not adopted. Successful uploads
return a `nanoflare_worker_id` extension for native administration. New identities
have no hostname; configure ingress/TLS separately using the native routing setup.
No shared namespace is created or deleted by ordinary upload. Failed publication
can leave an undeployed ordinary identity for inspection/native cleanup.

Full uploads preserve only explicitly selected binding types, for example
`keep_bindings: ["dispatch_namespace", "service", "secret_text"]`, or explicit
`inherit` entries. Secrets remain provider-owned and redacted. The ordinary route
supports the same optional `Idempotency-Key`, strict inheritance and
`excludeScript=true` query as namespace upload; full-upload `If-Match` remains
rejected. Replaying a completed upload cannot undo native traffic rollback.
Retained namespace bindings must resolve to their original immutable IDs; a
namespace rename/replacement requires an explicit binding on the next upload.
Native deploy/vars/secret edits retain namespace binding metadata; native secret
edits cannot collide with dispatch binding names.

Other ordinary Worker API operations, including code download, settings PATCH,
secret endpoints and ordinary asset-upload sessions, remain unsupported. Namespace
asset-upload completion tokens cannot be attached through the ordinary route.
Native APIs remain available for those administration tasks. These additions do
not claim general Wrangler compatibility or live Cloudflare equivalence.

## Revision selection and retries

Full uploads and configuration/secret/content mutations produce complete
immutable native snapshots and publish latest uploaded code at 100% traffic.
Native weighted deployment/rollback APIs remain available. Tag mutations create
configuration-only revisions and preserve serving traffic. A failed activation
can leave a complete stored upload as the latest inheritance source while
serving traffic returns to its previous revision.

Full uploads and per-script configuration/secret/tag/content mutations accept
an optional `Idempotency-Key`. Normalized keyed identities exclude plaintext
from persisted request metadata. An identical completed retry recovers its
original result before resolving current state or staging credentials, and
cannot undo a later rollback. Changed requests under the same key return 409.
Without a key, each request is a new operation.

Settings, secret, bulk-secret, tag and content mutation responses expose an ETag
and accept an optional `If-Match` revision. A stale observed revision returns 412;
a simultaneous optimistic publication conflict can return 409. Unsupported
native classic scripts or dispatch bindings require the native API.
Full script upload rejects `If-Match` rather than silently ignoring it.

Bulk deletion has current-selection semantics and requires an explicit tag
filter. It rejects `Idempotency-Key`, `If-Match` and the per-script customer
header rather than ignoring them. Ownership and membership checks are repeated
inside the storage batch. All selected Worker metadata is deleted atomically;
shared KV/database/object resources, other namespaces/accounts and platform-kind
scripts remain untouched. Successful results report `deleted_count`,
`nanoflare_deletion_state: committed` and `nanoflare_config_refreshed`.

If configuration refresh fails after commit, a 503 response reports the committed
count and `nanoflare_config_refreshed: false`. A retry refreshes current
configuration even if the selection is empty. An unknown storage commit outcome
reports `nanoflare_deletion_state: unverified` and omits the count. A retry selects
whatever currently matches the filter, including scripts created/tagged since
the earlier request. Configuration refresh acknowledgment is not runtime
readiness telemetry. Object cleanup is best effort, as in native deletion;
orphan storage qualification remains step 10 work.

## Upload and asset limits

Module uploads accept UTF-8 JavaScript module parts plus a JSON `metadata` part,
including metadata supplied as a plain multipart field. Supported typed bindings
are plain text, JSON, secret text, KV, D1, R2, fetch services and assets. Native
ownership/grants are checked after translation. Type preservation and renamed
inheritance resolve against the latest upload; strict missing inheritance fails.
Unsupported fields/types and ambiguous duplicate JSON/parts are rejected.

D1 uploads and settings edits accept the current `database_id` field and the
deprecated `id` alias. When both are nonempty they must agree. New translated
snapshots and native update projections emit `database_id`; retained legacy
snapshots remain readable and are normalized when used in a new publication.
Identifiers still pass native organization ownership and namespace/customer
resource-grant checks.

Code uploads are bounded to 10 MiB, 256 parts, 1 MiB metadata and 128 effective
bindings. Secret patch bodies are bounded to 1 MiB and 128 entries. Tags are
limited to eight distinct strings of at most 256 UTF-8 bytes. Secret-key bindings,
version tags, CPU intent, classic scripts, non-JavaScript module formats and
arbitrary RPC entrypoints are unsupported by this profile.

Asset manifests map canonical absolute paths to hash/size entries. A session
permits 128 files and 10 MiB total declared bytes; an organization can hold at
most 16 live sessions. The delegated endpoint is:

```text
POST /client/v4/accounts/{organization}/workers/assets/upload?base64=true
Authorization: Bearer {upload JWT}
```

Multipart parts are named by manifest hash and contain standard base64 data.
Size, content hash, scope and content type are verified before each batch is
stored. Partial batches return 200; completion and identical completion retries
return 201 with a distinct completion JWT. Upload and completion credentials
have separate purposes and one-hour lifetimes. Completed assets attach only to
their owning account/namespace/script/customer, and first attachment binds a
new-script session to the actual immutable Worker ID.

Full script metadata supplies `assets.jwt` and an explicit assets binding.
Routing configuration can set native HTML/not-found/worker-first behavior.
Tokens never enter deployed artifacts. Retained assets use `keep_assets` with
integrity checks, and remain usable after staging expiry/cleanup. Cleanup runs
at startup and each minute in batches of at most 256 expired records, with a
five-second database deadline. Cross-session deduplication, empty native asset
files and `application/null` served-header omission are unsupported.

## Errors and qualification boundaries

JSON responses use Cloudflare-shaped envelopes. Module content is a raw
multipart response. Error code 10000 is an adapter code; exact Cloudflare error
mapping is unverified. Main statuses are 401 invalid credentials, 403 scope or
account denial, 404 absent/unsupported routes, 409 conflict/in-use, 412 stale
precondition, 422 unsupported/invalid adapter input and 503 deletion
reconciliation. Arbitrary native backend errors receive a generic 400 message;
native quota failures can return 402.

No startup latency or sandbox guarantee is fabricated. Script summaries expose
`nanoflare_runtime_ready_verified: false`. The native shared trusted pool lacks
real CPU interruption and a hardened untrusted failure boundary; CPU enforcement
is a separate workstream.

The [client fixtures](../../scripts/wfp-client-flows/README.md) qualify pinned
SDK and Wrangler subsets with explicit adaptations, not drop-in compatibility.
The [step 9 record](../plans/workers-for-platforms-step-9-status.md) holds the
current test evidence and unresolved live comparison gate. Step 10 qualification
and independent review still precede any pilot deployment decision.
