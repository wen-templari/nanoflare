# vite-react-spa

`vite-react-spa` is a React single-page app built with Vite, plus a separate
Worker entrypoint that provides API routes to the page.

It mirrors the structure of Cloudflare's React SPA with API tutorial, but uses
Nanoflare deployment config and bindings.

Reference tutorial: [Cloudflare Workers Vite Plugin tutorial](https://developers.cloudflare.com/workers/vite-plugin/tutorial/index.md)

## What It Demonstrates

- a Vite-powered React SPA under `src/`
- a separate Worker entrypoint under `worker/`
- API routes handled by the Worker at `/api/*`
- static frontend assets served from `dist/client` through `ASSETS`
- `run_worker_first` so API requests reach the Worker before SPA asset handling

## Setup

From this directory:

```sh
npm install
npm run build
nanoflare create
nanoflare deploy
```

## Routes To Try

- `/` serves the React SPA
- `GET /api/hello` returns a message payload the SPA renders
- `GET /api/time` returns the Worker time and request info
