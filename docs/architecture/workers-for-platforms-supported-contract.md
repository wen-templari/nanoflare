# Workers for Platforms — first supported contract

Date: 2026-10-19. Status: frozen native implementation contract for the disabled-by-default trusted pilot. This does not qualify a production rollout, untrusted customer code or Cloudflare client portability. The [execution decision](workers-for-platforms-execution.md) defines future untrusted boundaries; [steps 6](../plans/workers-for-platforms-step-6-status.md) and [7](../plans/workers-for-platforms-step-7-status.md) record the implemented subset and evidence.

## Identity and deployment

An organization owns its namespaces, Workers and resources. A namespace groups one platform/environment; it is neither a customer authorization boundary nor a physical sandbox. `customer_mode: organization` uses the organization as the trust domain. `customer_mode: platform` requires an explicit immutable `customer_id` for customer scripts, with resource grants for that exact customer. Deliberately shared resources require grants for every participating customer.

Workers have immutable IDs and unique script names inside a namespace. Namespace/script membership, customer identity and execution mode cannot change through a rename or deployment. Customer scripts are private: they have no public HTTP/asset ingress. Platform-controlled authentication occurs before namespace dispatch. Exact-target nested service fetches are limited to the same namespace/customer; a service grant does not grant namespace-wide lookup.

Namespace trust defaults to `trusted`, the only available profile. Script execution defaults to existing static behavior. `execution: dynamic` is an explicit creation-time choice and requires `--enable-platform-dispatch`; this flag defaults to false. The current trusted implementation shares the existing workerd pool, so an infinite loop or process failure can affect peers. Namespace membership does not imply an independently stoppable customer cell.

Revisions retain native immutable deployment IDs and weighted traffic. Each invocation selects one authorized revision and pins source, configuration, assets, secret versions and policy reference. Current resource revocation overrides the historical grant reference. Publication is staged and recoverable; secret/configuration updates preserve active weighted snapshots. Rollback uses retained artifacts rather than reading current secret values.

## Supported runtime matrix

| Surface                 | Initial contract                                                                                                                                      |
| ----------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| Engine                  | Experimental native Worker Loader, checked on workerd 2025-12-10 and candidate 2026-10-18; other builds need qualification                            |
| Platform namespace host | Module fetch-handler object and named native RPC exports; default classes rejected                                                                    |
| Customer handler        | Module fetch object or `WorkerEntrypoint` subclass                                                                                                    |
| Dispatch                | `env.NAMESPACE.get(script, { props }, {}).fetch(request)`                                                                                             |
| Props                   | Per-invocation native values/RPC capabilities, never cached in loader configuration                                                                   |
| Nested calls            | Exact declared dynamic service fetch, same namespace/customer, shared root budgets                                                                    |
| Lifecycle               | Streaming response, cancellation, at most two seconds background authority, original wall deadline retained                                           |
| Revision reuse          | Stable Worker/revision/digest keys; bounded code descriptors; fresh request-bound channels                                                            |
| Resources               | Explicit KV/database/object grants and revision-local assets; current grants rechecked on effects                                                     |
| HTTP outbound           | Scoped broker, administrator network allow policy, authorized/pinned DNS destinations, redirects counted, corporate credentials outside customer code |
| Cache                   | Explicitly unavailable for dynamic scripts; immutable code-descriptor reuse is separate                                                               |
| Accounting              | External atomic subrequest budget, SQL statement validation, wall/effect deadlines and admission bounds; no measured CPU limit                        |
| Publication/readiness   | Immutable artifacts and phased publication; a stored dynamic revision is not a prewarmed isolate                                                      |

The implementation uses a 30-second root wall/effect deadline, 100 shared effect units, maximum depth eight, and initial admission/descriptor/database/HTTP bounds detailed in the step records. These are implementation defaults for qualification, not measured tenant capacity or Cloudflare accounting parity. The runtime internally enables `nodejs_als` for invocation context.

## Rejected, deferred and independent work

Untrusted execution profiles, custom WFP CPU/outbound/limit options, classic scripts, WebSockets, native customer raw TCP, arbitrary cross-Worker service RPC, dynamic namespace-wide nested dispatch, schedules, custom DNS profiles, Cache API and presigned object URL effects are unavailable. Unsupported dispatch options are rejected explicitly. Native control JSON rejects unknown fields; it does not accept a limit and silently substitute a weaker one.

Real CPU accounting/interruption is the user's [separate workstream](../plans/workers-for-platforms-cpu-enforcement.md). The wall/effect deadline cannot stop a JavaScript infinite loop. Customer cells, hardened OS/VM containment and independent security qualification remain gates before an untrusted release.

Developer tools, effective-capability reporting and customer telemetry are step 8. Cloudflare-shaped management operations are a separate step 9 adapter over this native contract. Its controlled live comparison is [blocked by account entitlement](../plans/workers-for-platforms-live-comparison.md); no drop-in Wrangler/SDK or full Cloudflare runtime/security equivalence is claimed. Step 10 must qualify capacity, storage/recovery and rollback before any pilot deployment. The user-requested independent code review follows implementation.
