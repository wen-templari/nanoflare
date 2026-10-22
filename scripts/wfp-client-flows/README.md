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

The SDK's default script upload currently emits `metadata[main_module]` and
`files[]` parts while retaining an outer `application/javascript` content type.
Default settings serialization similarly emits bracketed fields. These requests
are rejected by the adapter's documented JSON-part wire contract. The test
records that limitation and demonstrates manual multipart construction through
SDK request options for script upload and an explicit JSON File for settings.
This requires caller adaptation; it does not establish drop-in TypeScript SDK,
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

Direct mutation fails because Wrangler does not supply Nanoflare's required
`X-Nanoflare-Execution-Profile` header. The successful path uses a test-only local
proxy that adds this acknowledgment; this adaptation is not implemented as a
production proxy. Wrangler's plain JSON metadata fields and `excludeScript=true`
projection are now accepted by the adapter, with exact JSON validation intact.

Wrangler also probes ordinary `/workers/scripts/{name}/settings` and, on errors,
`/user/tokens/verify`. Those unsupported endpoints remain 404 in the fixture.
No fake account/token state is returned. Secrets/assets, preservation of existing
configuration, forced script deletion, namespace rename, other Wrangler versions
and unmodified direct CLI deployment remain unqualified. The test uses the native
API for script cleanup before exercising Wrangler's empty-namespace deletion.
