# Local Cloudflare client qualification

The Go fixture starts an isolated loopback API, creates temporary Nanoflare
credentials and runs the official `cloudflare@7.3.0` JavaScript SDK against it.
The script explicitly rejects non-fixture network requests. It never uses a
Cloudflare account or credentials from the shell. Runtime execution is covered
separately by `TestPrivateDynamicDispatchEndToEnd`.

Install the pinned SDK outside the repository from a directory outside the
Nanoflare pnpm workspace:

```sh
cd /tmp
npm install --prefix /tmp/nf-wfp-sdk-730 --ignore-scripts --no-audit --no-fund --save-exact cloudflare@7.3.0
```

From the Nanoflare repository, run:

```sh
NANOFLARE_TEST_CF_SDK_DIR=/tmp/nf-wfp-sdk-730/node_modules/cloudflare \
  go test -race ./internal/api -run '^TestCloudflareSDKClientFlows$' -count=1 -v
```

Set `NANOFLARE_TEST_POSTGRES_URL` to a disposable PostgreSQL database to include
the PostgreSQL backend. Each test owns a temporary schema and removes it. Without
that setting only the memory backend runs; without the SDK directory the test
skips, so an ordinary green Go suite does not qualify SDK behavior.

Verified locally: namespace create/list/get/delete; script details and content
read; secret put/get/list and atomic bulk updates; tags put/list/delete; explicitly constructed JSON
settings parts; asset session creation, delegated base64 upload and deployment
with completed assets; script deletion. SDK page iterators are exercised.

The SDK's default script upload emits `metadata[main_module]` and
`files[]` parts while retaining an outer `application/javascript` content type.
Default settings serialization is accepted for compatibility date, compatibility
flags and tags. Empty SDK arrays emit no fields and cannot express clearing; use
an explicit JSON settings part for empty lists or binding arrays. Default uploads
now accept main module, compatibility date/flags, tags,
keep-bindings, keep-assets and the asset completion token. The adapter recognizes
the initial MIME boundary for the SDK's parameter-free application/javascript
outer type and validates all parts. Asset routing configuration supports HTML/not-found handling and boolean or
route-list worker-first values. Binding object arrays still require explicit
JSON metadata. The test
proves this limitation with two different inherit-binding arrays that serialize
to identical fields. It rejects both instead of guessing element ownership.
Uploads with binding arrays use a JSON File as the SDK metadata argument and
let the SDK construct multipart framing; no request-body or content-type override
is needed. Settings use an explicit JSON File when required. The generated SDK
TypeScript metadata type describes an object, so a typed caller needs a narrow
cast for this runtime-qualified File adaptation.
Those unsupported metadata shapes require caller adaptation; this does not
establish drop-in TypeScript SDK,
Wrangler or live Cloudflare compatibility. No assertion is made about how the
Cloudflare service handles these SDK requests without an entitled live account.

## Wrangler 4.54.0

`TestWranglerLocalRequestQualification` invokes the real installed CLI in an
isolated temporary project. Point `NANOFLARE_TEST_WRANGLER_PATH` to the absolute
`wrangler-dist/cli.js` path inside a `wrangler@4.54.0` package, then run:

```sh
go test -race ./internal/api -run '^TestWranglerLocalRequestQualification$' -count=1 -v
```

As with the SDK fixture, set `NANOFLARE_TEST_POSTGRES_URL` for PostgreSQL coverage.
The test explicitly supplies fixture credentials and a loopback API base URL.
Its process environment is isolated; the test-only preload sends configuration
to the disposable directory and blocks non-loopback Node socket connections.
It leaves shell home variables and the user's Wrangler login unchanged.

Verified: namespace list/get/create/delete, a plain module upload, and a second
upload with updated source and a plain-text variable. The Go fixture inspects the
actual native snapshots. This is management-request qualification; the fixture
does not execute Wrangler-uploaded code in workerd.

Mutations run directly against the fixture API without a consent-injecting proxy.
The fixture administrator enables shared trusted dispatch and the compatibility
adapter. Wrangler's plain JSON metadata fields and `excludeScript=true`
projection are accepted by the adapter, with exact JSON validation intact.

Wrangler also probes ordinary `/workers/scripts/{name}/settings` and, on errors,
`/user/tokens/verify`. The ordinary settings GET route is now implemented for
uploaded ordinary Workers; an absent Worker still returns 404. Token verification
remains unsupported and returns 404.
No fake account/token state is returned. Secrets/assets, preservation of existing
configuration, forced script deletion, namespace rename, other Wrangler versions
and broader CLI deployment workflows remain unqualified. The test uses the native
API for script cleanup before exercising Wrangler's empty-namespace deletion.

## Upstream example acceptance

The [separate example runner](../wfp-examples/README.md) builds the pinned
Cloudflare Worker Publisher and Internal Sites sources unchanged. The Publisher
locks Cloudflare SDK 5.2.0, whose filename-keyed multipart `files` encoding is
now supported. SDK 7 scalar metadata support is described above; object-array metadata
remains outside the default serializer contract. The example runner exercises actual deployment, dispatch,
identity and static assets against persistent local storage.

