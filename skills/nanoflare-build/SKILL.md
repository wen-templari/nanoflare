---
name: nanoflare-build
description: Scaffold, configure, and ship Nanoflare Worker applications. Use this skill whenever the user asks to build a Worker or app on Nanoflare, configure nanoflare.json, add Worker bindings, use the Nanoflare CLI or Vite plugin, generate Worker types, create databases/KV/buckets/secrets, or deploy a Nanoflare app. Generate the project artifacts and commands, not merely generic Workers advice.
---

# Build on Nanoflare

Use this skill to produce a deployable TypeScript Worker project that follows
Nanoflare's configuration, binding, and CLI model. Read
[references/worker-platform.md](references/worker-platform.md) before choosing
bindings or local development tooling. In the Nanoflare repository, treat
`schemas/nanoflare.json`, `packages/vite-plugin/README.md`, templates, and
examples as the source of truth for current behavior.

## Workflow

1. Identify the Worker shape: API, static pages, SPA, SSR, scheduled task, or
   service-to-service application. Identify every data binding, secret, public
   route, protected route, and local-development requirement.
2. Scaffold an ES-module Worker by default. Generate `nanoflare.json`, the
   Worker entrypoint, TypeScript configuration, package scripts, and
   `worker-configuration.d.ts`. Use service-worker format only when the user
   explicitly requires legacy event-listener syntax.
3. Use `npm create nanoflare@latest` or `nanoflare init` for a new baseline when
   it matches the requested template. For a custom project, generate the same
   essential structure directly. Pick the smallest template that satisfies the
   request: starter, bindings, pages, SPA, SSR, or API.
4. Prefer binding-only KV namespaces, SQLite databases, and object-storage
   buckets in `nanoflare.json`; `nanoflare deploy` resolves or provisions the
   Worker-and-binding-named resource without rewriting the file. Use explicit
   IDs only when binding a shared, pre-existing resource, and never fabricate
   IDs. Declare secret names under
   `secrets.required`, then configure values with `nanoflare secret put` or the
   deployment secret mechanism.
5. Generate types using `nanoflare types`; put `nanoflare types --check` in the
   validation or CI script. When a Worker calls another local Worker through a
   service binding, pass the caller and target configs with repeated `-c` flags.
   If the target uses a local ambient declaration (for example,
   `cloudflare:workers`), include the target's `src/**/*.d.ts` files in the
   caller's TypeScript configuration so emitted RPC declarations resolve during
   the caller type check.
6. Build TypeScript/React before `nanoflare deploy`. Prefer the Vite plugin when
   the project needs its supported local API/SSR development flow. Explain that
   its local D1/R2 emulation is not a connection to remote Nanoflare resources.
7. Finish with a concrete command sequence for authentication, organization
   selection, optional shared-resource creation, type generation, build,
   `nanoflare check`, deploy, and log inspection. `nanoflare check` validates
   the deployable artifact locally without creating resources or uploading it;
   use `nanoflare check --types` when the project commits generated Worker
   types. For CI, use `NANOFLARE_TOKEN`, `NANOFLARE_ORG_ID`, and
   `NANOFLARED_URL` rather than an interactive auth store.

## Output requirements

Start with the selected Worker architecture and bindings. Return complete,
internally consistent artifacts rather than isolated snippets: `nanoflare.json`
must agree with code, types, package scripts, and commands. Use the canonical
schema URL and an explicit current `compatibility_date` when scaffolding a new
project.

Run the project's declared type check before handing it off. Make request-body
validation narrow optional values into local variables before using them; do not
rely on `Number.isInteger()` to narrow an optional property. For a raw Web
`Request`, call `request.json()` without a type argument and validate/cast the
result; Hono's request helper may provide its own typed parsing API. Let
`NanoflareWorkerHandler` infer the scheduled context, or use the generated
`NanoflareExecutionContext` type rather than the platform `ExecutionContext`.

Keep secret values out of `nanoflare.json`, committed dotenv files, and source.
Do not add placeholder IDs for managed bindings; omit the ID. Mark an explicit
ID as a shared-resource input only when the project intentionally pins that
binding. Do not claim support for Durable Objects, multiple local Workers,
browser/client builds through the Vite Worker plugin, or remote Nanoflare
bindings in Vite development.
