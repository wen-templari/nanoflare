# Nanoflare CLI

Private dynamic namespace targets can use
`nanoflare check --runtime-profile private-dynamic` for advisory compatibility
warnings and `privateDynamicProfile` from `@nanoflare/cli/vite` alongside the
Cloudflare Vite plugin. See
[runtime differences and diagnostics](../../docs/guides/private-dynamic-runtime.md).

Install the Nanoflare command-line interface:

```sh
npm install -g @nanoflare/cli
nanoflare --help
```

Or run it without a global install:

```sh
npx @nanoflare/cli --help
```

The package downloads only the native binary matching your operating system and CPU architecture.

## Deployment uploads

`nanoflare deploy` streams worker modules and static assets from disk as raw
multipart file parts. Files are not base64 encoded over this upload connection.
Source projects bundled in memory by esbuild use the same multipart transport.
Deploy the updated server before updating the CLI; older clients can continue
sending native JSON deployment requests.

The default limits are **30 MiB total worker code** (JavaScript and Wasm),
**50 MiB per static asset**, and **250 MiB for the complete encoded request**,
including metadata and multipart framing. Static assets do not count toward the
worker-code limit. HTML or assets embedded into worker JavaScript do count as
code. Multipart metadata is limited to 1 MiB and a deployment can contain up to
10,000 multipart files. Files must not change while they are uploading.

Operators can configure `nanoflared` with `--deployment-worker-max-bytes`,
`--deployment-asset-max-bytes`, and `--deployment-request-max-bytes`. Invalid
combinations, including limits that exceed the internal runner snapshot capacity,
are rejected at startup. Oversized uploads return HTTP 413 with the exceeded
limit. Other API routes retain their existing request limits.

The UI container allows 251 MiB on the deployment route and disables request
buffering there; the API enforces the exact 250 MiB default. If the API request
limit is increased, also adjust `NANOFLARE_DEPLOYMENT_PROXY_MAX_BODY_SIZE` on the
UI container and any external ingress limits. Recreate the UI container to apply
its updated proxy configuration.

The native deployment endpoint accepts either legacy `application/json` or
`multipart/form-data`. Multipart starts with a `metadata` field containing
`DeployInput` JSON. Its `files` and `assets` arrays contain paths and metadata,
with inline `content`, `wasm`, `data`, and asset `object_key` values omitted.
Subsequent parts are named `file-0`, `file-1`, `asset-0`, etc., indexing those
arrays. Worker parts use `application/javascript` (UTF-8) or `application/wasm`;
asset bytes are preserved exactly and their serving content type comes from the
metadata. Each declared file must have exactly one part.

`nanoflare platform bundle` and `nanoflare platform deploy` remain explicit JSON
export/import commands; binary content in those JSON files still uses base64.
The Cloudflare-compatible `/client/v4/` upload profile retains its separate limits
and asset-upload protocol.
