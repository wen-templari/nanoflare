# Migrate to the Cloudflare Vite plugin

New projects use `@cloudflare/vite-plugin` for development, builds, and preview.
Nanoflare remains the deployment target. Existing `@nanoflare/vite-plugin`
projects and their build manifests continue to work with the CLI.

## Update a project

Use a Nanoflare CLI release containing Cloudflare build-output support before
following this guide. Until that release is published, build the CLI from this
repository. Install the official plugin and Wrangler, then remove our plugin:

```sh
npm install -D @cloudflare/vite-plugin wrangler
npm uninstall @nanoflare/vite-plugin
```

Use Node 22.18 or newer and a Vite version supported by the installed plugin.
Replace `nanoflare()` with `cloudflare()` in `vite.config.ts`:

```ts
import { cloudflare } from "@cloudflare/vite-plugin";
import { defineConfig } from "vite";

export default defineConfig({ plugins: [cloudflare()] });
```

Rename the source configuration to `wrangler.jsonc`, or keep its existing name
and pass `cloudflare({ configPath: "nanoflare.json" })`. Set `main` to the Worker
source (for example `src/worker.ts`), remove `files`, `format`, and `vite.entry`,
and remove custom Worker library builds and separate `vite.worker.config.ts`
files. Keep other framework plugins in the Vite configuration.

Use canonical `kv_namespaces`, `d1_databases`, and `r2_buckets` fields. Cloudflare
IDs are separate from Nanoflare IDs: preserve explicit Nanoflare resource IDs in
`nanoflare.resource_ids`. New templates use local placeholder Cloudflare IDs;
replace those with real Cloudflare IDs only if also deploying to Cloudflare.

For apps with frontend assets, keep an `assets` object and its routing settings,
but let the plugin supply the built assets directory. The CLI resolves it from
the generated configuration. Worker-only SSR projects need no client HTML file.

```sh
vite dev
vite build
vite preview
nanoflare check
nanoflare deploy
```

The CLI follows `.wrangler/deploy/config.json`, reads the generated Worker and
client paths, and retains source configuration for bindings, routing, variables,
and Nanoflare extensions. Generated Wrangler metadata is not treated as source
configuration. Always rebuild after configuration changes. Programmatic Worker
configuration overrides are outside this initial integration; keep deployable
settings in the source config.

## Replace custom options

| Previous option                           | Replacement                                                                              |
| ----------------------------------------- | ---------------------------------------------------------------------------------------- |
| `entry`, `vite.entry`                     | Source config `main`                                                                     |
| `configPath`                              | Cloudflare plugin `configPath`                                                           |
| `bindings`, `env`, `devVars`              | Config `vars` and local `.dev.vars`; manage deployed secrets with `nanoflare secret put` |
| `compatibilityDate`, `compatibilityFlags` | Config `compatibility_date`, `compatibility_flags`                                       |
| `d1.bindings`, `r2.bindings`              | Config `d1_databases`, `r2_buckets`                                                      |
| Per-binding persistence paths             | `persistState: { path: ".wrangler/state" }`; existing local data is not migrated         |
| Request `include` callback                | Worker routing and `assets.run_worker_first`                                             |

Local binding state remains separate from deployed Nanoflare storage. Add
`.wrangler/` and `.dev.vars` to `.gitignore`. The official plugin may warn about
Nanoflare extension fields; Nanoflare's CLI still reads them.

## Initial limits and release sequence

This migration supports a single Worker with JavaScript modules and optional
client assets. Auxiliary Workers, named environments, and binary Worker module
uploads are rejected. Cloudflare's local runtime supporting a feature does not
establish Nanoflare production support for it.

Ship CLI support before distributing updated scaffolds. Release Please manages
versions; do not bump package versions or create tags manually. Publish the final
Nanoflare plugin release through its existing release process, then apply the npm
deprecation notice pointing here. Existing package versions remain available.

The npm registry deprecation action requires publisher access and is separate
from this source change. Suggested notice: `Deprecated: use @cloudflare/vite-plugin
with the current Nanoflare CLI. See https://github.com/wen-templari/nanoflare/blob/main/docs/guides/cloudflare-vite-migration.md`.

Official plugin documentation: https://developers.cloudflare.com/workers/vite-plugin/