See the maintained [integration guide](../../docs/guides/workers-for-platforms.md)
and [API reference](../../docs/reference/cloudflare-management-api.md).

## Combined local baseline

Use the native workerd executable from the Dockerfile-pinned package
`workerd@1.20260911.1`, rather than an npm launcher. Place its platform-specific
`bin` directory first in PATH. A launcher can leave runtime children alive when
the test cancels it, preventing suite teardown.

```sh
PATH=/tmp/nf-workerd-20260911/node_modules/@cloudflare/workerd-darwin-arm64/bin:$PATH \
NANOFLARE_TEST_CF_SDK_DIR=/tmp/nf-wfp-sdk-730/node_modules/cloudflare \
NANOFLARE_TEST_WRANGLER_PATH=/tmp/nf-wfp-wrangler-4540/node_modules/wrangler/wrangler-dist/cli.js \
  go test ./internal/cfcompat ./internal/api -count=1 -timeout=120s
```

Adjust the native platform package for the host OS/architecture. On 2026-10-23,
this combined baseline passed on macOS arm64 with native workerd 2026-09-11.
PostgreSQL tests skipped because no disposable database was configured. This
qualifies the existing supported subset, not the proposed SDK/Wrangler expansion.

### SDK binding-array ambiguity

SDK 7.3.0 serializes both of these arrays to the same ordered form fields:

```js
[
  { name: "A", type: "inherit" },
  { old_name: "SOURCE", name: "B", type: "inherit" },
][({ name: "A", type: "inherit", old_name: "SOURCE" }, { name: "B", type: "inherit" })];
```

Inferring boundaries from `name` would attach SOURCE to the wrong binding for
one of these requests. The pinned fixture uses the actual SDK serializer to
prove the collision. Retain JSON metadata for object arrays until the upstream
wire format provides explicit indices or element boundaries.

## SDK runtime workflow

`TestCloudflareSDKRuntimeWorkflow` runs the pinned SDK against the actual dynamic
runtime manager and workerd. It verifies serving code and secrets, dispatcher
upload with JSON File metadata, source updates, assets and a later secret update
that preserves those assets. It also uploads a binary Wasm module, executes 42,
downloads exact bytes (including a non-UTF-8 custom section), then changes only
the binary through the SDK content API and observes 7. A later secret update
preserves the module and assets. Customer/namespace cleanup uses the SDK; the Go
fixture removes the dispatcher through native administration.

```sh
NANOFLARE_TEST_WORKERD=/tmp/nf-workerd-20260911/node_modules/@cloudflare/workerd-darwin-arm64/bin/workerd \
NANOFLARE_TEST_CF_SDK_DIR=/tmp/nf-wfp-sdk-730/node_modules/cloudflare \
  go test -race ./internal/api -run '^TestCloudflareSDKRuntimeWorkflow$' -count=1 -v -timeout=90s
```

Verified on 2026-10-25 with SDK 7.3.0 and native workerd 2026-09-11 on macOS
arm64 (Go race detector, Node 24.22.1) and Linux arm64 (Node 22.23.1). Linux
uses a disposable network-disabled container. The workflow uses the memory
repository, local object storage and loopback requests. It does not qualify
PostgreSQL, production operations, hosted Cloudflare equivalence or untrusted
execution. Evidence: `results-cloudflare-stages-wasm-sdk*`.

## Wrangler runtime workflow

Set the pinned `NANOFLARE_TEST_WORKERD` and `NANOFLARE_TEST_WRANGLER_PATH`
paths, then run `go test -race ./internal/api -run '^TestWranglerRuntimeWorkflow$'
-count=1 -v -timeout=90s`. The fixture uses the real dynamic runtime and verifies
namespace upload, ordinary dispatcher provisioning, serving, source updates and
assets. Use `--keep-vars` to retain secrets on namespace re-upload.

Wrangler 4.54.0 has no namespace option for secret commands, so namespace secret
updates use the compatible API in this fixture. Customer script cleanup uses native administration; namespace deletion and
ordinary Worker deletion use Wrangler. The fixture also exercises ordinary secret
put/list/update/delete, proving serving-state changes and dispatch preservation.
This qualifies the representative local workflow with explicit namespace
adaptations, not full CLI equivalence. The CLI cleanup requires kv:read to list
legacy asset namespaces; deletion of matching legacy KV namespaces is unsupported.

The Wrangler runtime fixture also uses `no_bundle` with
`find_additional_modules` to deploy an imported Wasm file. It executes 42, changes
only the binary and observes 7, then verifies secret/asset retention. The
compatible API content endpoint verifies exact Wasm bytes and MIME after both
deployments and a later namespace secret update; this is not a Wrangler download
command. Verified on 2026-10-25 with Wrangler 4.54.0 and workerd 2026-09-11 on
macOS arm64 (Go race detector) and Linux arm64. The first Linux fixture lacked
platform binaries; its failure is retained alongside the verified pinned Linux
package integrities. Evidence: `results-cloudflare-stages-wasm-wrangler*`.
PostgreSQL, hosted equivalence, production operations and untrusted execution
remain outside this client qualification.
