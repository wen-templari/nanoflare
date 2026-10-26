# Shared Cloudflare-compatible Worker configuration

`nanoflare.json` and `wrangler.json` use the same source configuration fields,
parser, validation, and resource resolution. Both names also support `.jsonc`
(comments and trailing commas) and `.toml`. Discovery checks Nanoflare JSON,
JSONC, TOML, then Wrangler JSON, JSONC, TOML. Run from the project directory:

```sh
nanoflare check
nanoflare types
nanoflare deploy
```

New projects use the official Cloudflare Vite plugin and `wrangler.jsonc`.
See the [migration guide](cloudflare-vite-migration.md). The CLI discovers its
build output through `.wrangler/deploy/config.json`. The legacy Nanoflare Vite
plugin remains supported and uses the same source discovery order and D1/R2 fields. Deployment uses an existing Vite build manifest when
present, or bundles `main` (including TypeScript and imports) in memory. Source
configs need no `files` list or `format`. Existing manifests remain supported.

```jsonc
{
  "$schema": "node_modules/wrangler/config-schema.json",
  "name": "portable-worker",
  "main": "src/index.ts",
  "compatibility_date": "2026-08-01",
  "vars": { "GREETING": "Hello" },
  "kv_namespaces": [{ "binding": "CACHE", "id": "your-cloudflare-kv-id" }],
  "d1_databases": [
    { "binding": "DB", "database_name": "app", "database_id": "your-cloudflare-d1-id" },
  ],
  "r2_buckets": [{ "binding": "UPLOADS", "bucket_name": "uploads" }],
  "dispatch_namespaces": [{ "binding": "DISPATCHER", "namespace": "customers" }],
}
```

Supported behavior includes compatibility date and flags, JSON `vars`, KV, D1,
R2, service bindings by Worker name, dispatch bindings by namespace name, cron
triggers, and Workers assets alongside a Worker entrypoint.
`nanoflare types -c path/to/wrangler.toml` accepts explicit type-generation
configs. Dispatch types use Nanoflare's existing dispatch runtime interface.

Cloudflare resource IDs remain in portable source files for Cloudflare. Nanoflare
resolves independent KV, D1, and R2 resources using project-and-binding names
(for example `portable-worker-cache`). Deployment provisions missing resources;
`--provision=false` requires them to exist. Data is not copied from Cloudflare.
Dispatch namespaces must already exist: create one with
`nanoflare platform namespace create customers`. Deployment resolves that name
in the current Nanoflare organization. It does not create a namespace implicitly.

## Nanoflare extensions and legacy configs

Use a `nanoflare` section for Nanoflare-specific configuration. With this section,
use the Nanoflare schema (`schemas/nanoflare.json`) for editor validation:

```jsonc
{
  "name": "portable-worker",
  "main": "src/index.ts",
  "compatibility_date": "2026-08-01",
  "d1_databases": [{ "binding": "DB", "database_name": "app" }],
  "nanoflare": {
    "worker_id": "existing-nanoflare-worker-id",
    "dns": { "profile": "private" },
    "auth": { "protected_routes": ["/admin/*"] },
    "secrets": { "required": ["API_KEY"] },
    "resource_ids": { "DB": "existing-nanoflare-database-id" },
  },
}
```

`resource_ids` maps binding names to explicit Nanoflare resource IDs, including
KV, D1, R2, dispatch namespaces, and service Worker targets. Secret values are managed separately with
`nanoflare secret put`.

Legacy `db`, `object_storage_buckets`, singular `object_storage_bucket`,
`dispatch_namespaces[].namespace_id`, and top-level `worker_id`, `dns`, `auth`,
and `secrets` remain accepted under either config name. Do not combine a
canonical field with its alias or a top-level option with its extension form.
Legacy configs with a `files` list or legacy storage fields retain their KV ID
semantics; new portable source configs use `nanoflare.resource_ids` for explicit
Nanoflare IDs. Existing `entrypoint` migration remains supported; ordinary
portable configs are never rewritten. Build manifests and server deployment
payloads retain their internal representation independently of this source schema.

## Current runtime limits

A shared config format does not implement every Cloudflare feature. Unsupported
settings produce errors, including dispatch `outbound` and remote dispatch,
custom build commands, routing, Durable Objects, queues, Pages, and asset-only
deployments. Named environment definitions can remain in the file, but only
top-level configuration is selected; `CLOUDFLARE_ENV` must be unset.

Cloudflare account and preview metadata (`account_id`, `workers_dev`,
`preview_urls`) do not control Nanoflare routing. D1 migration metadata is accepted;
deployment does not execute migrations. Runtime API and compatibility-flag
support depend on the Nanoflare server. Wrangler CLI operation against Nanoflare
requires the separate API compatibility work.
