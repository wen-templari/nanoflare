# Workers for Platforms runtime contract

Updated 2026-10-22. Current native contract for the disabled-by-default
`trusted_shared_workerd` pilot. Bounded local qualification and independent
review are complete for their recorded scope; production rollout, untrusted
execution and live Cloudflare portability remain unqualified. See the
[current architecture](../architecture/workers-for-platforms.md),
[integration guide](../guides/workers-for-platforms.md) and
[remaining work](../plans/workers-for-platforms-remaining-work.md).

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

Untrusted execution profiles, custom WFP CPU/outbound/limit options, classic scripts, WebSockets, arbitrary cross-Worker service RPC, dynamic namespace-wide nested dispatch, schedules, custom DNS profiles, Cache API and presigned object URL effects are unavailable. Unsupported dispatch options are rejected explicitly. Native control JSON rejects unknown fields; it does not accept a limit and silently substitute a weaker one.

Real CPU accounting/interruption is the user's [separate workstream](../plans/workers-for-platforms-cpu-enforcement.md). The wall/effect deadline cannot stop a JavaScript infinite loop. Customer cells, hardened OS/VM containment and independent security qualification remain gates before an untrusted release.

Developer tools, effective-capability reporting and customer telemetry are implemented. The [management adapter](cloudflare-management-api.md) provides a separate Cloudflare-shaped subset over this native contract. Bounded local capacity, storage/recovery and rollback qualification and independent review are recorded in the [archive](../archive/workers-for-platforms/README.md). Live comparison remains blocked by account entitlement; broad Wrangler/SDK, runtime/security equivalence and production expansion remain unqualified.


## Opt-in namespace TCP and TLS sockets

Namespace sockets are disabled by default (`globalOutbound: null`), independently
of standalone Worker egress. Configure both nanoflared and each remote runner
that executes platform hosts:

```sh
NANOFLARE_WFP_SOCKET_ALLOW=10.20.0.0/16
NANOFLARE_WFP_SOCKET_CA_FILES=/etc/nanoflare/database-ca.pem
```

The equivalent flags are `--wfp-socket-allow` and `--wfp-socket-ca-files`.
Allow entries are comma-separated IP addresses, CIDRs, or workerd categories
`public`, `private`, and `local`. Explicit `public` is supported; it is never
implied. Empty or whitespace-only input disables sockets. CA files alone do not
enable networking. Invalid destinations, unreadable files, and invalid certificate
PEM fail startup. Certificates are loaded at startup; restart to rotate them.
Browser CAs remain trusted alongside configured PEM certificates.

A dedicated `nanoflare-wfp-sockets` network service supplies namespace
`globalOutbound` on both static hosts and the dynamic dispatcher. It dials directly,
bypassing the corporate HTTP proxy and its routing/CA configuration. Socket policy
is independent of `NANOFLARE_WORKERD_NETWORK_ALLOW`. Ordinary namespace `fetch()`
continues through the invocation broker and its accounting. Native outbound that
bypasses the JavaScript fetch override can reach the socket service's allowed ranges.

Both `secureTransport: "on"` and `startTls()` support verified TLS. Connect using a
hostname matching the server certificate; an untrusted CA or wrong hostname is
rejected. The proxy-style `nanoflare-egress` service cannot supply workerd-managed
socket TLS in workerd 2026-09-11 and is not used for namespace sockets.

All namespaces on an executor share this allow list and trust store. Sockets do
not consume broker effect units and have no per-tenant destination grants or
broker-enforced invocation deadlines. No socket-specific lifetime, idle timeout,
or concurrency limit is added here; applications must close sockets and bound
operations. Runtime socket/request ownership still applies, but broker expiry is
not a socket revocation mechanism. This remains part of the trusted shared pilot.

PostgreSQL supports plaintext and TLS. MySQL TLS requires the customer deployment
compatibility flag `no_throw_on_not_implemented_tls_options`: mysql2 supplies an
unsupported `checkServerIdentity` callback, while workerd still performs native
certificate and hostname verification. Nanoflare does not add that flag silently.
Oracle Thin plaintext is supported; TCPS needs database-specific qualification.
SQL Server through tedious remains plaintext-only: `encrypt: true` requires TLS
on a JavaScript duplex unsupported by workerd, and strict mode uses unsupported
ALPN options. Use an HTTP gateway for SQL Server TLS. Standalone Worker socket TLS
through proxy egress is outside this configuration's scope.
