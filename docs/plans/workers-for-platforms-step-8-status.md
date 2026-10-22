# Step 8 — Operational visibility and developer tooling

Updated 2026-10-20. Status: **implemented and verified locally for the trusted subset**. Backend, CLI and console workflows pass local validation. The feature remains a disabled-by-default trusted shared-runtime subset; CPU enforcement and untrusted execution remain separate release gates.

## Implemented work

- Replaced the incorrectly named CPU p90 metric/API/chart with elapsed-duration p90. CPU accounting is explicitly unavailable. The overview presents the highest reporting Worker p90 rather than summing per-Worker percentiles.
- Added organization-authorized effective capability and publication-history APIs. Runtime flags, rejected features, wall/background/effect limits and unverified dynamic readiness are distinct. The actual runner version remains `unreported` rather than inferred from a compatibility ceiling.
- Added `nanoflare platform` namespace/script/grant/policy, deployment/traffic/rollback, artifact/capability/publication and local bundle-export commands. Project `worker_id` pins an existing immutable target and cannot fall back to public creation. `dispatch_namespaces` and exact service IDs are supported in project schema/types. Exported bundles include complete source, resource configuration and assets, and cannot overwrite an existing output file.
- Scoped output queries to the authenticated organization, Worker and retained revision even if a telemetry backend returns unexpected data. Queries have 16 concurrent slots, a ten-second context deadline, at most 1,000 lines, 512-byte search text and a 48-hour range. Unknown HTTP output filters are rejected. Loki results must have the selected Worker identity.
- Added dynamic console log collection through the invocation's authenticated RPC broker. The trusted service supplies organization, namespace, customer, Worker, revision and hashed request-chain/invocation/parent identifiers. Pinned literal and JSON-escaped secret values, runtime capability strings and configured patterns are redacted before collection. Limits are 4 KiB per message, 64 lines per invocation, 100 attempted lines and 256 KiB per Worker per minute; oversized messages are dropped. Sampling and enable/disable controls apply before collection. Output buffering is bounded globally and per Worker.
- Removed Vector text-marker identity promotion. Production Vector has only a Unix collector; a separate development configuration provides the opt-in local TCP collector. Structured JSON preserves correlation fields through Loki. The development collector is trusted local infrastructure, not an untrusted ingestion boundary.
- Added Go-clock dispatch observations and admission/cancellation counters. Elapsed dispatch time includes loading, I/O and response streaming; it is not CPU time. Completion/cancellation observations are emitted once and callbacks run after releasing admission locks. Main daemon wiring feeds duration telemetry and exports dynamic invocation counters.
- Restricted the global Prometheus exporter to an operator credential when control authentication is active. Empty credentials disable it; organization/runtime tokens do not grant access. Sample monitoring configuration uses a separate bearer-token file and binds Prometheus's host port to loopback.
- Added console namespace/script/grant management, full-bundle upload, runtime capabilities/publication history and log-chain display. Namespace operations use the live control API and optimistic policy revision checks. Existing Worker detail traffic/rollback operations are reused.
- Added `scripts/wfp-local-demo`, a loopback-only disposable local fixture using real native workerd and SQLite. Control metadata and KV are explicitly in memory; object storage and Cloudflare are not configured. It has synthetic local credentials and is not a staging deployment or production server.

## Verification evidence

- Native local/remote dispatch plus affected package suite passed after telemetry integration: `/tmp/nf-step8-logs-native.log`.
- Full Go/PostgreSQL/native suite passed: `/tmp/nf-step8-backend-full.log`.
- Memory/PostgreSQL log identity, nested correlation, redaction, quota, sampling, revocation and elapsed-observation tests passed: `/tmp/nf-step8-telemetry-policy.log`.
- CLI full suite including immutable private targets and complete bundle export passed: `/tmp/nf-step8-cli-project-tests.log`.
- CLI operations against an authenticated real control API, publication readiness and rollback passed in the backend suite.
- Final full Go/PostgreSQL/native suite passed: `/tmp/nf-step8-final-full.log`.
- Final race checks passed across service, API, runtime, logs, metrics and CLI: `/tmp/nf-step8-final-race.log`.
- The native local/remote integration test now verifies the customer dispatch observation sink and duration attribution. It passed on baseline workerd 2025-12-10 and current candidate 2026-10-18: `/tmp/nf-step8-final-current-native.log`.
- Schema typechecking and production UI build passed: `/tmp/nf-step8-final-schema.log`, `/tmp/nf-step8-final-ui-build.log`. The existing large-bundle advisory remains.
- Production and development Vector configurations validate against Vector 0.46.1, with environment/health checks intentionally excluded: `/tmp/nf-step8-vector-prod-validation.log`, `/tmp/nf-step8-vector-dev-validation.log`. This is configuration validation, not a live Loki pipeline test.
- Headed browser verification used a disposable native fixture: create/rename/delete namespace; create/delete private scripts; explicit customer KV/DB grants with preserved policy revision; export and upload a full bundle; update `ui-v2` to `ui-v3`; roll back through Deployments and confirm native dispatch returns `ui-v2`; verify private ingress labels, truthful CPU/readiness reporting and log-chain IDs. The expected free-tier Worker limit was surfaced correctly; an owned disposable script was removed before continuing.
- Desktop and 390px mobile layouts were inspected. Fixed mobile sidebar behavior, automatic menu closing on navigation, bounded namespace IDs, scrollable tables and wrapping action controls. Screenshots are local verification output under `output/playwright/` and are excluded from the commit.
- The final fixture builds and serves both customers using real native workerd, KV, SQLite and native platform RPC. Its authenticated capability response records real completed dispatches and elapsed duration. These are local observations, not live Cloudflare evidence.

An editing mistake overwrote the dispatch source during this work. It was restored by replaying that file's recorded edits, then its authorization and real native local/remote integration suites were rerun successfully. No production deployment was affected.

## Operational notes and remaining release qualification

The [local development example](../../scripts/wfp-local-demo/README.md) describes dispatch, full-bundle updates, rollback and inspection. [Operator migration notes](workers-for-platforms-operations.md) cover the metric rename, authenticated scraper configuration, sampling/redaction, collector trust and honest capability limits.

Step 10 still needs the live collector/Loki path, production filesystem permissions, production-like resources, sustained capacity and crash/recovery qualification. Shared runtime safety and real CPU interruption remain separate gates. Module-initialization logs outside invocation context are unavailable. Dynamic publication readiness remains unverified until a separate readiness mechanism can attest it. No commit, release or deployment was made.

The Cloudflare staging comparison remains blocked by account entitlement as recorded in the [live comparison status](workers-for-platforms-live-comparison.md). This does not prevent local step 8 or compatibility adapter implementation.
