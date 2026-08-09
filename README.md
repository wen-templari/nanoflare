# nanoflare

`nanoflare` is a lightweight self-hosted runtime for trusted business apps generated
with AI. The design keeps the custom control plane small and uses existing
infrastructure directly:

- Traefik for TLS, host routing, and ForwardAuth.
- `workerd` for a shared pool of Worker isolates.
- PostgreSQL for nanoflare metadata and app-scoped KV.
- MinIO for static assets and application objects.

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
- A bucket-scoped, Cloudflare R2-style `env.OBJECTS` binding backed by MinIO, with `put`, `get`, `head`, and `delete` operations plus object metadata/body helpers.
- A starter Worker and a TypeScript types package for Worker bindings.

Example workers live under `examples/`:

- [examples/simple-kv](examples/simple-kv/) shows a hello-world counter backed by an explicit KV binding.
- [examples/gallery-app](examples/gallery-app/) serves a static UI, uploads images to object storage, and stores gallery metadata in KV.
- [examples/protected-app](examples/protected-app/) protects `/api/auth/*` routes and returns resolved auth information from the Worker.
- [examples/service-bindings](examples/service-bindings/) shows private Worker-to-Worker HTTP calls and RPC with Cloudflare-style `services` bindings.

Each example directory includes its own README with setup steps and routes to try.

Podman sandbox lifecycle management, OIDC validation, explicit rollback APIs,
and runner reconciliation after an unexpected `workerd` exit remain integration
work.

## Development Environment

Prerequisites:

- Docker with Compose for PostgreSQL, MinIO, Traefik, and Prometheus.
- Go to run `./cmd/nanoflare`, `./cmd/nanoflared`, and `./cmd/nanoflare-runner`.
- Node.js and npm for the example apps and web console.
- `workerd` on `PATH`, or pass `-workerd /path/to/workerd` to the control plane or runner.

For local development, run the dependencies in Docker and keep `nanoflared`,
`nanoflare-runner`, and `workerd` on the host:

```sh
cp .env.example .env
docker compose -f docker/compose.dev.yml up -d
```

Then update `.env` with development credentials that match the Compose defaults:

```dotenv
DATABASE_URL=postgres://nanoflare:nanoflare-development@127.0.0.1:5432/nanoflare?sslmode=disable
MINIO_ENDPOINT=127.0.0.1:9000
MINIO_ACCESS_KEY=nanoflare
MINIO_SECRET_KEY=nanoflare-development
MINIO_BUCKET=nanoflare
MINIO_SECURE=false
NANOFLARE_TRAEFIK_TOKEN=nanoflare-development
NANOFLARE_RUNNER_TOKEN=nanoflare-development
NANOFLARE_BASE_HOSTNAME=workers.example.test
```

`docker/compose.dev.yml` starts only shared dependencies. Its Traefik instance
polls `http://host.docker.internal:8080/internal/traefik/config`, so
`nanoflared` must be running on the host at port `8080`.

Use `docker/compose.yml` only when you want the full stack to run inside
Compose, including `nanoflared` and `nanoflare-runner`.

Start the control plane on the host:

Run `nanoflared` with PostgreSQL, MinIO, and a base hostname for workers that do
not provide an explicit hostname:

```sh
go run ./cmd/nanoflared \
  -addr :8080 \
  -config-dir ./var/generated \
  -base-hostname workers.example.test
```

`nanoflared` automatically loads `.env` when it starts. Existing shell
environment variables take precedence. Loading `DATABASE_URL` makes registered
workers and deployments survive a `nanoflared` restart. Without it, `nanoflared`
uses its intentionally ephemeral in-memory repository.

When a worker is registered without a hostname, `nanoflared` uses
`-base-hostname` or `NANOFLARE_BASE_HOSTNAME` to generate one in the form
`worker-name-org.workers.example.test`. If that hostname is already taken,
`nanoflared` retries with a random suffix, for example
`worker-name-a1b2c3d4e5-org.workers.example.test`. Requests without a hostname
are rejected when no base hostname is configured.

`nanoflared` also listens on `127.0.0.1:8081` for the private Worker KV adapter.
Use `-runtime-addr` to change the listener address. Do not expose this endpoint
publicly; generated `workerd` configuration injects app-scoped credentials when
calling it.

