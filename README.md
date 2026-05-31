# platform

`platform` is a lightweight self-hosted runtime for trusted business apps generated
with AI. The design keeps the custom control plane small and uses existing
infrastructure directly:

- Traefik for TLS, host routing, and ForwardAuth.
- `workerd` for a shared pool of Worker isolates.
- PostgreSQL for platform metadata and app-scoped KV.
- MinIO for static assets and application objects.
- Rootless Podman for the shared pool sandbox boundary.

The current repository is the first runnable slice of `platformd`. It provides:

- App registration and immutable deployment records.
- A combined `workerd` Cap'n Proto configuration with one isolate and socket per
  active app.
- A Traefik dynamic configuration with host routers and ForwardAuth.
- Atomic config file replacement so watched files are never partially written.
- App-scoped runtime KV capabilities with an in-memory store.
- A small TypeScript worker SDK and starter Worker.

PostgreSQL, MinIO, Podman lifecycle management, OIDC validation, blue-green pool
replacement, rollback, and the deploy CLI remain integration work.

## Run

```sh
go run ./cmd/platformd -addr :8080 -config-dir ./var/generated
```

Register and deploy a worker bundle:

```sh
curl -X POST http://127.0.0.1:8080/v1/apps \
  -H 'content-type: application/json' \
  -d '{"id":"hello","hostname":"hello.example.com"}'

curl -X POST http://127.0.0.1:8080/v1/apps/hello/deployments \
  -H 'content-type: application/json' \
  -d '{"bundle_path":"/srv/apps/hello/worker.js","compatibility_date":"2026-05-31"}'
```

The second request writes `var/generated/workerd.capnp` and
`var/generated/traefik.yml`. In the full runtime, `platformd` will start a new
pool generation, health-check it, and then allow Traefik to observe the new
dynamic configuration.

## Security Boundary

The shared pool is intended for company-controlled or reviewed applications.
`workerd` explicitly does not claim to be a hardened sandbox for malicious code.
Less-trusted applications must be placed into dedicated sandboxes or VMs.

Runtime APIs use deployment capability tokens. An application never chooses its
own app ID when reading or writing KV data.
