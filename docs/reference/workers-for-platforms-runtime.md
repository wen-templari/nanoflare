# Workers for Platforms runtime contract

For application-facing restrictions, cancellation support, local capability
checks and exception diagnostics, see
[the private dynamic runtime guide](../guides/private-dynamic-runtime.md).

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

The implementation uses a 30-second root wall/effect deadline, 10,000 shared effect units, maximum depth eight, and initial admission/descriptor/database/HTTP bounds detailed in the step records. These are implementation defaults for qualification, not measured tenant capacity or Cloudflare accounting parity. The runtime internally enables `nodejs_als` for invocation context.

Dispatch acquisitions consume one shared root unit each. Resource effects share the remaining root budget across parent and child dispatches; SQL statements consume units individually. Creating a child dispatch does not replenish the root budget.

## Rejected, deferred and independent work

Untrusted execution profiles, custom WFP CPU/outbound/limit options, classic scripts, WebSockets, arbitrary cross-Worker service RPC, dynamic namespace-wide nested dispatch, schedules, custom DNS profiles, Cache API and presigned object URL effects are unavailable. Unsupported dispatch options are rejected explicitly. Native control JSON rejects unknown fields; it does not accept a limit and silently substitute a weaker one.

Real CPU accounting/interruption is the user's [separate workstream](../plans/workers-for-platforms-cpu-enforcement.md). The wall/effect deadline cannot stop a JavaScript infinite loop. Customer cells, hardened OS/VM containment and independent security qualification remain gates before an untrusted release.

Developer tools, effective-capability reporting and customer telemetry are implemented. The [management adapter](cloudflare-management-api.md) provides a separate Cloudflare-shaped subset over this native contract. Bounded local capacity, storage/recovery and rollback qualification and independent review are recorded in the [archive](../archive/workers-for-platforms/README.md). Live comparison remains blocked by account entitlement; broad Wrangler/SDK, runtime/security equivalence and production expansion remain unqualified.

## Namespace DNS lookup

With effective Node.js compatibility, dynamic namespace scripts use Nanoflare's
adapter for callback and promise `lookup()`, A/AAAA `resolve()`, `resolve4()`,
`resolve6()`, `resolveTxt()`, `resolveMx()`, `resolveCname()`, `resolveSrv()`,
`resolveNs()`, `resolvePtr()` and `reverse()` requests (including bare imports
under Node.js compatibility v2).
The invocation broker resolves through `nanoflared`'s configured `defaultProfile`,
including with a remote runner. Configure `NANOFLARE_DNS_CONFIG` on `nanoflared`
for private/corporate names. Scripts cannot select another profile. Each DoH
fetch consumes one shared outbound effect unit; an A+AAAA lookup can consume two.
Resolution is canceled with the invocation's deadline or cancellation. Revision
reuse does not retain invocation capabilities.

Namespace DNS uses the platform default resolver for all authorized trusted
namespace scripts with Node.js compatibility by default. Set
`NANOFLARE_WFP_DNS=false` (`--wfp-dns=false`) on nanoflared to reject their JSON
DoH requests without contacting either the corporate resolver or public DoH.
This switch does not change ordinary Worker DNS or arbitrary outbound fetch policy.
Enable this pilot only for code trusted to query that resolver, including its
private names. Socket opt-in does not restrict DNS queries.

Persistent pools that create or reconnect connections in a background task
started by an earlier invocation are unsupported in this pilot. This includes
the node-oracledb thin pool background connection loop: waking it from a later
request does not change its async context or refresh the original lease. Use
request-scoped connections and close them within the invocation.

