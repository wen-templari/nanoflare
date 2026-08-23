# k6 load testing

These scripts measure Nanoflare over a real HTTP connection. Use them alongside
the Go benchmarks: Go benchmarks catch code-level regressions, while k6 shows
latency, throughput, and errors under concurrent traffic.

## Target

Deploy a worker that supports:

- `GET /plain`
- `GET /kv-get`
- `GET /kv-put`
- optional database routes: `GET /db-read`, `GET /db-write`, and `GET /db-init`
- static assets such as `/`, `/assets/app.js`, and `/assets/logo.svg`
- optional object routes: `PUT /object/{key}`, `GET /object/{key}`,
  `DELETE /object/{key}`, and `GET /objects`

By default, k6 sends Worker traffic through Traefik, matching the production
request path. The local development Compose stack exposes Traefik at port 8088.

For Traefik (the default):

```sh
export HOSTNAME=<worker-hostname>
# BASE_URL defaults to http://127.0.0.1:8088
```

For the internal gateway, which is useful only for isolated gateway diagnosis:

```sh
export BASE_URL=http://127.0.0.1:8080
export ROUTE_VIA=internal
export WORKER_ID=<worker-id>
```

## Run

Install k6 locally, or run it through Docker.

Local:

```sh
k6 run scripts/k6/worker-load.js
```

If your shell uses a local proxy, bypass it for localhost load tests. Otherwise
k6 can measure the proxy or exhaust local sockets instead of measuring
Nanoflare:

```sh
env -u http_proxy -u https_proxy -u all_proxy \
  -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY \
  NO_PROXY=127.0.0.1,localhost \
  HOSTNAME="$HOSTNAME" \
  k6 run scripts/k6/worker-load.js
```

Docker:

```sh
docker run --rm -i \
  --add-host host.docker.internal:host-gateway \
  -e BASE_URL=http://host.docker.internal:8088 \
  -e HOSTNAME="$HOSTNAME" \
  -v "$PWD/scripts/k6:/scripts" \
  grafana/k6 run /scripts/worker-load.js
```

## Profiles

Smoke check:

```sh
PROFILE=smoke k6 run scripts/k6/worker-load.js
```

Step load:

```sh
PROFILE=step k6 run scripts/k6/worker-load.js
```

Sustained load:

```sh
PROFILE=sustained VUS=100 DURATION=10m k6 run scripts/k6/worker-load.js
```

The sustained profile starts all configured VUs immediately and holds them for
the full duration. Step and spike profiles continue to ramp through their
configured stages. Result summaries retain average, p90, p95, and p99 latency.

Spike:

```sh
PROFILE=spike k6 run scripts/k6/worker-load.js
```

## Traffic mix

Defaults to `mixed`, which sends about 70% `/plain`, 20% `/kv-get`, and 10%
`/kv-put`.

```sh
SCENARIO=plain k6 run scripts/k6/worker-load.js
SCENARIO=kv_read k6 run scripts/k6/worker-load.js
SCENARIO=kv_write k6 run scripts/k6/worker-load.js
SCENARIO=mixed k6 run scripts/k6/worker-load.js
SCENARIO=assets k6 run scripts/k6/worker-load.js
SCENARIO=object_read k6 run scripts/k6/worker-load.js
SCENARIO=object_write k6 run scripts/k6/worker-load.js
SCENARIO=objects k6 run scripts/k6/worker-load.js
SCENARIO=db_read k6 run scripts/k6/worker-load.js
SCENARIO=db_write k6 run scripts/k6/worker-load.js
SCENARIO=db_mixed k6 run scripts/k6/worker-load.js
SCENARIO=db_multi DATABASE_COUNT=5 k6 run scripts/k6/worker-load.js
SCENARIO=mixed_app k6 run scripts/k6/worker-load.js
SCENARIO=many_workers HOSTNAMES=worker-a.example.test,worker-b.example.test,worker-c.example.test k6 run scripts/k6/worker-load.js
```

For an internal-gateway-only run, use worker IDs instead:

```sh
SCENARIO=many_workers ROUTE_VIA=internal WORKER_IDS=worker-a,worker-b,worker-c \
  k6 run scripts/k6/worker-load.js
```

Useful knobs:

```sh
PROFILE=sustained VUS=250 DURATION=15m SCENARIO=mixed k6 run scripts/k6/worker-load.js
```

### Suggested matrix

Run these in order while watching `http_req_failed`, p95/p99 latency, CPU,
memory, open connections, Postgres connections, and workerd process churn.

