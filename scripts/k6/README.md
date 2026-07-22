# k6 load testing

These scripts measure Nanoflare over a real HTTP connection. Use them alongside
the Go benchmarks: Go benchmarks catch code-level regressions, while k6 shows
latency, throughput, and errors under concurrent traffic.

## Target

Deploy a worker that supports:

- `GET /plain`
- `GET /kv-get`
- `GET /kv-put`
- static assets such as `/`, `/assets/app.js`, and `/assets/logo.svg`
- optional object routes: `PUT /object/{key}`, `GET /object/{key}`,
  `DELETE /object/{key}`, and `GET /objects`

Then point k6 at either the Nanoflare internal worker gateway or your routed
worker URL.

For the internal gateway:

```sh
export BASE_URL=http://127.0.0.1:8080
export WORKER_ID=<worker-id>
```

For a routed worker URL:

```sh
export BASE_URL=http://127.0.0.1:8089
export HOSTNAME=<worker-hostname>
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
  BASE_URL=http://127.0.0.1:8080 \
  WORKER_ID="$WORKER_ID" \
  k6 run scripts/k6/worker-load.js
```

Docker:

```sh
docker run --rm -i \
  --add-host host.docker.internal:host-gateway \
  -e BASE_URL=http://host.docker.internal:8080 \
  -e WORKER_ID="$WORKER_ID" \
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
SCENARIO=mixed_app k6 run scripts/k6/worker-load.js
SCENARIO=multi_worker WORKER_IDS=worker-a,worker-b,worker-c k6 run scripts/k6/worker-load.js
SCENARIO=control_api k6 run scripts/k6/worker-load.js
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
SCENARIO=mixed_app PROFILE=sustained VUS=25 DURATION=2m THINK_TIME=0.01 k6 run scripts/k6/worker-load.js

SCENARIO=multi_worker WORKER_IDS=worker-a,worker-b,worker-c,worker-d,worker-e \
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

### Scenario knobs

- `ASSET_PATHS=/,/assets/app.js,/assets/logo.svg,/assets/image.svg` changes the
  asset mix.
- `WORKER_IDS=id-a,id-b,id-c` sends internal-gateway traffic across multiple
  workers. Use `HOSTNAMES=host-a,host-b` with routed worker URLs.
- `OBJECT_BUCKET_ID=<bucket-id>` makes object tests use the console object
  routes at `/v1/workers/{worker}/object-storage-buckets/{bucket}`. Without it,
  object tests use the Worker routes under `/object/{key}`.
- `API_TOKEN=<token>` adds a bearer token for control-plane and console object
  routes.
- `CONTROL_PATHS=/v1/workers,/v1/kv/namespaces,/metrics` overrides the read-only
  control-plane mix.
- `CONTROL_WRITES=1` adds namespace creation and deploy calls to the control API
  scenario. Set `CONTROL_DEPLOY_WORKER_ID` and optionally `CONTROL_DEPLOY_BODY`
  to control the deploy target and payload.

Watch `http_req_failed`, p95/p99 latency, CPU, memory, and workerd restarts.
The first profile where errors rise or p95/p99 latency becomes unacceptable is
your practical capacity boundary for that machine and deployment shape.
