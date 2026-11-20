# Nanoflare Stack Overview

Both Compose configurations mount `provisioning/` into Grafana. The dashboard
appears in the **Nanoflare** folder at
<http://127.0.0.1:3000/d/nanoflare-stack-overview>.

For a complete container stack, run `docker compose -f docker/compose.yml up -d`
from the repository root. To build Nanoflare from the local source, add
`-f docker/compose.e2e.yml` before `up -d --build`. The development Compose file
runs dependencies only and expects the daemon and runner on the host.

Configure a matching `NANOFLARE_METRICS_TOKEN` and
`NANOFLARE_METRICS_TOKEN_FILE` before starting the stack. The empty default
disables the daemon exporter, so its scrape health is Down and its panels have
no data. See the [authenticated scrape setup](../../docs/guides/workers-for-platforms-operator.md#authenticated-prometheus-scrape)
for token-file ownership and permissions. Grafana's full-stack configuration
requires login; a fresh Grafana volume uses its default `admin` / `admin` login.

The dashboard includes scrape health, firing Nanoflare alerts, runner sample
freshness, daemon log-forwarder drops, request rate, HTTP 5xx percentage,
Traefik p50/p95/p99 latency, the ten busiest routes, runner memory and PIDs,
OOM kills, gateway errors, and recent error logs from Loki.

Traffic panels require requests to a matched Worker route and at least two
Prometheus scrapes (15 seconds apart). The generated Worker routers use TLS;
use port 8443 with the full container stack. Unmatched HTTP 404 responses do
not create per-router metrics. A zero-traffic window has no error percentage
or latency. The table's 5xx count and percentage cover the selected dashboard
time range; its request rate and latency use the recent rate window.

Runner limits appear only when a finite cgroup bound is reported. An unbounded
local runner can trigger `NanoflareRunnerMissingBounds`; resource sample age
shows Unavailable when the runner's measurements are missing or stale.

Scrape health checks only Traefik and nanoflared. It does not probe the database,
object store, or log pipeline. The drop counter covers the daemon forwarder;
inspect delivered Worker logs to check the complete logging path.

The **Log services (regex)** filter defaults to
`docker-(nanoflared|nanoflare-runner|traefik)-.+|host-runtime`, matching the
daemon, runner, proxy and attributed Worker events in the default Compose
project. Change it for a custom Compose project, or use `.+` to include every
collected service. Expand
a log to see Worker and deployment labels when available.

Edit the provisioned JSON in source control to keep dashboard changes across
container recreation. Grafana polls the provisioning directory for updates.
