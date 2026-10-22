# Workers for Platforms integration guide

Updated 2026-10-22. For third-party applications deploying and invoking trusted
customer Workers on Nanoflare. Native dispatch and the Cloudflare-shaped API
adapter are opt-in. Untrusted code hosting and drop-in Cloudflare portability
are not supported by the current release contract.

## Using Cloudflare documentation

Use Cloudflare's [WFP overview](https://developers.cloudflare.com/cloudflare-for-platforms/workers-for-platforms/)
and [dynamic dispatch guide](https://developers.cloudflare.com/cloudflare-for-platforms/workers-for-platforms/configuration/dynamic-dispatch/)
for the namespace, dispatcher and user Worker concepts. Use this guide and the
[Nanoflare API reference](../reference/cloudflare-management-api.md) for actual
requests, supported fields, credentials and limits. Changing only the Cloudflare
API URL is insufficient for an arbitrary Cloudflare SDK or Wrangler application.

| Surface                     | Nanoflare contract                                                                                                 |
| --------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| API account                 | Nanoflare organization ID, with Nanoflare bearer credentials                                                       |
| Namespace scripts           | Private Workers reached through an authorized dispatcher                                                           |
| Dispatcher upload           | Ordinary Worker multipart PUT with a `dispatch_namespace` binding                                                  |
| Bindings                    | Text, JSON, secret text, KV, D1, R2, services and namespace assets; native ownership/grants apply                  |
| Management                  | Namespace/script operations, settings, content, secrets, tags and assets within the reference's route/field subset |
| Dispatch options            | Per-invocation `props`; custom CPU, limits and outbound options are rejected                                       |
| Execution                   | Shared trusted workerd; no CPU interruption or hardened customer sandbox                                           |
| Hosting                     | Native hostname configuration with separately managed DNS/TLS                                                      |
| Cloudflare account services | Access policy management, billing, token verification and general provisioning are outside the adapter             |

## Prerequisites

An operator must configure persistent metadata/artifact storage, control
authentication, a qualified native workerd executable and routing. Enable both:

```text
NANOFLARE_PLATFORM_DISPATCH_ENABLED=true
NANOFLARE_CLOUDFLARE_COMPATIBILITY_ENABLED=true
```

Equivalent daemon flags are `--enable-platform-dispatch` and
`--enable-cloudflare-compatibility`. The adapter cannot be enabled without
native dispatch. See the [pilot runbook](workers-for-platforms-operator.md)
for deployment setup and recovery.

Obtain an organization-scoped Nanoflare personal access token, or an authorized
session/OAuth token. The example needs `namespaces:read`, `namespaces:write`,
`namespaces:dispatch`, `workers:read`, `workers:write`, `deployments:write` and
`secrets:write`. Upload requires `secrets:write` even without secret bindings.
Keep this control credential in your platform backend. Authenticate users and
authorize target scripts in the dispatcher before dispatching their requests.

Enabling native dispatch is the administrator's consent to the only supported
profile, `trusted_shared_workerd`. Namespaces share a runtime failure boundary;
there is no per-namespace runtime isolation or CPU interruption. Applications do
not send execution consent headers. The legacy `X-Nanoflare-Execution-Profile`
request header is ignored; responses report the effective profile in that header.
Cloudflare's `trusted_workers` field remains unsupported. The delegated asset-file
upload uses its own upload JWT.

## Deploy and invoke a minimal application

Use a new namespace and dispatcher name for this walkthrough. Requirements:
POSIX shell, curl and jq. Run file creation in an empty working directory.
Replace the origin, organization ID and token with your deployment values.
The example uses an organization-mode namespace and a fixed trusted target.

```sh
export NANOFLARE_ORIGIN='https://api.nanoflare.example'
export NANOFLARE_ORG_ID='your-organization-id'
export NANOFLARE_TOKEN='your-nanoflare-token'
export NANOFLARE_ACCOUNT_API="$NANOFLARE_ORIGIN/client/v4/accounts/$NANOFLARE_ORG_ID"
```

### 1. Create a namespace

```sh
curl --fail-with-body "$NANOFLARE_ACCOUNT_API/workers/dispatch/namespaces" \
  -H "Authorization: Bearer $NANOFLARE_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"name":"integration-demo"}'
```

The adapter creates an organization-mode namespace. For platform-owned customer
identities, create a namespace through the native API with
`customer_mode: "platform"`, then supply `X-Nanoflare-Customer-ID` when creating
a customer script or its asset session. That identity is immutable. Resource
grants must name the same customer; tags are not authorization. Ordinary
dispatcher uploads reject the customer identity header.

### 2. Upload a customer Worker

```sh
cat > customer.js <<'JS'
export default {
  fetch() {
    return new Response('Hello from a Nanoflare customer Worker!');
  },
};
JS
cat > customer-metadata.json <<'JSON'
{"main_module":"customer.js","compatibility_date":"2025-12-10","bindings":[]}
JSON
curl --fail-with-body -X PUT \
  "$NANOFLARE_ACCOUNT_API/workers/dispatch/namespaces/integration-demo/scripts/hello" \
  -H "Authorization: Bearer $NANOFLARE_TOKEN" \
  -F 'metadata=@customer-metadata.json;type=application/json' \
  -F 'customer.js=@customer.js;type=application/javascript+module'
```

The customer Worker has no public hostname or direct asset ingress. A namespace
script cannot bind another dispatch namespace.

### 3. Upload the dispatcher

```sh
cat > dispatcher.js <<'JS'
export default {
  async fetch(request, env) {
    // Fixed trusted demo target. Add your platform's authentication and target
    // authorization before allowing callers to select customer scripts.
    return env.DISPATCHER.get('hello', { props: {} }, {}).fetch(request);
  },
};
JS
cat > dispatcher-metadata.json <<'JSON'
{"main_module":"dispatcher.js","compatibility_date":"2025-12-10","bindings":[{"name":"DISPATCHER","type":"dispatch_namespace","namespace":"integration-demo"}]}
JSON
curl --fail-with-body -X PUT \
  "$NANOFLARE_ACCOUNT_API/workers/scripts/integration-dispatcher" \
  -H "Authorization: Bearer $NANOFLARE_TOKEN" \
  -F 'metadata=@dispatcher-metadata.json;type=application/json' \
  -F 'dispatcher.js=@dispatcher.js;type=application/javascript+module' \
  -o dispatcher-upload.json
export NANOFLARE_DISPATCHER_ID="$(jq -er '.result.nanoflare_worker_id' dispatcher-upload.json)"
```

The response contains the immutable native Worker ID for administration.
An existing native Worker with the same display name is not adopted.

### 4. Configure ingress and call it

Have your operator prepare DNS and TLS for `dispatch.example.com` and route it
through Nanoflare's Traefik ingress. Then assign that hostname through the native
API (replace the example hostname with your own):

```sh
curl --fail-with-body -X PATCH \
  "$NANOFLARE_ORIGIN/v1/organizations/$NANOFLARE_ORG_ID/workers/$NANOFLARE_DISPATCHER_ID" \
  -H "Authorization: Bearer $NANOFLARE_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"hostname":"dispatch.example.com"}'
curl --fail-with-body 'https://dispatch.example.com/'
```

After routing refreshes, expect `Hello from a Nanoflare customer Worker!`.
Native Worker authentication settings also apply; if your operator requires
login, use an authenticated browser/session. A successful upload does not itself
configure DNS/TLS or prove runtime readiness. Verify an actual request.

### 5. Update, retry and clean up

Edit `customer.js` and repeat its multipart PUT to publish a complete new
revision. Full uploads replace configuration unless preservation is explicitly
requested; see `keep_bindings`, inheritance and `keep_assets` in the reference.
For retryable publication, add a unique `Idempotency-Key` and reuse it only for
the identical request. A completed retry cannot undo a later native rollback.
Full upload rejects `If-Match`; supported settings/content/secret/tag mutations
have separate ETag preconditions. Native deployment traffic APIs support rollback.

Delete the ordinary dispatcher through the native API first, then its private
customer script and the empty namespace:

```sh
curl --fail-with-body -X DELETE \
  "$NANOFLARE_ORIGIN/v1/organizations/$NANOFLARE_ORG_ID/workers/$NANOFLARE_DISPATCHER_ID" \
  -H "Authorization: Bearer $NANOFLARE_TOKEN"
curl --fail-with-body -X DELETE \
  "$NANOFLARE_ACCOUNT_API/workers/dispatch/namespaces/integration-demo/scripts/hello" \
  -H "Authorization: Bearer $NANOFLARE_TOKEN"
curl --fail-with-body -X DELETE \
  "$NANOFLARE_ACCOUNT_API/workers/dispatch/namespaces/integration-demo" \
  -H "Authorization: Bearer $NANOFLARE_TOKEN"
```

## Resources, assets and client libraries

Provision resource IDs through Nanoflare's native API/tooling and configure
namespace/customer grants before binding them. D1 accepts `database_id` and the
legacy `id` alias; both must agree if supplied. R2 resolves a native bucket name.
A service target must already exist and pass native ownership/grant checks.

Namespace assets use three stages: create a manifest session, upload requested
base64 hash buckets using the delegated JWT, then attach the returned completion
JWT as `assets.jwt` in script metadata. Implicit asset serving is supported;
ordinary dispatcher asset sessions are not. See the
[asset protocol and bounds](../reference/cloudflare-management-api.md#upload-and-asset-limits).

| Client               | Locally qualified behavior                                 | Required adaptation / boundary                                                                   |
| -------------------- | ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| Direct HTTP          | Documented routes and JSON-part multipart uploads          | Nanoflare credentials and IDs                                                                    |
| Cloudflare SDK 5.2.0 | Publisher's filename-keyed multipart uploads               | Official example fixture supplies configuration and a private API gateway                        |
| Cloudflare SDK 7.3.0 | Selected management/assets flows                           | Construct JSON multipart explicitly; default bracketed upload/settings serialization is rejected |
| Wrangler 4.54.0      | Selected namespace and plain-module upload/update requests | Direct fixture requests; broad secrets/assets/provisioning remain unqualified                    |

The [client fixtures](../../scripts/wfp-client-flows/README.md) record exact
versions and request limitations. The [official example workflow](../../scripts/wfp-examples/README.md)
runs unchanged, pinned Publisher and Internal Sites bundles with local
configuration, persistent storage and identity/API adapters. It does not qualify
the full upstream deployment commands or Cloudflare Access policy APIs.

## Troubleshooting and support boundary

| Symptom                         | Check                                                                                                                  |
| ------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| 401 / 403                       | Nanoflare credentials, URL organization, scopes and resource/customer grants                                           |
| 404 management route            | Adapter enablement and supported route/method; `/user/tokens/verify` is unsupported                                    |
| 422 mutation/upload             | Profile header, explicit JSON metadata, supported fields/bindings; remove Cloudflare custom CPU/outbound/limit options |
| 409 / 412                       | Conflicting operation, namespace dependencies, changed idempotent request or stale supported mutation precondition     |
| Upload succeeds, hostname fails | Native hostname assignment, DNS/TLS, Traefik refresh, authentication and actual runtime health                         |
| 429 during dispatch             | Shared admission bounds; apply backpressure and bounded retries                                                        |

The [runtime reference](../reference/workers-for-platforms-runtime.md) defines
resource/lifecycle restrictions. The current profile has no real CPU interruption;
an infinite loop or process failure can affect peers. Cache API, WebSockets,
customer raw TCP, schedules and arbitrary cross-Worker RPC are outside this
subset. Exact Cloudflare errors, live portability, broad tooling compatibility
and production soak remain unverified; see [remaining work](../plans/workers-for-platforms-remaining-work.md).
