---
name: nanoflare-deploy
description: Scaffold and operate a production-oriented, self-hosted Nanoflare platform. Use this skill whenever the user asks to deploy, self-host, containerize, configure Docker Compose for, put behind a reverse proxy, operate, secure, observe, or upgrade Nanoflare; mentions nanoflared, nanoflare-runner, GHCR images, Traefik, MinIO, or the Nanoflare control plane also trigger it. Generate the deployment artifacts, not merely general advice.
---

# Deploy Nanoflare

Nanoflare is a control plane plus a Worker runtime, rather than a single web
application. Keep public routing, control-plane state, Worker execution, and
application data on the correct network boundaries; a Compose file that merely
starts every image is not a usable production deployment.

Read [references/architecture.md](references/architecture.md) before designing
or changing a deployment. When working in the Nanoflare repository, also
inspect the current `docker/compose.yml`, `.env.example`, and `README.md` so
generated artifacts match the version in use.

## Default deployment shape

Use Docker Compose and the published images unless the user explicitly asks for
another orchestrator:

- `ghcr.io/wen-templari/nanoflared:<tag>` runs the control plane.
- `ghcr.io/wen-templari/nanoflare-runner:<tag>` owns `workerd` pool
  generations.
- `ghcr.io/wen-templari/nanoflare-ui:<tag>` serves the control-plane UI.
- PostgreSQL stores control-plane metadata and KV data. MinIO provides
  application objects and deployment assets.
- Traefik discovers Worker routes from `nanoflared`. Prometheus, Loki, Vector,
  and Grafana are optional but should be included when the user requests
  observability.

The standard topology assumes an existing load balancer or reverse proxy owns
public TLS. Publish only that proxy; keep database, MinIO, runner control,
Worker runtime, and observability ports on private Compose networks. Traefik
may expose its web/websecure entrypoints only to the upstream proxy. Never use
the repository's `*-development`, `change-me`, anonymous Grafana, or insecure
Traefik API defaults in a production artifact.

## Workflow

1. Gather the deployment inputs: immutable Nanoflare image tag, domain names,
   external TLS proxy address/network, persistent-volume or managed-service
   choices, secret delivery method, and whether OIDC, backups, egress proxy, or
   observability are required. Ask only for missing inputs that make a generated
   configuration unsafe or unusable.
2. Produce a deployment directory containing `compose.yml`, `.env.example`,
   a secret-handling note, and a concise `README.md` with launch, upgrade, and
   verification commands. Use `${VAR:?required}` for values that must be
   supplied, and do not put real secrets in tracked files.
3. Place PostgreSQL and MinIO data on persistent volumes (or use the user's
   managed endpoints). Persist the control-plane generated/config directory and
   the runner directory separately. Ensure the MinIO bucket exists before
   application deployment.
4. Configure `nanoflared` with its database, object-storage, base-hostname,
   Traefik discovery token, runner token, private runner URL, private runtime
   address, and an `auth-url` reachable by Traefik. Configure the runner with
   the same runner token, a private runtime address, and a stable internal
   runtime port host.
5. Configure Traefik's HTTP provider to poll Nanoflare's authenticated
   discovery endpoint. Do not expose `/internal/traefik/config`, the runtime
   adapter, runner API, Traefik dashboard, PostgreSQL, or MinIO to the public
   internet.
6. Add optional features only when requested: control-plane OIDC, protected
   Worker-route OIDC, Litestream backups, corporate fetch egress proxy/CA/no-
   proxy policy, and the logging/metrics stack. Explain their required external
   dependencies and keep their credentials out of Compose literals.
7. Validate syntax with `docker compose config`, start dependencies, wait for
   PostgreSQL health, then verify control-plane, UI, Traefik discovery, and a
   deployed Worker through the external proxy. Include rollback and log-inspection
   commands in the handoff.

## Output requirements

State the selected topology and assumptions before presenting artifacts. Make
the Compose networks and persistent storage explicit. Pin Nanoflare images to a
release tag chosen by the user; if none is known, leave a required
`NANOFLARE_IMAGE_TAG` variable instead of silently using `latest`.

For a deployment change, return a short inventory of created/changed files,
required secrets, externally provisioned resources, and verification steps.
Call out any feature that Nanoflare does not currently support rather than
inventing an integration.