```sh
SCENARIO=plain PROFILE=sustained VUS=50 DURATION=2m THINK_TIME=0.01 k6 run scripts/k6/worker-load.js

SCENARIO=kv_read  PROFILE=sustained VUS=25 DURATION=2m THINK_TIME=0.01 k6 run scripts/k6/worker-load.js
SCENARIO=kv_write PROFILE=sustained VUS=25 DURATION=2m THINK_TIME=0.01 k6 run scripts/k6/worker-load.js
SCENARIO=mixed    PROFILE=sustained VUS=25 DURATION=2m THINK_TIME=0.01 k6 run scripts/k6/worker-load.js

SCENARIO=assets    PROFILE=sustained VUS=25 DURATION=2m THINK_TIME=0.01 k6 run scripts/k6/worker-load.js
SCENARIO=objects   PROFILE=sustained VUS=25 DURATION=2m THINK_TIME=0.01 k6 run scripts/k6/worker-load.js
SCENARIO=db_read   PROFILE=sustained VUS=25 DURATION=2m THINK_TIME=0.01 k6 run scripts/k6/worker-load.js
SCENARIO=db_write  PROFILE=sustained VUS=25 DURATION=2m THINK_TIME=0.01 k6 run scripts/k6/worker-load.js
SCENARIO=db_mixed  PROFILE=sustained VUS=25 DURATION=2m THINK_TIME=0.01 k6 run scripts/k6/worker-load.js
SCENARIO=db_multi  DATABASE_COUNT=5 PROFILE=sustained VUS=25 DURATION=2m THINK_TIME=0.01 k6 run scripts/k6/worker-load.js
SCENARIO=mixed_app PROFILE=sustained VUS=25 DURATION=2m THINK_TIME=0.01 k6 run scripts/k6/worker-load.js

SCENARIO=many_workers HOSTNAMES=worker-a.example.test,worker-b.example.test,worker-c.example.test,worker-d.example.test,worker-e.example.test \
  PROFILE=sustained VUS=50 DURATION=2m THINK_TIME=0.01 \
  k6 run scripts/k6/worker-load.js
```

For cold-start / idle-worker latency, set a short runtime idle timeout, wait for
the worker to shut down, then run the plain scenario against `/plain`.

For a long soak, run 15-60 minutes at a known-safe load, usually 70-80% of the
first failure boundary:

```sh
SCENARIO=mixed_app PROFILE=sustained VUS=80 DURATION=30m THINK_TIME=0.01 k6 run scripts/k6/worker-load.js
```

### Database setup

The load-test Worker has optional D1-style database routes. Create a database,
add its returned ID to the Worker's `db` binding, and deploy before running a
database scenario:

```sh
nanoflare db create load-test-db
```

```json
"db": [{ "binding": "DB", "database_id": "<database-id>" }]
```

`db_read` selects a row count, `db_write` inserts a small unique row, and
`db_mixed` uses a 70/30 read/write split. The script initializes its table once
in `setup`; it does not delete rows, so run it against a dedicated load-test
database.

`db_multi` distributes that same 70/30 mix round-robin across the configured
database bindings. Set `DATABASE_COUNT` to the number of bindings; the example
load-test Worker uses `DB` plus `DB_2` through `DB_5` for five databases.

### Control-plane lifecycle tests

Run control-plane traffic separately from Worker capacity tests. This prevents
control requests from distorting Worker latency and makes lifecycle effects
visible in their own metrics:

```sh
API_TOKEN=<token> ORG_ID=<org-id> PROFILE=sustained VUS=10 DURATION=2m \
  k6 run scripts/k6/control-plane-lifecycle.js
```

The default write operation creates and immediately deletes a uniquely named KV
namespace, so repeated runs do not accumulate namespaces. Deployment writes are
intentionally off by default because each deploy changes the Worker's active
version and the API has no matching deployment-delete endpoint. Only enable
them for an isolated Worker:

```sh
API_TOKEN=<token> ORG_ID=<org-id> WORKER_ID=<isolated-worker-id> ALLOW_DEPLOYS=1 \
  k6 run scripts/k6/control-plane-lifecycle.js
```

### Scenario knobs

- `ASSET_PATHS=/,/assets/app.js,/assets/logo.svg,/assets/image.svg` changes the
  asset mix.
- `SCENARIO=many_workers` distributes the normal Worker traffic mix round-robin
  across at least two targets. It seeds KV and object data on each target before
  load begins. `multi_worker` remains a compatible alias. When console object
  storage is enabled, provide matching `WORKER_IDS` and `HOSTNAMES` lists.
- `HOSTNAMES=host-a,host-b` spreads Traefik traffic across Worker hostnames.
- `ROUTE_VIA=internal WORKER_IDS=id-a,id-b,id-c` sends traffic through the
  internal gateway rather than Traefik. This bypasses ingress routing and its
  metrics, so use it only for gateway-specific diagnosis.
- `CONTROL_BASE_URL=http://127.0.0.1:8080` sets the direct control-plane URL
  for `control-plane-lifecycle.js`.
- `OBJECT_BUCKET_ID=<bucket-id>` makes object tests use the console object
  routes at `/v1/workers/{worker}/object-storage-buckets/{bucket}`. Without it,
  object tests use the Worker routes under `/object/{key}`.
- `API_TOKEN=<token>` adds a bearer token for control-plane and console object
  routes.

Watch `http_req_failed`, p95/p99 latency, CPU, memory, and workerd restarts.
The first profile where errors rise or p95/p99 latency becomes unacceptable is
your practical capacity boundary for that machine and deployment shape.
