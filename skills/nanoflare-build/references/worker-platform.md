# Nanoflare Worker platform reference

## Core project configuration

`nanoflare.json` requires `name`, `main`, and `compatibility_date`. New projects
should reference:

```json
{
  "$schema": "https://raw.githubusercontent.com/wen-templari/nanoflare/main/schemas/nanoflare.json",
  "name": "my-worker",
  "main": "src/worker.ts",
  "format": "modules",
  "compatibility_date": "YYYY-MM-DD"
}
```

`files` identifies uploaded files for non-Vite builds. A Vite build writes a
deploy manifest that `nanoflare deploy` detects automatically. `vars` contains
JSON-serializable Worker bindings. `compatibility_flags` and `triggers.crons`
are passed through configuration. New module Workers export a default object
with `fetch(request, env)`; scheduled Workers can also provide `scheduled`.

## Bindings

| Need                   | Config                                                             | Worker binding                                          |
| ---------------------- | ------------------------------------------------------------------ | ------------------------------------------------------- |
| Secrets                | `secrets.required`                                                 | `string` after `nanoflare secret put`                   |
| KV                     | `kv_namespaces: [{ binding }]` or an explicit `id`                 | `env.NAME` with `get`, `put`, `delete`                  |
| SQLite                 | `db: [{ binding }]` or an explicit `database_id`                   | D1-style `env.NAME` with `prepare`, `exec`, sessions    |
| Object storage         | `object_storage_buckets: [{ binding }]` or an explicit `bucket_id` | R2-style `env.NAME` with `put`, `get`, `head`, `delete` |
| Static assets          | `assets`                                                           | `env.ASSETS.fetch(request)` by default                  |
| Private Worker service | `services: [{ binding, service }]`                                 | Fetcher/RPC-capable `env.NAME`                          |
| Route authentication   | `auth.protected_routes`                                            | Verified auth context for matching routes               |

Asset settings include `directory`, optional `binding`, HTML handling, not-found
handling, and `run_worker_first`. Service targets must be deployed in the same
organization; deploy the target before the caller. Types can resolve service RPC
with `nanoflare types -c caller/nanoflare.json -c target/nanoflare.json`.
When the target imports `cloudflare:workers` through a local declaration file,
include that target declaration path in the caller's `tsconfig.json`, for
example `"../identity-worker/src/**/*.d.ts"`; the target's emitted declaration
retains that module import.

## TypeScript checks

Treat `nanoflare types --check` and TypeScript compilation as required scaffold
validation, not optional documentation. Generated handler types define
`NanoflareExecutionContext`; avoid explicitly annotating scheduled handlers
with `ExecutionContext`. For untyped Web `Request` parsing, validate the
`unknown` value returned by `await request.json()` before access. When validating
optional numeric input, assign a narrowed `number` after the guard before
passing it to database bindings or object construction.

## CLI sequence

```sh
npm install -g @nanoflare/cli
nanoflare auth login --web
nanoflare auth orgs
nanoflare auth use-org org_123

nanoflare secret put API_KEY "$API_KEY"

nanoflare types
npm run build
nanoflare check --types
nanoflare deploy
nanoflare deployment output --level error
```

ID-less resource bindings are provisioned during deploy with a deterministic
Worker-and-binding name. Pass `--provision=false` to require explicit IDs.
`nanoflare check` validates project configuration, bindings, Worker files, and
assets without changing platform state; add `--types` to require a current
generated declaration. Use `nanoflare db execute <database-id> --command` or
`--binding DB` for one statement, or `nanoflare db migrations create` and
`apply --binding DB` for multi-statement schema changes.
For non-interactive deploys set `NANOFLARED_URL`, `NANOFLARE_TOKEN`, and
`NANOFLARE_ORG_ID`.

## Vite plugin

`@nanoflare/vite-plugin` runs one API/SSR Worker locally in Miniflare while Vite
continues serving browser modules and HMR. It reads `nanoflare.json` by default;
the source entry is `main` or `vite.entry`. The plugin can read selected host
environment names, `.dev.vars`, explicit bindings, and local D1/R2 persistence.
Explicit bindings override `.dev.vars`.

Use it for a single Worker’s API/SSR local path. Do not use it to emulate remote
Nanoflare resources, multiple Workers, Durable Objects, or browser/client
builds; D1/R2 are local Miniflare emulations.
