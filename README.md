# nanoflare

`nanoflare` is a lightweight self-hosted runtime for trusted business apps generated
with AI. The design keeps the custom control plane small and uses existing
infrastructure directly:

- Traefik for TLS, host routing, and ForwardAuth.
- `workerd` for a shared pool of Worker isolates.
- PostgreSQL for nanoflare metadata and app-scoped KV.
- MinIO for static assets and application objects.
- Rootless Podman for the shared pool sandbox boundary.

The current repository is the first runnable integration slice of `nanoflared`. It provides:

- App registration and immutable deployment records.
- A combined `workerd` Cap'n Proto configuration with one isolate and socket per
  active app.
- A Traefik HTTP discovery endpoint with host routers and ForwardAuth.
- An optional atomic-file fallback for a host-run Traefik process.
- Managed `workerd` pool generations with readiness checks and blue-green traffic
  replacement.
- App-scoped runtime KV capabilities with PostgreSQL persistence when configured.
- Explicit Cloudflare-style KV namespace bindings with native `env.BINDING`
  `get`, `put`, and `delete` operations.
- A Cloudflare-style static assets binding for deployed assets through
  `env.ASSETS.fetch(...)`.
- A Cloudflare R2-style `env.OBJECTS` binding with core `put`, `get`, `head`, and `delete` operations backed by MinIO.
- A starter Worker and a TypeScript types package for Worker bindings.

Example workers live under `examples/`:

- [examples/simple-kv](examples/simple-kv/) shows a hello-world counter backed by an explicit KV binding.
- [examples/gallery-app](examples/gallery-app/) serves a static UI, uploads images to object storage, and stores gallery metadata in KV.
- [examples/protected-app](examples/protected-app/) protects `/api/auth/*` routes and returns resolved auth information from the Worker.
- [examples/vite-react-spa](examples/vite-react-spa/) pairs a Vite-built React SPA with a separate `worker/` API entrypoint.
- [examples/full-demo](examples/full-demo/) combines all of the above in one larger app.

Podman sandbox lifecycle management, OIDC validation, explicit rollback APIs,
and runner reconciliation after an unexpected `workerd` exit remain integration
work.

## Run

Create the local environment and start the infrastructure:

```sh
cp .env.example .env
docker compose -f docker/compose.yml up -d
```

Run `nanoflared` with PostgreSQL, MinIO, and a base hostname for workers that do
not provide an explicit hostname:

```sh
go run ./cmd/nanoflared \
  -addr :8080 \
  -config-dir ./var/generated \
  -base-hostname workers.example.com
```

`nanoflared` automatically loads `.env` when it starts. Existing shell
environment variables take precedence. Loading `DATABASE_URL` makes registered
workers and deployments survive a `nanoflared` restart. Without it, `nanoflared`
uses its intentionally ephemeral in-memory repository.

When a worker is registered without a hostname, `nanoflared` uses
`-base-hostname` or `NANOFLARE_BASE_HOSTNAME` to generate one in the form
`worker-name-a1b2c3d4.workers.example.com`. Requests without a hostname are
rejected when no base hostname is configured.

`nanoflared` also listens on `127.0.0.1:8081` for the private Worker KV adapter.
Use `-runtime-addr` to change the listener address. Do not expose this endpoint
publicly; generated `workerd` configuration injects app-scoped credentials when
calling it.

The Compose Traefik service polls `nanoflared` at
`GET /internal/traefik/config` using `NANOFLARE_TRAEFIK_TOKEN`. Application
traffic still routes directly from Traefik to `workerd`. The default flags
assume Traefik runs from `docker/compose.yml` while `nanoflared` and `workerd`
run on the host.

For a host-run Traefik process configured with its file provider instead, use
the explicit file fallback and loopback addresses:

```sh
go run ./cmd/nanoflared \
  -addr :8080 \
  -auth-url http://127.0.0.1:8080/internal/auth/verify \
  -worker-host 127.0.0.1 \
  -config-dir ./var/generated \
  -traefik-file ./var/generated/traefik.yml
```

`nanoflared` starts `workerd` itself. Use `-workerd /path/to/workerd` when the
binary is not on `PATH`. Its `-config-dir` stores private `workerd`
configuration files; Traefik does not mount this directory.

For a split control plane, start `nanoflare-runner` separately and point
`nanoflared` at its authenticated control API:

```sh
export NANOFLARE_RUNNER_TOKEN=nanoflare-development

go run ./cmd/nanoflare-runner \
  -addr 127.0.0.1:8090 \
  -config-dir ./var/runner \
  -nanoflare-runtime-addr 127.0.0.1:8081

go run ./cmd/nanoflared \
  -addr :8080 \
  -runner-url http://127.0.0.1:8090
```

When `nanoflare-runner` and `nanoflared` run on separate hosts, set
`nanoflared -runtime-addr` to a private reachable listener and pass that address
to `nanoflare-runner -nanoflare-runtime-addr`.

The runner prepares a fresh `workerd` generation and health-checks its sockets.
`nanoflared` publishes the corresponding routes from its HTTP discovery endpoint
and then commits the generation. The runner keeps the previous pool alive for a
short grace period so Traefik can poll the new configuration before old sockets
are retired. Direct `workerd` execution remains available as a development
fallback when `-runner-url` is empty.

Build the CLI:

```sh
go build -o ./bin/nanoflare ./cmd/nanoflare
```

Build all distributable packages with Docker:

```sh
docker build --output type=local,dest=./dist .
```

