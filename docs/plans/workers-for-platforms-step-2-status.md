# Step 2 — Runtime capability authorization

Updated: 2026-10-18. Status: implemented and verified locally in the working tree. Not committed or deployed. This closes the two reproduced authorization defects; it does not qualify untrusted customer execution or implement dispatch namespaces.

## Implemented contract

Each generated runtime credential is an HMAC-signed `nfc1` grant containing organization, Worker, exact deployment, capability kind and resource ID. The existing private app token is the signing key and is no longer sent as a Worker-facing runtime credential. Kinds are KV, database, objects, assets, durations and logs. Each resource binding grants its supported operations on that resource; this change does not introduce read-only bindings or per-operation policy.

The HTTP resource and telemetry adapters authenticate before processing the request. Missing, malformed, tampered and legacy app-wide credentials return 401. A valid credential for another handler/resource, another organization, an unbound resource or a stopped revision returns 403. Backend/runner errors deny access. Administrative repository calls may still use their existing internal credentials; empty credentials are never administrative authority at these HTTP boundaries.

Authorization loads the exact saved deployment snapshot rather than whichever revision currently receives traffic. Resource ownership and binding membership are checked against that snapshot. Assets likewise resolve from the signed revision. This works with both in-memory and PostgreSQL repositories and requires no new schema migration.

## Lifetime and revocation

The runtime manager tracks live prepared, active and draining generations separately from traffic selection. A draining revision keeps its grants while its process remains alive. Stop, abort, failed preparation and process exit revoke that generation's authority; a revision still loaded in another live generation remains authorized. Checks use a separate lock so startup/health checks cannot deadlock on authorization.

A remote runner exposes an authenticated `/v1/runtime-grants/check` endpoint. The control plane queries it for lifetime checks; an unsupported or unavailable runner fails closed. Standalone adapters without a runtime checker permit only stored active revisions. Reintroducing the same immutable revision in a live generation makes its revision grant valid again; credentials are scoped to revisions, not individual process instances. Existing object presigned URLs retain their own expiry (currently 15 minutes); revoking the runtime credential does not revoke a URL already issued by object storage.

## Telemetry identity

Duration identity is overwritten from the authenticated grant. Customer code can still submit fabricated measurements for itself; duration remains wall-clock instrumentation and must not be used as trustworthy CPU accounting or billing evidence.

Console logs and exceptions come from a native workerd tail Worker attached per deployment. Only that trusted tail receives the log capability; customer code receives the duration capability. The receiver assigns Worker/deployment identity from the log grant and ignores caller-selected identity fields. Shared stdout/stderr is unscoped diagnostic text: markers and continuation lines cannot assign or inherit Worker identity. Reserved internal module and binding names are rejected during deployment validation.

## Verification

Passed locally on macOS arm64:

- Full `go test ./... -count=1 -timeout=120s`, with an isolated PostgreSQL 17 database and the installed workerd 2025-12-10 native executable.
- Race checks for API, runtime, runner, config, domain, database and daemon packages; the expanded resource matrix also passed under the race detector.
- The same authorization matrix against memory and real PostgreSQL: valid KV CRUD, real SQLite query, object operations, object JSON/body substitution, resource ownership, wrong handler/header, missing/raw/tampered credentials, missing deployment, retired/draining revisions, unavailable runner, forged log identity, and old/new asset snapshots.
- Runtime process lifecycle checks, including aborted and crashed prepared generations, draining leases and shutdown; authenticated remote-runner checks and unavailable/unsupported runner denial.
- Native workerd KV, assets, objects and duration integration tests.
- Updated opt-in PoC on workerd 2025-12-10 and 2026-10-18: real native tail delivery attributes the forged victim marker to the attacker; victim telemetry remains empty; telemetry-to-unbound-KV access returns 403.

Evidence: [installed runtime results](../../scripts/archive/wfp-poc/results-step2.json), [candidate runtime results](../../scripts/archive/wfp-poc/results-step2-current.json). The original `results.json` and `results-current.json` remain historical baseline evidence. A passing full PoC still reports CPU/subrequest and egress defects as blockers.

To repeat repository security checks, set `NANOFLARE_TEST_POSTGRES_URL` to a disposable PostgreSQL database; the tests create and remove a dedicated schema. Without that environment variable the PostgreSQL portion skips. Use the native workerd executable on PATH for native integration tests.

## Rollout and remaining work

Runner and control-plane versions and regenerated workerd configs must be upgraded together: old configs send app-wide or missing telemetry credentials, which the new handlers deliberately reject. Regenerate/reload existing generations as part of deployment. No deployment has been performed here. Remote authorization adds a runner request to runtime adapter calls; capacity and latency qualification remain required before release.

Step 3 remains the next runtime investigation: prove CPU interruption, subrequest limits and an appropriate sandbox/failure boundary. The existing CPU/subrequest probes still fail, infinite loops block peers, and corporate proxy routing still bypasses the native network allowlist. Dispatch resources, production Worker Loader integration, cache isolation and Cloudflare compatibility remain later plan steps. The first supported product contract in step 1 still needs adoption.