The development Traefik service polls `nanoflared` at
`GET /internal/traefik/config` using `NANOFLARE_TRAEFIK_TOKEN`. Application
traffic still routes directly from Traefik to `workerd`. The default local-dev
flow assumes Traefik runs from `docker/compose.dev.yml` while `nanoflared` and
`workerd` run on the host.

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

By default, Worker global `fetch()` can reach public internet addresses and
services in `10.0.0.0/8`:

```sh
NANOFLARE_WORKERD_NETWORK_ALLOW=public,10.0.0.0/8
```

Use `public,local,10.0.0.0/8` when Workers also need local-machine addresses
such as `127.0.0.1`, or `public,private` when Workers should reach all private
network ranges, including `10.0.0.0/8`, `172.16.0.0/12`, and
`192.168.0.0/16`. The same value can be passed with
`-workerd-network-allow`.

### Corporate proxy egress

Set `NANOFLARE_WORKERD_EGRESS_PROXY_URL` when Worker global `fetch()` must use
an HTTP corporate proxy. Add intercepting or private certificate authorities
with `NANOFLARE_WORKERD_EGRESS_CA_FILES`. Requests matching
`NANOFLARE_WORKERD_EGRESS_NO_PROXY` bypass that proxy; entries support literal
IP addresses, CIDRs, and hostname suffixes:

```sh
NANOFLARE_WORKERD_EGRESS_PROXY_URL=http://proxy.corp.example:8080
NANOFLARE_WORKERD_EGRESS_CA_FILES=/run/secrets/corp-root.pem
NANOFLARE_WORKERD_EGRESS_NO_PROXY=10.0.0.0/8,.corp.example
```

When a proxy is configured, `globalOutbound` directs Worker `fetch()` through
the adapter, so treat the no-proxy list as an explicit egress policy. Keep it
aligned with the private destinations intended to be allowed by
`NANOFLARE_WORKERD_NETWORK_ALLOW`.

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

Or install the prebuilt CLI from npm:

```sh
npm install -g @nanoflare/cli
nanoflare --help
```

Build all distributable packages with Docker:

```sh
docker build --output type=local,dest=./dist .
```

The exported artifacts include the `nanoflare`, `nanoflare-runner`, and
`nanoflared` binaries under `dist/bin` and the web console under `dist/ui`.

## `nanoflare.json` schema

The project configuration schema lives at
[`schemas/nanoflare.json`](schemas/nanoflare.json). New projects generated by
`nanoflare init` and the included examples reference its canonical URL:

```json
{
  "$schema": "https://raw.githubusercontent.com/wen-templari/nanoflare/main/schemas/nanoflare.json"
}
```

GitHub's raw-content endpoint is the current hosting location because it serves
the schema directly from the release-controlled source. Before making a stable
public release, publish the same schema at a Nanoflare-controlled, versioned
docs URL (for example, `https://schema.nanoflare.dev/v1.json`) and retain this
URL as a compatibility redirect. That prevents an editor integration from
depending on either GitHub availability or the moving `main` branch.

## Generate Worker Types

Generate a self-contained declaration file from the bindings and variables in
the current directory's `nanoflare.json`:

```sh
nanoflare types
```

This writes `worker-configuration.d.ts` with a global `Env` interface. Choose a
different path or interface name when needed:

```sh
nanoflare types bindings.d.ts --env-interface CloudflareBindings
```

The generated file includes the Nanoflare KV, database, asset, object storage,
and Worker handler declarations (including scheduled handlers). Add a script
such as the following to regenerate it during development:

```json
{
  "scripts": {
    "cf-typegen": "nanoflare types --env-interface CloudflareBindings"
  }
}
```

Use `nanoflare types --check` in CI to ensure the declaration file is current.

For typed service bindings, pass the caller configuration first and each local
service target with another `-c` flag. These paths are used only to resolve the
target's TypeScript entrypoint; they do not change the deployed configuration.

```sh
nanoflare types -c ./api-worker/nanoflare.json -c ../identity-worker/nanoflare.json
```

### Required secrets

Store secret values separately from your project configuration:

```sh
nanoflare secret put API_KEY "$API_KEY"
```

Declare the names required by a Worker in `nanoflare.json`. Secret values are
never written to this file:

