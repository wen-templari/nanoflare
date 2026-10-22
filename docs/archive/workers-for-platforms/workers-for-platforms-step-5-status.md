# Step 5 — Immutable artifacts and recoverable publication

> Historical record. Status statements and proposed designs below describe the work at the time.
> See the [documentation index](../../README.md) for the maintained contract and
> [remaining work](../../plans/workers-for-platforms-remaining-work.md) for current gates.

Updated: 2026-10-19. Status: implemented and verified locally for the trusted runtime path. Not committed or deployed. Private loader dispatch and invocation leases remain step 6; CPU enforcement remains a separate workstream.

## Artifact contract

Each new deployment has an immutable artifact indexed by Worker ID and deployment ID. Both repositories retain a detached source/configuration/binding snapshot, SHA-256 content digest, asset body digests, encrypted secret snapshots with opaque version references, and a namespace policy snapshot. Replacing an existing artifact with different content is rejected even if a caller supplies the old digest. PostgreSQL persists artifacts and publication records across service restarts; memory mode retains them for the process lifetime.

The digest includes effective source/configuration, asset hashes, secret versions and namespace policy. Deployment IDs, revision storage paths and deployment audit metadata are excluded. Equivalent redeployments with unchanged secret/policy versions have the same digest. Uploaded asset bodies remain in revision-specific object storage; reads verify their digest and fail on corruption. Runtime source is loaded from the immutable artifact, so changing a source bundle object cannot change an existing revision. Derived variable/secret revisions verify their shared assets against the base artifact.

Authorized public reads expose a manifest: deployment/Worker identity, digest, asset digests, secret names/version references, namespace ID/policy revision, and creation time. They do not expose decrypted secrets, nonce/ciphertext, signing roots, source bundles, or other customers' policy grants. Existing source-view permissions/APIs retain their established behavior.

| Read endpoint                                                                                   | Required scope        |
| ----------------------------------------------------------------------------------------------- | --------------------- |
| `GET /v1/organizations/{orgID}/workers/{workerID}/deployments/{deploymentID}/artifact`          | `workers:read`        |
| `GET /v1/organizations/{orgID}/workers/{workerID}/publications/{key}`                           | `workers:read`        |
| `GET /v1/organizations/{orgID}/dispatch-namespaces/{namespaceID}/scripts/{scriptName}/artifact` | `namespaces:dispatch` |

Every lookup checks organization ownership. Namespace/script resolution selects an active deployment using native weighted traffic. The service also supports an active preferred revision for existing routing affinity; the public manifest resolution endpoint does not accept an arbitrary inactive revision override. This management lookup does not itself grant a customer runtime dispatch capability.

## Publication and retries

Deployment requests accept an optional `idempotency_key` or `Idempotency-Key` header, scoped to the Worker. Supplying conflicting header/body values returns 400. Keys are limited to 256 bytes; callers needing recovery should supply one. Without a key, the service creates an internal key, so independent unkeyed requests remain independent deployments.

Identical requests with the same key return the original revision without creating or reactivating it. Fingerprints canonicalize nested JSON without rounding numbers and exclude the actor marker. Different content with an existing key returns 409. Confirmed failures retain a failed outcome; a new attempt requires a new key. Replaying a successful or superseded outcome never moves traffic back from a later selection.

Publication is serialized with the existing cross-process repository guard. Its durable phases are:

| Phase             | Meaning                                                                                                      |
| ----------------- | ------------------------------------------------------------------------------------------------------------ |
| `building`        | Intent recorded; artifact construction/upload is incomplete. Recovery fails this attempt without activation. |
| `stored`          | Artifact and inactive deployment persisted.                                                                  |
| `runtime_ready`   | A prepare/commit publisher started the candidate and passed the existing process/socket readiness check.     |
| `activating`      | Activation intent persisted before changing database traffic.                                                |
| `route_published` | Runtime/configuration publication returned successfully. Edge propagation can follow later.                  |
| `rolling_back`    | Rollback intent persisted before restoring traffic and runtime/routes.                                       |
| `failed`          | Attempt failed; completed rollback retains the previous selection.                                           |
| `superseded`      | A later traffic selection prevents an unfinished attempt from reactivating its candidate.                    |

