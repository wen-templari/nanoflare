# platform

`platform` is a lightweight self-hosted runtime for trusted business apps generated
with AI. The design keeps the custom control plane small and uses existing
infrastructure directly:

- Traefik for TLS, host routing, and ForwardAuth.
- `workerd` for a shared pool of Worker isolates.
- PostgreSQL for platform metadata and app-scoped KV.
- MinIO for static assets and application objects.
- Rootless Podman for the shared pool sandbox boundary.

The current repository is the first runnable integration slice of `platformd`. It provides:

- App registration and immutable deployment records.
- A combined `workerd` Cap'n Proto configuration with one isolate and socket per
  active app.
- A Traefik dynamic configuration with host routers and ForwardAuth.
- Atomic config file replacement so watched files are never partially written.
- Managed `workerd` pool generations with readiness checks and blue-green traffic
  replacement.
- App-scoped runtime KV capabilities with PostgreSQL persistence when configured.
- MinIO presigned upload and download URLs with app-prefixed object keys.
- A small TypeScript worker SDK and starter Worker.

Podman sandbox lifecycle management, OIDC validation, explicit rollback APIs,
and the deploy CLI remain integration work.

## Run

Start the local infrastructure:

```sh
docker compose up -d
```

Run `platformd` with PostgreSQL and MinIO:

```sh
cp .env.example .env
go run ./cmd/platformd -addr :8080 -config-dir ./var/generated
```

`platformd` automatically loads `.env` when it starts. Existing shell
environment variables take precedence. Loading `DATABASE_URL` makes registered
workers and deployments survive a `platformd` restart. Without it, `platformd`
uses its intentionally ephemeral in-memory repository.

The default flags assume Traefik runs from `compose.yml` while `platformd` and
`workerd` run on the host. For a host-run Traefik process instead, use loopback
addresses explicitly:

```sh
go run ./cmd/platformd \
  -addr :8080 \
  -auth-url http://127.0.0.1:8080/internal/auth/verify \
  -worker-host 127.0.0.1 \
  -config-dir ./var/generated
```

`platformd` starts `workerd` itself. Use `-workerd /path/to/workerd` when the
binary is not on `PATH`.

Register and deploy a worker bundle:

```sh
APP_ID=$(curl -sS -X POST http://127.0.0.1:8080/v1/apps \
  -H 'content-type: application/json' \
  -d '{"name":"Hello worker","hostname":"hello.example.com"}' | jq -r .id)

curl -X POST "http://127.0.0.1:8080/v1/apps/$APP_ID/deployments" \
  -H 'content-type: application/json' \
  -d '{"files":[{"path":"worker.js","content":"addEventListener(\"fetch\", event => event.respondWith(new Response(\"hello\")));"}],"compatibility_date":"2026-05-31"}'
```

The second request starts a new `workerd` pool generation on fresh runtime
ports, health-checks every socket, writes `var/generated/workerd.capnp` and
`var/generated/traefik.yml`, and then stops the previous generation. Traefik
only observes healthy generations.

Deployments store worker file content, not host filesystem paths. A single file
uses service-worker syntax. For an ES-module worker, send multiple files and set
`entrypoint` to the module that exports the worker handlers.

Without `DATABASE_URL` and `MINIO_ENDPOINT`, `platformd` still starts with its
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
`platformd` at `http://127.0.0.1:8080`. When `platformd` is not running, the
console opens with demo workers and local page and storage management state.

The Compose stack also starts Prometheus at `http://127.0.0.1:9090`. Traefik
publishes request metrics on its internal metrics endpoint, and Prometheus
scrapes them every 15 seconds. The console's Monitoring view queries Prometheus
through Vite's `/prometheus` development proxy.

Worker drill-down data is served by `platformd`:

```text
GET /v1/apps/{appID}
GET /v1/apps/{appID}/files
GET /v1/apps/{appID}/output
GET /v1/apps/{appID}/traffic
```

The file viewer exposes the active deployed bundle, output contains the captured
shared `workerd` process stream, and traffic is scoped to the worker's Traefik
router. Set `PLATFORMD_URL` when running Vite to proxy the console to a
non-default control-plane address.

## Security Boundary

The shared pool is intended for company-controlled or reviewed applications.
`workerd` explicitly does not claim to be a hardened sandbox for malicious code.
Less-trusted applications must be placed into dedicated sandboxes or VMs.

Runtime APIs use deployment capability tokens. An application never chooses its
own app ID when reading or writing KV data.
