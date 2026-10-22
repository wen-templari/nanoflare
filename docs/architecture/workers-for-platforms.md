# Workers for Platforms architecture

Updated 2026-10-22. This describes the implemented, disabled-by-default
`trusted_shared_workerd` profile. The [runtime contract](../reference/workers-for-platforms-runtime.md)
and [management API](../reference/cloudflare-management-api.md) define supported
external behavior; the [integration guide](../guides/workers-for-platforms.md)
shows how applications use it.

## Identity and authorization

An organization owns Workers, namespaces and resources. Namespaces group scripts
and policy; they are not physical sandboxes. Immutable namespace and Worker IDs
anchor authorization. Names are resolved within an authenticated organization.
Organization-mode namespaces use the organization as the trust domain;
platform-mode namespaces additionally pin a customer ID to each customer script.
Resource sharing requires explicit grants for participating customers.

Customer scripts are private. A platform-controlled dispatcher authenticates
requests, authorizes targets and resolves scripts through its namespace binding.
Ordinary dispatcher uploads resolve names to immutable namespace IDs. Customer
scripts cannot bind dispatch namespaces; declared nested service fetches are
restricted to the same namespace/customer. Credentials and user-supplied tags
or headers cannot replace resource grants or trusted invocation identity.

## Publication and execution

Both native and compatible management operations publish through the native
immutable revision pipeline. Snapshots pin code, configuration, selected secret
versions, assets and policy references. Staged publication persists recoverable
intent before activation. Latest uploaded configuration and active weighted
traffic are separate: failed activation can preserve a complete upload without
serving it. Rollback selects retained snapshots instead of current secret values.
Idempotent request replay recovers the original result without reversing rollback.

The dispatch broker selects an authorized revision and issues request-bound
capabilities. Experimental native Worker Loader code descriptors are reused by
immutable Worker/revision/digest keys, with bounded descriptor count/bytes.
Invocation props and live authority are not cached in those descriptors.
This code cache is not a bound on native isolate memory or an eviction guarantee.

Invocation authority pins identity, revision, allowed resources, root effect
budget and deadlines. Nested calls share root accounting. Streaming,
cancellation and bounded background work retain the original authority lifetime;
new effects cannot refresh it. Revocation prevents new effects, while already
accepted I/O may finish. KV, database, object and outbound effects recheck current
authorization at trusted handlers. Outbound HTTP uses the configured network
policy, authorized/pinned DNS destinations and corporate transport settings.

## Management and operational boundaries

The optional `/client/v4` adapter translates the supported Cloudflare request
subset into native operations and returns Cloudflare-shaped envelopes. Native
`/v1` administration, organization permissions and traffic controls remain
authoritative. Adapter routes are documented separately from native OpenAPI.
Ordinary compatible upload does not provision hostname, DNS or TLS.

CLI and console expose namespaces, private scripts, capabilities, publication
and telemetry. Runtime credentials protect private resource/identity handlers;
logs and metrics use trusted identity rather than caller-selected attribution.
Physical monitoring describes runner/cgroup resources, not per-customer billing.
The [operations guide](../guides/workers-for-platforms-operator.md) and
[runbook](../guides/workers-for-platforms-operator.md) cover those controls and recovery.

## Current failure boundary and future design

Trusted dynamic Workers share the configured workerd pool. Wall deadlines and
external effect accounting do not interrupt synchronous infinite loops. A runtime
crash or memory exhaustion can affect peers. Container-level bounds qualify a
shared physical failure boundary; they do not isolate each customer.

Future untrusted execution needs real CPU interruption, a qualified hardened
sandbox/failure unit and resource/capability transport across that boundary.
The [original execution decision](../archive/workers-for-platforms/workers-for-platforms-execution.md)
contains customer-cell and typed-bridge proposals plus bounded experiments;
those proposals are not the implemented trusted architecture. Track the gates
in [remaining work](../plans/workers-for-platforms-remaining-work.md) and the
[CPU plan](../plans/workers-for-platforms-cpu-enforcement.md).
