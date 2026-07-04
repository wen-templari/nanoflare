# full-demo

`full-demo` is the end-to-end Nanoflare example for the full platform surface:

- a Worker running in the shared `workerd` pool
- forwarded auth headers on protected routes
- explicit Cloudflare-style KV namespace bindings
- attached static assets through `ASSETS`
- object storage through `OBJECTS`

## What It Demonstrates

This example combines the main Nanoflare platform capabilities in one app:

- `VISITS_KV` stores a visit counter and file metadata.
- `ASSETS` serves the static frontend from `public/`.
- `OBJECTS` stores and retrieves `uploads/latest.txt`.
- protected routes show how Nanoflare forwards authenticated user headers into the Worker.

The Worker entrypoint is [worker.js](worker.js), with route handling in [router.js](router.js).

## Setup

Start Nanoflare locally, then from this directory:

```sh
nanoflare create
nanoflare kv namespace create visits
```

Update [nanoflare.json](nanoflare.json) so `kv_namespaces[0].id` matches the namespace id returned by the create command, then deploy:

```sh
nanoflare deploy
```

If your local API is not at `http://127.0.0.1:8080`, either update `api_url` in `nanoflare.json` or pass `--api-url`.

## Routes To Try

- `/` serves the attached static site.
- `/api/visits` increments and returns the counter stored in `VISITS_KV`.
- `PUT /api/files/latest.txt` uploads a file to `OBJECTS` and stores metadata in `VISITS_KV`.
- `GET /api/files/latest.txt` reads the uploaded file back from `OBJECTS`.
- `/preview/logo.svg` fetches an attached asset through `env.ASSETS.fetch(...)`.
- `/preview/auth` returns the forwarded auth headers on a protected route.

## Config Notes

This example uses:

- `auth.protected_routes` for `/api/files/*` and `/preview/*`
- `assets.run_worker_first` so dynamic API and preview routes hit the Worker before static asset resolution
- `kv_namespaces` with an explicit binding name, `VISITS_KV`

The frontend in `public/` expects the Worker APIs above and is mainly there to make the asset and KV pieces visible immediately after deploy.
