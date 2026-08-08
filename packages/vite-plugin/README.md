# `@nanoflare/vite-plugin`

Run a single React SSR or API Worker in a local Workers runtime during `vite dev`
and build its deploy artifact with `vite build`. Vite continues to serve browser
modules, static files, and client HMR; matching document and `/api/*` requests
run in Miniflare.

`nanoflare()` reads `nanoflare.json` in the Vite root by default. Its `main`
field names Worker source code, while the plugin writes a deploy manifest after
building. The project file supplies the Worker entry, `compatibility_date`,
`vars`, database, and object-storage binding names, so a minimal configuration
is:

```ts
// vite.config.ts
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { nanoflare } from "@nanoflare/vite-plugin";

export default defineConfig({
  plugins: [react(), nanoflare({ env: ["SESSION_SECRET"] })],
});
```

```json
{
  "name": "my-worker",
  "main": "src/worker.ts",
  "compatibility_date": "2026-08-08"
}
```

`vite build` writes the Worker, any code-split chunks, and a deploy manifest to
`dist/my-worker/`. Deploy the result with the usual command:

```sh
vite build && nanoflare deploy
```

`nanoflare deploy` detects the generated manifest automatically. The source
project config does not need a `files` list or Vite `build.lib` configuration.
Projects that retain a deployment artifact in `main` can instead configure the
Vite source entry explicitly:

```json
{ "vite": { "entry": "src/worker.ts" } }
```

Explicit plugin options override project-file values; use `configPath` to load
a different project file, or `configPath: false` with `entry` to opt out of
project-file loading. D1/R2 persistence remains a Vite-only option:

```ts
nanoflare({ d1: { persist: ".nanoflare/d1" } });
```

`src/worker.ts` must have a Worker module default export with a `fetch` method.
For example, it can use `renderToReadableStream()` and return its result in a
`Response`. Values in `.dev.vars` become string bindings; explicit `bindings`
override those values. Only environment variable names listed in `env` are
exposed. The runtime uses compatibility date `2025-12-10` by default; set
`compatibilityDate` to match a different deployment target.

On TypeScript/JavaScript source changes, the plugin rebuilds and replaces the
local Worker before the next SSR/API request. Server-side HMR, multiple Workers,
Durable Objects, browser/client builds, and remote Nanoflare bindings are
deliberately outside this first version. D1 and R2 use Miniflare's local
emulation; they do not connect to Nanoflare's deployed database or object
storage services.