```json
{
  "secrets": {
    "required": ["API_KEY", "DB_PASSWORD"]
  }
}
```

`nanoflare types` generates each declared secret as a `string` binding. Before
deploying, `nanoflare deploy` checks that every declared secret is configured
for the Worker and reports any names that are missing.

Initialize, register, and deploy a worker:

```sh
npm create nanoflare@latest hello-worker
cd ./hello-worker
nanoflare create
nanoflare deploy
```

`npm create nanoflare@latest` scaffolds a TypeScript starter Worker without installing
dependencies or contacting a Nanoflare server. It prompts for a directory and
template in an interactive terminal. Templates include `bindings` (KV and object
storage), `pages` (static pages), `spa`, `ssr`, and `api` (Drizzle and OpenAPI).
Pass `-- --template starter` to select the minimal Worker explicitly.

To use the commands above, install `@nanoflare/cli` or run the local repository
binary. The equivalent local workflow is:

```sh
./bin/nanoflare init ./hello-worker --template starter
cd ./hello-worker
../bin/nanoflare create
../bin/nanoflare deploy
```

`nanoflare init` is a convenience proxy for `npm create nanoflare@latest`; it
forwards all initializer arguments, including `--template`, `--overwrite`, and
interactive controls. Use `nanoflare init --help` for the initializer's available
options. It requires npm and network access.
`nanoflare create` registers the worker by its configured name; Nanoflare assigns
its hostname from the configured base hostname. `nanoflare deploy` resolves that
name in the selected organization and uploads each file listed in `nanoflare.json`.
After a successful deployment, it prints the worker's public URL.
Use `--api-url`, or set `NANOFLARED_URL`, when `nanoflared` is not listening on
`http://127.0.0.1:8080`. CLI authentication is stored at
the OS user-config directory by default (`~/Library/Application Support/nanoflare/auth.json`
on macOS and `~/.config/nanoflare/auth.json` on Linux); set
`NANOFLARE_AUTH_STORE` to an alternate file path when you need a different
auth store location. For non-interactive use, set `NANOFLARE_TOKEN` and, when
the command operates on an organization-scoped resource, `NANOFLARE_ORG_ID`:

```sh
export NANOFLARED_URL=https://nanoflare.example.com
export NANOFLARE_TOKEN=your-personal-access-token
export NANOFLARE_ORG_ID=org_123
nanoflare deploy
```

Non-empty environment values override the corresponding value in `auth.json`.
`NANOFLARE_TOKEN` is never refreshed or written back to the auth store, making
it appropriate for CI; replace it in the environment when it expires or is
rotated. `nanoflare auth whoami` and `nanoflare auth orgs` show the configured
organization without attempting to resolve identity metadata when environment
credentials are active.
Use `nanoflare deployment output` from a registered project to print captured
worker output, or pass a worker ID with `nanoflare deployment output <worker-id>`.
When `NANOFLARE_LOKI_URL` is configured, output is retained in Loki and can be
filtered with `--deployment`, `--level`, `--search`, `--since`, and `--until`.

The browser console can also use an external OIDC provider for login. Configure
the console-specific settings on `nanoflared`:

```sh
NANOFLARE_CONTROL_OIDC_ISSUER=https://auth.example.com/oidc
NANOFLARE_CONTROL_OIDC_CLIENT_ID=nanoflare-console
NANOFLARE_CONTROL_OIDC_CLIENT_SECRET=change-me
NANOFLARE_CONTROL_OIDC_PUBLIC_URL=https://console.example.com
NANOFLARE_CONTROL_OIDC_EMAIL_CLAIM=email
NANOFLARE_CONTROL_OIDC_DIRECT_LOGIN=true
```

Register `https://console.example.com/v1/auth/oidc/callback` as the OIDC client
redirect URI. These settings may point at the same identity provider as
protected worker-route OIDC, but they are intentionally separate so enabling
worker auth does not automatically enable console registration and login.
`nanoflare auth login` prompts for the web console flow or a personal access
token. Use `nanoflare auth login --web` to skip the prompt, or
`nanoflare auth login --pat <token>` /
`nanoflare auth login --pat-token <token>` for non-interactive personal access
token login.

External platforms can integrate through Nanoflare's OAuth control-plane flow.
First create an OAuth client while signed in as a Nanoflare control-plane user.
The client is owned by the organization identified in the request URL; any
member of that owner organization can manage its redirect URIs, scopes, and secrets:

