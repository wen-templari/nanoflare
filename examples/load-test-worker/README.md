# load-test-worker

Minimal Worker target for k6 load tests.

## Setup

Start `nanoflared`, then create a KV namespace:

```sh
nanoflare kv namespace create load-test-kv
```

Update `nanoflare.json` so `kv_namespaces[0].id` matches the returned namespace
id, then register and deploy:

```sh
nanoflare create
nanoflare deploy
```

Use the worker id from `nanoflare.json` when running k6:

```sh
BASE_URL=http://127.0.0.1:8080 WORKER_ID=<worker-id> k6 run ../../scripts/k6/worker-load.js
```

## Routes

- `GET /plain` returns a small static response.
- `GET /kv-put` writes one KV key and returns `stored`.
- `GET /kv-get` reads the same KV key and returns its value.
