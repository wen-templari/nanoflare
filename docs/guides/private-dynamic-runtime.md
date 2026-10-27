# Private dynamic Worker runtime

Private namespace scripts use a restricted, broker-backed runtime. Ordinary
Workers and Cloudflare's local runtime have a different capability profile.

| API                                                 | Private dynamic behavior                                                 |
| --------------------------------------------------- | ------------------------------------------------------------------------ |
| `fetch(url, { cf: ... })`                           | Rejected; omit Cloudflare-specific fetch/cache options                   |
| `caches.default`, `caches.open`, `Cache` operations | Unavailable; use an explicit storage binding for application storage     |
| Outbound WebSockets and URLs containing credentials | Rejected                                                                 |
| Fetch with an AbortSignal                           | Supported on outbound, service and asset fetch, including Request inputs |

Response `Cache-Control` headers control HTTP caching by clients and configured
intermediaries. They do not provide a Cache API backend or cache outbound requests.
Cancellation cannot undo effects that already completed.

## Diagnose an execution failure

Uncaught private dynamic fetch errors return a generic 500 with an
`X-Nanoflare-Error-Id` header and a reference in the body. Details go to
authenticated deployment output with Worker/revision attribution and existing
telemetry redaction, sampling and collection limits.

```sh
nanoflare deployment output <worker-id> --level error --search <reference>
nanoflare deployment output <worker-id> --deployment <deployment-id> --level error
```

Rejected `ctx.waitUntil()` tasks and response-stream failures are collected too.
Stream failures after headers cannot change the initial status; inspect output
even when it was 200. Initialization errors are attributed to the attempted
revision. Logging is best effort and never replaces the application's failure.

## Check before publishing

```sh
npm run build
nanoflare check --types --runtime-profile private-dynamic
```

Advisory warnings recognize direct global Cache API access and literal `cf`
options on global fetch. Comments and strings are skipped. Aliases, computed
properties, spreads and dynamically constructed options can evade the checks;
passing does not prove full compatibility. Signals are supported and do not
produce warnings. The default `standard` profile keeps ordinary checks unchanged.

## Reproduce restrictions locally

With a CLI package release containing the profile helper, add it alongside the
official Cloudflare Vite plugin. Until release, use the workspace package.

```ts
import { cloudflare } from "@cloudflare/vite-plugin";
import { privateDynamicProfile } from "@nanoflare/cli/vite";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [privateDynamicProfile({ entry: "src/worker.ts" }), cloudflare()],
});
```

The selected entry imports production's capability checks, including in preview
builds. Custom runners can import `installPrivateDynamicProfile` from
`@nanoflare/cli/runtime-profile` in a bootstrap module and call it before importing
the Worker. The deprecated Nanoflare plugin supports
`nanoflare({ runtimeProfile: "private-dynamic" })` in local development.

Allowed fetches delegate to the local runtime. These adapters reproduce API
restrictions, not production network policy, resource authorization, quotas or
the full RPC topology. Use pinned native end-to-end tests for those.

## Operator implementation

Nanoflare enables `enable_abortsignal_rpc` internally on pinned workerd
2026-09-11. Parent loaders explicitly permit that experimental transport;
application configuration needs no extra flag. Adapters preserve signals through
redirects and broker HTTP requests and preserve the caller's abort reason when
an RPC response stream disconnects.

Regenerate shared checks with `node scripts/update-runtime-profile.mjs` and
verify generated files with `node scripts/update-runtime-profile.mjs --check`.