```sh
curl -X POST http://127.0.0.1:8080/v1/organizations/$NANOFLARE_OWNER_ORG_ID/oauth-clients \
  -H "Authorization: Bearer $NANOFLARE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "External Platform",
    "redirect_uris": ["https://external.example.com/oauth/callback"],
    "scopes": ["workers:read", "workers:write", "deployments:write", "kv:write"]
  }'
```

The response includes a `client_id` and one-time-visible `client_secret`. The
owner organization controls the client registration, but authorization is per
user and per resource organization. The external platform redirects its user to
its own connection flow, then asks Nanoflare to authorize a specific Nanoflare
organization that the approving user belongs to:

```sh
curl -X POST http://127.0.0.1:8080/v1/oauth/authorize \
  -H "Authorization: Bearer $NANOFLARE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "client_id": "CLIENT_ID",
    "redirect_uri": "https://external.example.com/oauth/callback",
    "scopes": ["workers:write", "deployments:write"],
    "org_id": "NANOFLARE_ORG_ID",
    "state": "opaque-state"
  }'
```

Exchange the returned code from the external platform backend:

```sh
curl -X POST http://127.0.0.1:8080/v1/oauth/token \
  -H "Content-Type: application/json" \
  -d '{
    "grant_type": "authorization_code",
    "client_id": "CLIENT_ID",
    "client_secret": "CLIENT_SECRET",
    "code": "AUTHORIZATION_CODE",
    "redirect_uri": "https://external.example.com/oauth/callback"
  }'
```

Use the returned access token with existing `/v1` resource APIs. Nanoflare
derives the organization from the OAuth token and enforces the granted scopes:

```sh
curl -X POST http://127.0.0.1:8080/v1/organizations/$NANOFLARE_ORG_ID/workers \
  -H "Authorization: Bearer $NANOFLARE_ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"Managed Worker","hostname":"managed.example.com","external_id":"external-worker-123"}'
```

When the access token expires, exchange the refresh token with
`grant_type=refresh_token`. Users can inspect connected external apps with
`GET /v1/oauth/connections` and disconnect one with
`DELETE /v1/oauth/connections/{clientID}`.

### Partner-managed tenant connections

For a fully embedded external-platform experience, create a partner integration
from the owning Nanoflare organization. The returned secret is shown once and
must be kept by the external platform backend; never send it to a browser.

```sh
curl -X POST http://127.0.0.1:8080/v1/organizations/$NANOFLARE_OWNER_ORG_ID/partner-integrations \
  -H "Authorization: Bearer $NANOFLARE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"External Platform","allowed_scopes":["workers:read","workers:write","deployments:write","secrets:write","kv:read","kv:write","db:read","db:write","objects:read","objects:write"]}'
```

The external platform backend provisions a tenant organization and receives a
tenant-scoped rotating token pair. Repeating the request for the same immutable
`external_account_id` returns the same organization and connection.

```sh
curl -X POST http://127.0.0.1:8080/v1/organizations/$NANOFLARE_OWNER_ORG_ID/partner-integrations/$INTEGRATION_ID/connections \
  -u "$INTEGRATION_ID:$PARTNER_INTEGRATION_SECRET" \
  -H "Content-Type: application/json" \
  -d '{"external_account_id":"workspace_123","organization_name":"Acme","requested_scopes":["workers:write","deployments:write","kv:write"]}'
```

Refresh that connection with `POST /v1/organizations/{organization_id}/partner-integrations/{integration_id}/token`, supplying
`connection_id` and `refresh_token` with the same HTTP Basic client authentication.
Disconnect it with
`DELETE /v1/organizations/{organization_id}/partner-integrations/{integration_id}/connections/{connection_id}`;
this revokes all connection tokens while retaining the organization and its
resources. Repeating provisioning reconnects the retained organization with a
new token pair. Partner integration owners can list integrations and
connections, rotate the provisioning secret, or disable an integration through
the corresponding `/v1/partner-integrations` endpoints.
See [`packages/external-app`](packages/external-app) for a browser UI backed by
a server-side external-platform implementation.

The starter project is TypeScript and must be built before deployment. The
example apps under `examples/` use npm-based build steps first because they
bundle TypeScript, React, or both before `nanoflare deploy`.

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

