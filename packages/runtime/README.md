# `@nanoflare/runtime`

Typed runtime adapters injected into every Nanoflare worker.

`src/bindings.ts` is the source of truth. `pnpm build` emits deterministic
JavaScript into `internal/runtimeassets/assets`, where Go embeds it in the
Nanoflare binary. Commit the generated JavaScript with source changes so Go
builds and tests do not require Node.js.

Run `pnpm typecheck` for a source-only check and `pnpm build` to refresh the
embedded asset.