The adapter answers the pinned workerd `node:dns` implementation's JSON DoH
fetches at `https://cloudflare-dns.com/dns-query`. Uploaded modules and the
dispatch artifact format are unchanged. The profile's Go resolver answers
TXT, MX, CNAME, SRV, NS and PTR as well as address lookups, sharing configured
server selection, timeout and cancellation. SOA, CAA, NAPTR and ANY remain
unsupported and return NOTIMP locally. Explicit JSON fetches with a `name`
parameter to that same endpoint also receive platform DNS answers; binary
DoH (`?dns=`) and requests without `name` retain ordinary outbound behavior.
Trailing-dot hostnames and the default HTTPS port are normalized in both adapters.
`lookup()` maps resolver failures and broker
admission errors to `ENOTFOUND`; other DNS APIs may expose native transport
errors such as `EBADRESP`. Timeout and terminal invocation codes from the former
shim are not preserved. Retry policies must be bounded.
The host lookup adapter uses a synthetic TTL of 30 seconds and maps an absent
address family to NXDOMAIN to avoid workerd's empty-success result behavior.
Native CI tests pin these implementation details for workerd upgrades.

DNS resolution does not grant socket access: TCP/TLS connections remain subject
to the executor's separate socket policy below. Split deployments with WFP or
namespace DNS disabled ignore nanoflared DNS settings. With both enabled,
nanoflared validates its active resolver config at startup and exits on invalid
configuration (see [release notes](../../CHANGELOG.md#unreleased)), but
does not validate or bind the local DNS adapter listen address. The outbound
transport calls that resolver in-process, so private DoH does not traverse the
HTTP proxy. Missing resolver configuration fails closed at the intercepted
endpoint rather than falling back to public DoH.


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
`public` and `private`. Explicit `public` is supported; it is never
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

### Protecting platform networks

`NANOFLARE_WFP_SOCKET_DENY` (`--wfp-socket-deny`) adds comma-separated destination
rules to the service's workerd `deny` list. The `local` category, loopback ranges
`127.0.0.0/8` and `::1/128`, and link-local ranges `169.254.0.0/16` and `fe80::/10`
are denied by default, including when the allow list contains narrower addresses.
Link-local denial protects cloud metadata endpoints such as `169.254.169.254`
and link-local container or VM services even when `private` is allowed.
Allowing `local` requires the unsafe administrator opt-in
`NANOFLARE_WFP_SOCKET_ALLOW_LOCAL=true` (`--wfp-socket-allow-local`); this also
removes the automatic loopback and link-local denies. Explicit deny rules remain configured. Custom allow/deny rules otherwise follow
workerd's longest-prefix matching: a more specific allow can override a broader
custom deny, so avoid such overlaps. The automatic local guard adds matching
denies for narrower local allow CIDRs to prevent that bypass. The
native test fixture opts into local access solely to reach its isolated test
servers, and separately verifies default loopback denial and explicit denies.

Allow and deny entries filter destination IP addresses only; they do not filter
ports. An allowed address is reachable on every TCP port. Prefer individual
database hosts (`/32` for IPv4 or `/128` for IPv6). When broader corporate ranges
are necessary, explicitly deny the Nanoflare host's own IP, the corporate proxy
host's IP, and platform subnets. A future database binding or port-aware adapter
is needed for port-level controls.

Allowed CIDRs must not overlap the platform's own networks. In particular,
`private` includes Docker bridge networks and potentially Nanoflare's Postgres,
MinIO, Loki, and other internal services. Startup warns when `private` is used;
prefer database-specific CIDRs and explicitly deny platform subnets, for example:

```sh
NANOFLARE_WFP_SOCKET_ALLOW=10.20.0.0/16
NANOFLARE_WFP_SOCKET_DENY=10.20.10.0/24
```

On static hosts, the platform host Worker itself also receives the internal socket
binding and loader capability. Its code can use them to make direct connections
under this socket policy, bypassing the general outbound allow list and corporate
proxy. Internal binding names are not a security boundary: only trusted platform
host code may run with these capabilities. The native dispatcher keeps the binding
on its internal bridge, rather than passing it to the dynamically loaded host.

Each nanoflared and remote runner logs its own effective enabled state, allow/deny
rules, and additional certificate count at startup, plus a warning if local access
is opted into. Certificate contents are never logged. Configuration is executor-local;
these logs make mismatches visible but do not enforce cross-process equality.

The `WFP native sockets` GitHub Actions job installs the repository-pinned
`workerd@1.20260911.1` and runs the native contract tests, including TLS trust and
hostname rejection, destination denial, nested dispatch, and brokered fetch.