The local manager and remote runner use prepare/commit/abort. The previous pool remains available during preparation. Socket readiness does not certify arbitrary customer code or application dependencies. Plain configuration writers remain supported, but their publication status reports `runtime_ready_verified: false`; configuration publication is not silently upgraded to runtime health verification.

The daemon reconciles unfinished records before its initial configuration publication. Retrying an uncertain request also reconciles its outcome. Recovery resumes a prepared/stored candidate only while traffic still matches its recorded base, republishes an already-applied candidate, or marks an obsolete attempt superseded. A rollback intent is recovered rather than mistaken for a successful activation. Prepared runner generations can be reused after a lost prepare response; a successor replaces an obsolete uncommitted generation. These operations rely on the existing single repository publication guard and trusted runner management channel.

Failed stored revisions/artifacts remain available for audit instead of being deleted with their publication record. Normal traffic changes still determine which revisions are active. Artifact/object garbage collection and retention policy are not implemented by this step; retained artifacts must not be manually removed while referenced by active/draining work.

## Variables, secrets and rollback

A new source deployment captures the current desired secret set. Plain-variable updates preserve the base revision's source, assets and sealed secret versions. They retain the existing sole-active-base CAS and idempotency protocol.

Secret writes/deletes retain the existing desired-secret store and publish new revisions for every active traffic assignment, preserving each assignment's weight. Those revisions capture the new desired secret set. Existing revisions never silently acquire new secret values. Selecting an old revision restores its old code, bindings, assets and secret versions together. Failed secret publication restores the old active snapshots; the desired secret record remains changed, so a later fresh deployment or secret retry can apply it. A plain-variable update based on a rolled-back revision continues using that revision's secret versions.

Legacy active deployments without artifacts are pinned before a secret mutation and during daemon recovery. This records the secrets/configuration observed at upgrade; historical secret versions that were never retained cannot be reconstructed. Inactive legacy revisions continue using the legacy fallback until an artifact is captured. Do not claim retroactive historical-secret guarantees for pre-upgrade deployments.

Runner preparation now transports pinned secret values and traffic weights over its authenticated management channel; public response JSON continues hiding secrets. The request has an artifact protocol version. Update control plane and runner together: mismatched legacy requests are rejected, and older runners reject the added request fields rather than silently dropping snapshot data.

## Invocation boundary and remaining work

The selected deployment supplies exact revision-scoped runtime capabilities and assets. Old live/draining revisions retain their own snapshot; an old credential cannot read a new revision's assets. Existing runtime liveness checks remain mandatory. Namespace policy snapshots are immutable audit/reference data; current namespace revocation still denies subsequent effects immediately, including effects from live old revisions. A snapshot never overrides current revocation.

This step does not add a new invocation lease, per-request loader capability, background-work budget, or cross-process cell protocol. Step 6 must integrate those with the artifact selected once for the invocation, including streaming and cancellation. The existing shared trusted pool and its draining behavior remain the execution path. Untrusted execution is still unavailable.

## Verification

Passed locally with a disposable PostgreSQL 17 instance and the native workerd 2025-12-10 executable:

- Full Go suite, including native runtime integration and existing authorization/migration tests.
- Artifact/publication matrix against memory and PostgreSQL: immutable replacement rejection, detached reads, digest equivalence, organization isolation, secret redaction, retry/conflict handling (including concurrent requests through independent service instances), failed prepare/commit, interrupted activation, lost final acknowledgement, superseded pending work and rollback recovery.
- Secret weighted-rollout/rollback and variable inheritance checks; old/new asset separation, credential rejection across revisions, and corrupted asset rejection.
- Local prepared-generation reuse/replacement and authenticated runner snapshot transport tests.
- Race detector for service, API, database, runtime and runner packages.
- Generated OpenAPI/TypeScript contract, schema typecheck and UI typecheck.

Verification logs for this local run are under `/tmp/nf-step5-*.log`; they are transient machine-local evidence. One full-suite attempt encountered an existing ephemeral TCP port collision in an egress test; the subsequent full suite passed. No production or live Cloudflare compatibility test is claimed.

Next: step 6, private dynamic dispatch through the real artifact/authorization model.
