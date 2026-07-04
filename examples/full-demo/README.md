# full-demo

`full-demo` is the end-to-end Nanoflare example for the full platform surface:

- a Worker running in the shared `workerd` pool
- forwarded auth headers on protected routes
- explicit Cloudflare-style KV namespace bindings
- attached static assets through `ASSETS`
- object storage through `OBJECTS`

## What It Demonstrates

This example combines the main Nanoflare platform capabilities in one app:

- `VISITS_KV` stores a visit counter.
- `ASSETS` serves the static frontend from `public/`.
- `OBJECTS` stores and retrieves `uploads/latest.txt`.
- protected routes show how Nanoflare forwards authenticated user headers into the Worker.

The source lives in [src/worker.ts](src/worker.ts) and [src/router.ts](src/router.ts).
The compiled deploy artifacts are written to `dist/` and deployed from there.

## Setup

Start Nanoflare locally, then from this directory:

```sh
npm install
npm run build
nanoflare create
nanoflare kv namespace create visits
```

Update [nanoflare.json](nanoflare.json) so `kv_namespaces[0].id` matches the namespace id returned by the create command, then deploy:

```sh
nanoflare deploy
```

If your local API is not at `http://127.0.0.1:8080`, either update `api_url` in `nanoflare.json` or pass `--api-url`.
`nanoflare deploy` uploads the built files from `dist/`, so rerun `npm run build` after changing the TypeScript sources.

## Routes To Try

- `/` serves the attached static site.
- `/api/visits` increments and returns the counter stored in `VISITS_KV`.
- `PUT /api/files/latest.txt` uploads a file to `OBJECTS`.
- `GET /api/files/latest.txt` reads the uploaded file back from `OBJECTS`.
- `/preview/logo.svg` fetches an attached asset through `env.ASSETS.fetch(...)`.
- `/preview/auth` returns the forwarded auth headers on a protected route.

## Config Notes

This example uses:

- `auth.protected_routes` for `/api/files/*` and `/preview/*`
- `assets.run_worker_first` so dynamic API and preview routes hit the Worker before static asset resolution
- `kv_namespaces` with an explicit binding name, `VISITS_KV`
- a local `file:` dependency on `@nanoflare/workers-types` for Worker env typing while the package is still unpublished

The frontend in `public/` expects the Worker APIs above and is mainly there to make the asset and KV pieces visible immediately after deploy.
