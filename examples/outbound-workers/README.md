# Outbound Workers

Three small Workers demonstrate hostname filtering, platform-owned authentication
and correlated logs. Replace `api.example.com` with your test API in both
`customer.js` and `outbound.js`. The API token belongs only to the outbound
Worker; the customer Worker never receives it.

An operator must enable platform dispatch. The Cloudflare-compatible upload path
also requires the compatibility adapter. See the
[WFP guide](../../docs/guides/workers-for-platforms.md) for credentials, namespace
creation and multipart uploads; no Cloudflare account is needed for Nanoflare.

Deploy in this order:

1. Upload `outbound.js` as an ordinary modules Worker named `outbound-policy` and
   set its `API_TOKEN` secret through ordinary Worker secret management.
2. Create namespace `outbound-demo`, then upload `customer.js` as its private
   dynamic script `demo`.
3. Upload `dispatcher.js` as an ordinary Worker with the following binding in
   its multipart metadata (alongside `main_module` and `compatibility_date`):

```json
{
  "name": "CUSTOMERS",
  "type": "dispatch_namespace",
  "namespace": "outbound-demo",
  "outbound": {
    "worker": { "service": "outbound-policy" },
    "params": [{ "name": "customerContext" }]
  }
}
```

The equivalent Wrangler configuration for the dispatcher is:

```jsonc
{
  "name": "outbound-dispatcher",
  "main": "dispatcher.js",
  "compatibility_date": "2025-12-10",
  "dispatch_namespaces": [
    {
      "binding": "CUSTOMERS",
      "namespace": "outbound-demo",
      "outbound": {
        "service": "outbound-policy",
        "parameters": ["customerContext"],
      },
    },
  ],
}
```

Nanoflare's CLI resolves `namespace` and `outbound.service` to immutable IDs.
Native deployment bundles instead use `namespace_id` and `outbound.worker_id`.
`nanoflare platform bundle bundle.json` exports a complete bundle. In the console,
open the dispatcher's Runtime tab, upload the bundle, select its outbound Worker
and parameter names, then deploy the revision. Inspect the active binding table
and the outbound Worker's output to verify the selected policy.

Parameters are JSON data, declared by name, limited to 16 KiB and snapshotted at
`get()`. Access them as `env.customerContext`, including inside the request handler
through `import { env } from "cloudflare:workers"`. Read request-local parameters
inside a handler or constructor, rather than capturing them at module startup.

Outbound policies use the ordinary Worker compilation and invocation path, preserving
their own variables, secrets, storage and service authority. Named fetch entrypoints
can be selected with `outbound.worker.entrypoint` in multipart metadata or
`outbound.entrypoint` in native/CLI configuration. Pinned Wrangler 4.54.0 does not
expose that field. Omit it to use the default export. Service environments select independent code and secrets through
`outbound.worker.environment` in multipart metadata or `outbound.environment` in
CLI configuration. Native bundles select the resolved environment Worker ID.

Policy-bound customers route native HTTP through the selected policy and cannot
open native TCP sockets. Policy forwarding does not re-enter its own hook.
Customers reached through a binding without outbound retain the existing socket
policy. Nested dynamic service calls inherit an attached policy. Request-bound
capabilities currently use anonymous loader descriptors, so warm-isolate reuse
and its performance must not be assumed. Check the
[compatibility contract](../../docs/reference/outbound-workers-compatibility.md)
for tested versions, detailed coverage and remaining hosted comparison gaps. Target DNS
profiles follow the ordinary executor policy and cannot be selected by customer
query parameters or headers.

Target revisions and secret versions are pinned at customer admission. A later
policy deployment affects later customer admissions. Missing/deleted targets fail
closed. Host rollback restores binding configuration; it does not roll back the
independent outbound Worker's traffic. Root deadlines, cancellation, effect units
and background limits apply to the entire chain. This remains trusted shared
execution, with no claim of CPU enforcement or live Cloudflare parity.