The exported artifacts include the `nanoflare`, `nanoflare-runner`, and
`nanoflared` binaries under `dist/bin`, the web console under `dist/ui`, and the
TypeScript Worker binding types under `dist/packages/workers-types`.
Use them alongside standard Worker runtime types to type `env.KV`,
`env.ASSETS`, `env.OBJECTS`, and `env.IDENTITY` in TypeScript workers.

Initialize, register, and deploy a worker:

```sh
./bin/nanoflare init --name "Hello worker" --hostname hello.example.com ./hello-worker
cd ./hello-worker
../bin/nanoflare create
../bin/nanoflare deploy
```

`nanoflare init` writes a starter `worker.js` and a `nanoflare.json` project file.
Pass `--hostname` for an explicit DNS hostname, or omit it to let `nanoflared`
generate one from the worker name and configured base hostname. `nanoflare
create` registers the worker and saves its generated app ID and final hostname
locally. `nanoflare deploy` uploads each file listed in `nanoflare.json`. Use
`--api-url`, or set `NANOFLARED_URL`, when `nanoflared` is not listening on
`http://127.0.0.1:8080`.

The deploy command starts a new `workerd` pool generation on fresh runtime
ports, health-checks every socket, publishes healthy upstreams for Traefik
discovery, and then stops the previous generation. In direct mode,
`var/generated` stores the private `workerd` configuration. In split mode,
`nanoflare-runner -config-dir` owns those private runtime files instead.

Deployments store worker file content, not host filesystem paths. New projects
use ES-module syntax and set `"format": "modules"` in `nanoflare.json`, so their
handler receives bindings through `env`, including any configured KV bindings
and any configured object storage bindings such as `env.OBJECTS`. Existing projects without an explicit format remain
compatible: one file uses service-worker syntax and multiple files use
ES-module syntax.

KV namespaces are explicit and follow Cloudflare's `kv_namespaces` pattern. Create
namespaces first:

```sh
nanoflare kv namespace create sessions
nanoflare kv namespace create cache
nanoflare kv namespace list
```

Then bind them in `nanoflare.json`:

```json
{
  "kv_namespaces": [
    { "binding": "SESSIONS", "id": "kvns_sessions" },
    { "binding": "CACHE", "id": "kvns_cache" }
  ]
}
```

Each binding is native inside the Worker:

```js
export default {
  async fetch(request, env) {
    await env.SESSIONS.put("message", "hello");
    return new Response(await env.SESSIONS.get("message"));
  },
};
```

Static assets can be attached to a Worker deployment by setting an assets
directory in `nanoflare.json`. The binding defaults to `ASSETS`, matching
Cloudflare Workers:

```json
{
  "assets": {
    "directory": "public",
    "binding": "ASSETS",
    "not_found_handling": "single-page-application",
    "run_worker_first": ["/api/*"]
  }
}
```

Worker code can fetch attached assets directly:

```js
export default {
  async fetch(request, env) {
    return env.ASSETS.fetch(request);
  },
};
```

Object storage buckets use explicit storage-oriented CLI commands. Create
buckets first:

```sh
nanoflare object-storage bucket create customer-files
nanoflare object-storage bucket list
```

Then bind them in `nanoflare.json`:

```json
{
  "object_storage_buckets": [
    { "binding": "OBJECTS", "bucket_id": "bucket_123" }
  ]
}
```

Application object storage is bucket-scoped and exposed with an R2-style binding:

```js
export default {
  async fetch(_request, env) {
    await env.OBJECTS.put("profiles/user.json", JSON.stringify({ ok: true }), {
      httpMetadata: { contentType: "application/json" },
    });
    const object = await env.OBJECTS.get("profiles/user.json");
    return Response.json({
      head: await env.OBJECTS.head("profiles/user.json"),
      body: object ? await object.json() : null,
    });
  },
};
```

Without `DATABASE_URL` and `MINIO_ENDPOINT`, `nanoflared` still starts with its
in-memory repository for quick unit-level experiments. Object endpoints remain
disabled in that mode.

## Web Console

Run the React control plane UI:

```sh
cd packages/ui
npm install
npm run dev
```

Vite serves the console at `http://127.0.0.1:5173` and proxies `/v1` requests to
`nanoflared` at `http://127.0.0.1:8080`. When `nanoflared` is not running, the
console opens with demo workers and local page and storage management state.

The Compose stack also starts Prometheus at `http://127.0.0.1:9090`. Traefik
publishes request metrics on its internal metrics endpoint, and Prometheus
scrapes them every 15 seconds. The console's Monitoring view queries Prometheus
through Vite's `/prometheus` development proxy.

Worker drill-down data is served by `nanoflared`:

```text
GET /v1/apps/{appID}
GET /v1/apps/{appID}/files
GET /v1/apps/{appID}/output
GET /v1/apps/{appID}/traffic
```

The file viewer exposes the active deployed bundle, output contains the captured
shared `workerd` process stream, and traffic is scoped to the worker's Traefik
router. Set `NANOFLARED_URL` when running Vite to proxy the console to a
non-default control-plane address.

## Security Boundary

The shared pool is intended for company-controlled or reviewed applications.
`workerd` explicitly does not claim to be a hardened sandbox for malicious code.
Less-trusted applications must be placed into dedicated sandboxes or VMs.

`nanoflare-runner` creates a control-plane boundary around runtime lifecycle
operations. It starts `workerd` with a minimal environment that does not inherit
`nanoflared` database, object-store, or API credentials. For production, run the
runner and `workerd` inside a dedicated rootless Podman sandbox or VM with
private ingress and restricted egress. Running the runner on the same host is an
integration step, not a hardened sandbox.

Runtime APIs use stable app-scoped capability tokens injected into private
`workerd` configuration. An application never chooses its own app ID when
reading or writing KV data.
