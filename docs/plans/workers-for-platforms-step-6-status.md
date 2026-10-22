# Step 6 — Private dynamic dispatch

Updated: 2026-10-19. Status: implemented and verified locally for the documented trusted fetch subset, through local and authenticated remote runners. Nothing is committed or deployed.

## Implemented path

The daemon has an opt-in `--enable-platform-dispatch` flag, disabled by default. Namespace scripts may select immutable `execution: "dynamic"` at creation; existing/default script execution remains static. Only trusted namespaces are accepted. Enabling this flag does not enable an untrusted sandbox or CPU interruption.

A platform deployment declares `dispatch_namespaces: [{"binding":"DISPATCHER","namespace_id":"..."}]`. Adding namespace bindings requires both deployment write and namespace dispatch permissions. Bindings are frozen in the deployment artifact. Namespace ownership, name collisions and reserved runtime names are checked before publication. Plain-variable and secret updates cannot overwrite a dispatch binding.

The platform uses `env.DISPATCHER.get(scriptName, { props }, {}).fetch(request)`. Native props and RPC stubs are transferred through workerd's per-invocation props. Unsupported custom limits/outbound options are rejected instead of being ignored. The loader path supports module fetch handlers and customer `WorkerEntrypoint` classes; classic scripts are rejected. WebSocket dispatch is explicitly rejected with status 501. Custom dispatch options are rejected with status 422. Namespace hosts currently require module fetch-handler objects; platform default classes are rejected during deployment. Customer `WorkerEntrypoint` classes and named platform RPC exports are tested. Arbitrary service RPC and namespace-wide dispatch from dynamic customer code are unavailable.

Dynamic customer revisions are excluded from the static pool. The running platform resolves namespace/script identity through the real authenticated runtime API, reads its immutable artifact, and loads the selected revision. Code changes and traffic rollback leave an unchanged dispatcher process running. Dynamic publication reports runtime readiness as unverified because customer code is loaded lazily; storing/activating an artifact does not certify its runtime initialization or application behavior.

The JavaScript cache holds only code descriptors and plain revision-specific values, never request-bound loader/RPC stubs. A fresh `WorkerLoader.get()` call obtains the native channel for every invocation while using a stable revision/digest key for code reuse. Descriptor caching is bounded by 64 entries and 32 MiB of serialized code data. This is not a measured bound on the runtime's internal isolate cache/RSS; that remains a step 10 qualification item.

## Authority and lifetime

The Go ingress creates an opaque root record for the actual platform request and revision, replacing any caller-supplied root header. Root IDs are removed from requests delivered to customer code. The root has a 30-second wall deadline and a 100-unit shared counter; acquiring a dispatch consumes a unit. This is an effects/admission deadline, not CPU interruption.

Dynamic scripts may declare exact-Worker fetch service bindings inside the same namespace/customer boundary. Each child selects its target using the same native weighted selector, inherits the parent/root deadline and counter, and cannot acquire namespace-wide dispatch authority. Parent cancellation revokes descendants; maximum nesting depth is eight. Static callers use namespace bindings to invoke dynamic scripts; they cannot silently create an unusable direct dynamic service binding.

The broker selects one immutable revision per invocation. Its opaque lease pins source, secret versions, bindings and asset digests. Customer code receives a native RPC facade; it receives no management signing roots, namespace control binding, raw loader binding, or resource bearer tokens. KV, database, object and asset operations resolve a binding name against that lease, authorize its exact resource and current namespace/customer grant, then use the established handlers.

The root is cancelled on ingress disconnect/deadline independently of JavaScript stream-cancellation callback behavior. Normal completion reduces remaining effect authority to at most two seconds of background time without extending the original deadline. Source pools are held through that bounded window; the remote runner also retains ingress references through the window. Child EOF/cancellation further narrows its lease. Repeating completion cannot renew it. Current namespace revocation still overrides frozen policy references.

Runtime credentials now optionally bind to a unique process-instance ID, in addition to Worker/deployment/resource. Both local and remote liveness checks validate that ID. A replacement running the same deployment cannot revive a retired instance's credentials. A control-plane restart drops invocation handles and fails their effects closed. Native shared-pool CPU/memory failure impact is unchanged; mutually untrusted execution remains unavailable.

Current admission defaults are 1,024 live roots/leases overall, 128 leases per namespace and 32 per namespace/customer boundary. Expired records are reclaimed on admission/access. These defaults are implementation bounds awaiting capacity qualification, not a production capacity claim.

## Evidence so far

- Full Go suite with real PostgreSQL passed after the initial integration.
- Native integration on workerd 2025-12-10 passes through the real control API, JWT authorization and both local manager and authenticated remote runner.
- Customer deployment, immutable lookup, source update, traffic rollback and private-route rejection pass without restarting an unchanged dispatcher.
- Old response streams retain old assets while a newer revision is selected for new calls.
- Twenty-four concurrent calls retain distinct native props, RPC identity and KV state.
- Customer module handlers and `WorkerEntrypoint` classes receive the pinned secret/RPC context.
- KV, real SQLite execution and object read/write facades pass; object ownership/path checks use the expected key.
- Client disconnect prevents the delayed resource write in the native cancellation scenario.
- Memory/PostgreSQL lease tests cover revision/resource substitution, management-root rejection, atomic counter reservations, caller substitution, explicit cancellation, current grant revocation, dead source processes and control-plane restart.
- Unit tests cover background-window non-renewal and process-instance replacement/draining.
- Generated contracts and schema/UI typechecks pass. The full Go suite and service/API/database/runtime/runner race suite pass against PostgreSQL. The complete native local/remote lifecycle scenarios also pass on workerd 2026-10-18. Logs are under `/tmp/nf-step6-*.log` and are transient local evidence.

## Additional verification

The final integration includes exact-target nested service fetches. Memory/PostgreSQL tests cover shared parent/child budget exhaustion, descendant cancellation, current child selection, cross-namespace/customer rejection, native weighted selection invariants and the depth bound. Native code intentionally retains an old RPC connector and resource broker across requests; both are rejected while fresh request capabilities work.

Both local and remote tests stop their own dedicated workerd process, wait for runner recovery, and confirm that old invocation credentials and ingress roots remain unusable even when the replacement serves the same deployment. New calls then succeed with the new process instance.

Evidence: `/tmp/nf-step6-complete-tests.log`, `/tmp/nf-step6-complete-race.log` and `/tmp/nf-step6-complete-current.log`. Generated schema/type checks and the UI typecheck also passed. These are transient local logs, not deployment or live Cloudflare evidence.

## Boundaries for the following steps

Step 7 now supplies the documented [outbound/cache/resource policy](workers-for-platforms-step-7-status.md), including scoped HTTP fetch, SQL statement accounting and SQLite file/attachment/pragma restrictions. Its status record supersedes this step 6 baseline for these capabilities. Production-like resource and capacity qualification remains step 10. Object effects intentionally exclude presigned URL operations so an invocation cannot mint an independent URL capability that outlives its lease.

Step 8 adds developer tools, customer-attributed telemetry and effective-capability reporting. Step 10 must measure memory/isolate-cache behavior, sustained load, storage failure/recovery and a production-like deployment configuration. The disabled pilot flag is not a rollout qualification or authorization to deploy. CPU enforcement and hardened untrusted execution remain separate unresolved release gates.
