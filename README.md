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
set -a
. ./.env.example
set +a
go run ./cmd/platformd -addr :8080 -config-dir ./var/generated
```

When Traefik runs from `compose.yml` and `platformd` plus `workerd` run on the
host, expose host-reachable callback and worker addresses:

```sh
go run ./cmd/platformd \
  -addr :8080 \
  -auth-url http://host.docker.internal:8080/internal/auth/verify \
  -worker-host host.docker.internal \
  -config-dir ./var/generated
```

`platformd` starts `workerd` itself. Use `-workerd /path/to/workerd` when the
binary is not on `PATH`.

Register and deploy a worker bundle:

```sh
curl -X POST http://127.0.0.1:8080/v1/apps \
  -H 'content-type: application/json' \
  -d '{"id":"hello","hostname":"hello.example.com"}'

curl -X POST http://127.0.0.1:8080/v1/apps/hello/deployments \
  -H 'content-type: application/json' \
  -d '{"bundle_path":"/srv/apps/hello/worker.js","compatibility_date":"2026-05-31"}'
```

The second request starts a new `workerd` pool generation on fresh runtime
ports, health-checks every socket, writes `var/generated/workerd.capnp` and
`var/generated/traefik.yml`, and then stops the previous generation. Traefik
only observes healthy generations.

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

## Security Boundary

The shared pool is intended for company-controlled or reviewed applications.
`workerd` explicitly does not claim to be a hardened sandbox for malicious code.
Less-trusted applications must be placed into dedicated sandboxes or VMs.

Runtime APIs use deployment capability tokens. An application never chooses its
own app ID when reading or writing KV data.