SQLite databases use explicit `db` bindings. Create a database first:

```sh
nanoflare db create app-data
nanoflare db list
```

Then bind it in `nanoflare.json`:

```json
{
  "db": [{ "binding": "DB", "database_id": "db_123" }]
}
```

Worker code receives a D1-style binding:

```js
export default {
  async fetch(_request, env) {
    await env.DB.exec("CREATE TABLE IF NOT EXISTS messages (body text)");
    await env.DB.prepare("INSERT INTO messages (body) VALUES (?)").bind("hello").run();
    const row = await env.DB.prepare("SELECT body FROM messages LIMIT 1").first();
    return Response.json(row);
  },
};
```

For one-shot SQL and migrations:

```sh
nanoflare db execute db_123 --command "CREATE TABLE messages (id integer primary key, body text)"
nanoflare db execute db_123 --command "INSERT INTO messages (body) VALUES ('hello')"
nanoflare db execute db_123 --command "SELECT id, body FROM messages"
nanoflare db execute db_123 --file query.sql
nanoflare db execute db_123 --command "SELECT id, body FROM messages" --json
nanoflare db migrations create add_messages
nanoflare db migrations apply db_123
```

`db execute` runs exactly one SQL statement. Query results are printed as a
table by default; use `--json` when scripting. Use `--file` for one statement
saved in a file, and use migrations for multi-statement schema changes.

`nanoflared` stores SQLite files under `-db-dir`, defaulting to
`<config-dir>/db`. Litestream can be enabled with `-litestream-enabled`.
When `-litestream-config` is omitted, `nanoflared` generates
`<config-dir>/litestream.generated.yml`, adds databases to it as they are opened,
and starts or restarts Litestream replication as needed. The generated config
uses `NANOFLARE_LITESTREAM_REPLICA_URL_PREFIX` when set, or falls back to
`MINIO_ENDPOINT`, `MINIO_BUCKET`, `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY`, and
`MINIO_SECURE`.

For a custom S3-compatible target:

```sh
NANOFLARE_LITESTREAM_REPLICA_URL_PREFIX=s3://my-bucket/nanoflare-litestream
NANOFLARE_LITESTREAM_ENDPOINT=https://s3.example.com
NANOFLARE_LITESTREAM_ACCESS_KEY_ID=...
NANOFLARE_LITESTREAM_SECRET_ACCESS_KEY=...
nanoflared -litestream-enabled
```

`-litestream-config` and `-litestream-bin` are still available for fully manual
configurations or non-default binary locations. Litestream restores a missing
local database before it is opened and then runs as a long-lived replication
process; it is not started per query and does not provide multi-node writes or
automatic primary failover.

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
  "object_storage_buckets": [{ "binding": "OBJECTS", "bucket_id": "bucket_123" }]
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
pnpm dev:ui
```

Vite serves the console at `http://127.0.0.1:5173` and proxies `/v1` requests to
`nanoflared` at `http://127.0.0.1:8080`. When `nanoflared` is not running, the
console opens with demo workers and local page and storage management state.

The development Compose stack also starts Prometheus at
`http://127.0.0.1:9090`. Traefik publishes request metrics on its internal
metrics endpoint, and Prometheus scrapes them every 15 seconds. `nanoflared`
queries Prometheus to serve the console's Monitoring view.

The Compose stack also starts Vector and Loki for logs. Vector labels worker
output with its worker and deployment IDs before writing it to Loki; Grafana at
`http://127.0.0.1:3000` can query all platform services. For a host-run runtime,
set `NANOFLARE_LOG_VECTOR_SOCKET` to Vector's Unix socket path (or
`tcp://127.0.0.1:6000` when Vector runs in Docker Compose on macOS) and
`NANOFLARE_LOKI_URL` to the Loki endpoint. If the collector is unavailable,
Nanoflare continues running and drops newly queued log events rather than
blocking Workers.

Worker drill-down data is served by `nanoflared`:

```text
GET /v1/workers/{workerID}
GET /v1/workers/{workerID}/files
GET /v1/workers/{workerID}/output
GET /v1/workers/{workerID}/traffic
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
`workerd` configuration. An application never chooses its own worker ID when
reading or writing KV data.
