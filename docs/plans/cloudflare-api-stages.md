# Cloudflare compatibility merge scope

Merge branch: `codex/cloudflare-compatibility`.
Base: `a1be0ee9b5df87deb7d84bd2e30b547d87be3c9b` (`main`, confirmed through GitHub).
Original work, including stage 6 experiments, is preserved at
`codex/cloudflare-api-stages` (`8383c0b`).

## Included stages

1. **Local baseline:** retain the native shared-workerd serving architecture and
   existing authorization boundaries.
2. **Pinned SDK 7.3.0 workflow:** scalar/list multipart metadata, settings,
   filename-keyed module uploads, assets, secrets, actual dispatch and binary
   Wasm lifecycle. Ambiguous binding arrays and explicit empty-list changes use
   documented JSON metadata adaptations.
3. **Pinned Wrangler 4.54.0 workflow:** dispatcher provisioning/probes, asset
   hashing and upload, secret lifecycle, deletion and binary Wasm deployment.
   Namespace-secret and customer-cleanup adaptations remain explicit; this is
   supported workflow compatibility, not every Wrangler command.
4. **Hosted parity:** still externally blocked. The available access returned
   403/10121 and the user confirmed no WFP-enabled Cloudflare account is available.
5. **Trusted local persistent pilot:** keep runner-session/publication recovery
   fixes and the isolated Docker pilot, backup/restore, upgrade/rollback, D1/Wasm,
   retention, load and alert tooling. Bounded local evidence is not production
   soak or remote-production qualification.

The API reference documents supported routes, opt-in enablement and differences:
[Cloudflare management API](../reference/cloudflare-management-api.md).
See [client fixtures](../../scripts/wfp-client-flows/README.md) and
[pilot operations](../../docker/pilot/README.md) for repeatable commands.

## Stage 6 deferred

This branch excludes the dedicated-process CPU supervisor, exclusive admission
and shared CPU ledger extensions, CPU invocation compiler, syscall/bootstrap and
namespace code, invocation-specific cgroups/egress brokers, kernel-timer preload,
and custom workerd drain patches and their test/evidence files.

The serving architecture remains a shared workerd generation. Process-wide CPU
measurement or termination does not provide per-Worker/invocation attribution or
isolated interruption in that architecture. Refinement must target trusted
runtime/isolate/invocation hooks and shared-process peer-safety qualification.
No untrusted execution profile or CPU guarantee is enabled by this merge.

Independent fixes discovered during experiments are retained where they apply to
shared serving: binary Wasm immutability and class Worker resource adapters at
construction. Only the Wasm validation/deep-copy changes are retained in execution
admission; experimental exclusive admission methods are excluded.

## Evidence and validation

Retained stages 1–5 reports identify their original source hashes/commits. They
are historical development-branch evidence, not proof that this extracted branch
produced those earlier results. Fresh merge-branch checks are recorded separately
in `results-cloudflare-compatibility-merge.json` under `scripts/wfp-qualification`.
Hosted parity and full production qualification remain open regardless of local
checks passing. Private pilot credentials, databases and backups remain ignored
and excluded from Docker image build contexts.
