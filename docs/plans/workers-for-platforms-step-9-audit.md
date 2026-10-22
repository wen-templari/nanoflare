# Step 9 acceptance audit

Updated 2026-10-22. **The documented local trusted subset is implemented and
qualified by local fixtures. Step 9's complete acceptance criterion remains
unmet because the required live Cloudflare comparison cannot run.** This audit
does not broaden the frozen native contract or treat rejected features as
implemented. No release or deployment is authorized by passing these checks.

| Original requirement                                      | Current evidence                                                                                                                                                                                                        | Finding                                                                                         |
| --------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------- |
| Namespace/script operations and multipart code metadata   | Authenticated memory/PostgreSQL HTTP fixtures; native local/remote dispatch; scoped PAT URL/account regressions                                                                                                         | Implemented for private customer scripts and the documented profile                             |
| Typed bindings and native authorization                   | Independent expected native snapshots in translator tests; native/adapter HTTP comparison; KV/D1/R2/service resolvers and native grant checks                                                                           | Implemented for the enumerated binding subset; unknown types rejected                           |
| Settings, secrets, tags and inheritance                   | Mutation fixtures verify retention/replacement, strict/renamed inheritance, typed JSON fidelity, encrypted selected secret snapshots and metadata-only tags                                                             | Implemented; latest upload remains independent from active traffic                              |
| One atomic code/secret revision                           | Managed publication and bulk-secret tests, concurrent writers, commit/unknown-outcome recovery and real workerd secret-pair execution                                                                                   | Implemented; failed stored uploads can remain latest while traffic rolls back                   |
| Native mutation interoperability                          | Native deploy, vars, secret put/delete/list against managed snapshots on memory/PostgreSQL; connection reopen                                                                                                           | Implemented; process/server crash qualification belongs to step 10                              |
| Scoped asset manifests, uploads and completion references | HTTP/parser race fixtures, delegated-token isolation, hash/size checks, expiry, first-attachment binding, native asset serving and combined adapter/PG/MinIO concurrent upload, read, restart and staging-removal drill | Bounded real-storage load passes; production soak and broader clients remain unqualified        |
| Retained artifacts, retries and rollback                  | Request identities are keyed before current-state resolution; HTTP retry after rollback, expired staging replay and pinned asset/secret execution                                                                       | Implemented; no startup latency/readiness is fabricated                                         |
| Bulk management and list/query behavior                   | Single-tag inclusion/exclusion shared by listing/deletion; atomic repository deletion, PostgreSQL trigger rollback and configuration reconciliation                                                                     | Implemented for the documented filter; unsupported pagination/compound syntax remains rejected  |
| Error/endpoint documentation                              | Supported adapter endpoint reference, native scopes, explicit generic error mappings and deletion outcome states                                                                                                        | Documented; compatibility routes are separate from generated native OpenAPI                     |
| Representative SDK flows                                  | Official JavaScript SDK 7.3.0 fixture on memory/PostgreSQL                                                                                                                                                              | Qualified with manual multipart adaptation; generated defaults remain incompatible              |
| Representative Wrangler flows                             | Installed Wrangler 4.54.0 namespace and plain-module upload/update fixture; non-loopback network guard                                                                                                                  | Qualified with profile-header adaptation only; broad CLI portability is not claimed             |
| Controlled live Cloudflare comparison                     | Supplied account returned 403/10121; user confirmed no enabled account                                                                                                                                                  | Blocked; exact live portability, hosted error mapping and runtime equivalence remain unverified |

The audit found one additional header edge: full script upload had no If-Match
implementation. It now rejects that header explicitly rather than silently
ignoring a caller's precondition. Per-script settings, secret, bulk-secret, tag
and content mutations retain their implemented If-Match handling. Regression
coverage is included in `TestCloudflareCompatibilityUploadHTTPContract`.

The most recent full suite before this final header check is
`/tmp/nf-step9-delete-full.log`, with PostgreSQL, baseline native workerd and the
pinned SDK/Wrangler fixtures enabled. The final adapter race rerun is
`/tmp/nf-step9-audit-race.log`. Current native-runtime deletion/asset checks pass
in `/tmp/nf-step9-delete-native-current.log`. Detailed evidence, source links,
known local differences and earlier publication/recovery tests are in the
[step 9 record](workers-for-platforms-step-9-status.md).

Remaining acceptance work is explicit:

- Run the live comparison when an entitled staging account becomes available;
  do not request credentials again or enable billing.
- Step 10 now records bounded real-storage, process/server failure, capacity,
  physical pool, monitoring and rollback qualification. Production expansion
  still needs actual workloads, soak and deployment-specific operations.
- The requested independent subagent review completed, including fixes and
  re-review of all three findings. See the [review record](workers-for-platforms-independent-review.md).

Real CPU enforcement remains the user's separate workstream. Hardened untrusted
execution remains unavailable. Expanding beyond the
[supported adapter contract](../architecture/cloudflare-management-adapter.md)
requires explicit implementation and evidence, not inference from these passing
local fixtures.

The later [combined asset drill](../../scripts/wfp-qualification/results-asset-storage.json)
adds concurrent adapter publication into real PostgreSQL/MinIO, native content
checks, restart recovery and independence of published artifacts from staging.
The [step 9 addendum](workers-for-platforms-step-9-status.md)
records its bounded scope. This updates the earlier storage/load gap without
substituting local results for the missing live comparison.
