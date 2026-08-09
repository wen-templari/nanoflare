# Nanoflare deployment reference

## Components and traffic

| Component                      | Responsibility                                                                 | Must be private?                             |
| ------------------------------ | ------------------------------------------------------------------------------ | -------------------------------------------- |
| External proxy/load balancer   | Public TLS termination and forwarding to Traefik                               | Public edge only                             |
| Traefik                        | Host routing, ForwardAuth, and Nanoflare HTTP discovery                        | Yes, behind the edge                         |
| `nanoflared`                   | Control API/UI backend, deployment metadata, route discovery, auth, KV adapter | Yes                                          |
| `nanoflare-runner`             | Authenticated control API that starts and health-checks `workerd` generations  | Yes                                          |
| `workerd`                      | Runs deployed Workers on runner-managed sockets                                | Yes                                          |
| PostgreSQL                     | Control-plane metadata and application KV persistence                          | Yes                                          |
| MinIO/S3                       | Deployment assets, static assets, and application object storage               | Yes                                          |
| UI                             | Nginx-served console that proxies API requests to `nanoflared`                 | Through the edge or internal console ingress |
| Prometheus/Loki/Vector/Grafana | Optional metrics, logs, collection, and dashboards                             | Yes                                          |

Worker requests flow from the public edge to Traefik and directly to the active
`workerd` socket. Traefik polls `nanoflared` for routing configuration. During a
deployment, the runner starts and checks a fresh pool; the control plane
publishes routes, then retires the prior pool after a short grace period.

## Required configuration

Use one immutable `NANOFLARE_IMAGE_TAG` for `nanoflared`, `nanoflare-runner`,
and the UI. The repository publishes multi-architecture GHCR images under:

- `ghcr.io/wen-templari/nanoflared`
- `ghcr.io/wen-templari/nanoflare-runner`
- `ghcr.io/wen-templari/nanoflare-ui`

`nanoflared` needs `DATABASE_URL`, `MINIO_ENDPOINT`, `MINIO_ACCESS_KEY`,
`MINIO_SECRET_KEY`, `MINIO_BUCKET`, `MINIO_SECURE`,
`NANOFLARE_BASE_HOSTNAME`, `NANOFLARE_TRAEFIK_TOKEN`,
`NANOFLARE_RUNNER_TOKEN`, and a high-entropy `NANOFLARE_SECRET_KEY`.

The runner needs the same `NANOFLARE_RUNNER_TOKEN`; use `-runner-url` on the
control plane and `-nanoflare-runtime-addr` on the runner to join them. The
runtime adapter address must be reachable from the runner but never public.

Set the worker base hostname to a DNS zone routed through the external edge,
for example `workers.platform.example.com`. Nanoflare assigns worker hostnames
below that base hostname. Configure wildcard DNS and routing for that zone.

## Optional configuration

- Worker-route OIDC uses `NANOFLARE_OIDC_*`; control-plane OIDC uses the
  separate `NANOFLARE_CONTROL_OIDC_*` variables. Register the console callback
  at `/v1/auth/oidc/callback` on the configured control-plane public URL.
- Set `NANOFLARE_LITESTREAM_ENABLED=true` and supply a replica target for
  database backup. Litestream replicates SQLite Worker databases, but is not
  multi-node write/failover support.
- Set `NANOFLARE_WORKERD_EGRESS_PROXY_URL`, CA files, and no-proxy destinations
  only when Worker global `fetch()` must traverse a corporate proxy. Keep the
  no-proxy policy aligned with `NANOFLARE_WORKERD_NETWORK_ALLOW`.
- For host-run runtime logging, configure Vector socket and Loki URL. Compose
  logging normally uses Vector's Docker source.

## Production checklist

- Supply secrets from the deployment platform or an untracked env file; rotate
  the Traefik and runner tokens independently.
- Use persistent, backed-up storage for PostgreSQL, MinIO, control-plane
  generated state, and runner state.
- Restrict internal networks/firewall rules and disable Traefik's insecure API.
- Pin image tags and change all Nanoflare image tags together during upgrades.
- Run `docker compose config`, inspect service health, test console login (if
  configured), deploy a smoke-test Worker, and route it through the public edge.
