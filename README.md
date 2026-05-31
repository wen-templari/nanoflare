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
- A Traefik HTTP discovery endpoint with host routers and ForwardAuth.
- An optional atomic-file fallback for a host-run Traefik process.
- Managed `workerd` pool generations with readiness checks and blue-green traffic
  replacement.
- App-scoped runtime KV capabilities with PostgreSQL persistence when configured.
- MinIO presigned upload and download URLs with app-prefixed object keys.
- A small TypeScript worker SDK and starter Worker.

Podman sandbox lifecycle management, OIDC validation, explicit rollback APIs,
and runner reconciliation after an unexpected `workerd` exit remain integration
work.

## Run

Create the local environment and start the infrastructure:

```sh
cp .env.example .env
docker compose up -d
```

Run `platformd` with PostgreSQL and MinIO:

```sh
go run ./cmd/platformd -addr :8080 -config-dir ./var/generated
```

`platformd` automatically loads `.env` when it starts. Existing shell
environment variables take precedence. Loading `DATABASE_URL` makes registered
workers and deployments survive a `platformd` restart. Without it, `platformd`
uses its intentionally ephemeral in-memory repository.

The Compose Traefik service polls `platformd` at
`GET /internal/traefik/config` using `PLATFORM_TRAEFIK_TOKEN`. Application
traffic still routes directly from Traefik to `workerd`. The default flags
assume Traefik runs from `compose.yml` while `platformd` and `workerd` run on
the host.

For a host-run Traefik process configured with its file provider instead, use
the explicit file fallback and loopback addresses:

```sh
go run ./cmd/platformd \
  -addr :8080 \
  -auth-url http://127.0.0.1:8080/internal/auth/verify \
  -worker-host 127.0.0.1 \
  -config-dir ./var/generated \
  -traefik-file ./var/generated/traefik.yml
```

`platformd` starts `workerd` itself. Use `-workerd /path/to/workerd` when the
binary is not on `PATH`. Its `-config-dir` stores private `workerd`
configuration files; Traefik does not mount this directory.

For a split control plane, start `platform-runner` separately and point
`platformd` at its authenticated control API:

```sh
export PLATFORM_RUNNER_TOKEN=platform-development

go run ./cmd/platform-runner \
  -addr 127.0.0.1:8090 \
  -config-dir ./var/runner

go run ./cmd/platformd \
  -addr :8080 \
  -runner-url http://127.0.0.1:8090
```

The runner prepares a fresh `workerd` generation and health-checks its sockets.
`platformd` publishes the corresponding routes from its HTTP discovery endpoint
and then commits the generation. The runner keeps the previous pool alive for a
short grace period so Traefik can poll the new configuration before old sockets
are retired. Direct `workerd` execution remains available as a development
fallback when `-runner-url` is empty.

Build the CLI:

```sh
go build -o ./bin/platform ./cmd/platform
```

Initialize, register, and deploy a worker:

```sh
./bin/platform init --name "Hello worker" --hostname hello.example.com ./hello-worker
cd ./hello-worker
../bin/platform create
../bin/platform deploy
```

`platform init` writes a starter `worker.js` and a `platform.json` project file.
`platform create` registers the worker and saves its generated app ID locally.
`platform deploy` uploads each file listed in `platform.json`. Use
`--api-url`, or set `PLATFORMD_URL`, when `platformd` is not listening on
`http://127.0.0.1:8080`.

The deploy command starts a new `workerd` pool generation on fresh runtime
ports, health-checks every socket, publishes healthy upstreams for Traefik
discovery, and then stops the previous generation. In direct mode,
`var/generated` stores the private `workerd` configuration. In split mode,
`platform-runner -config-dir` owns those private runtime files instead.

Deployments store worker file content, not host filesystem paths. A single file
uses service-worker syntax. For an ES-module worker, list multiple files in
`platform.json` and set `entrypoint` to the module that exports the worker
handlers.

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

`platform-runner` creates a control-plane boundary around runtime lifecycle
operations. It starts `workerd` with a minimal environment that does not inherit
`platformd` database, object-store, or API credentials. For production, run the
runner and `workerd` inside a dedicated rootless Podman sandbox or VM with
private ingress and restricted egress. Running the runner on the same host is an
integration step, not a hardened sandbox.

Runtime APIs use deployment capability tokens. An application never chooses its
own app ID when reading or writing KV data.
