# load-test-worker

Minimal Worker target for k6 load tests.

## Setup

Start `nanoflared`, then create a KV namespace:

```sh
nanoflare kv namespace create load-test-kv
```

Update `nanoflare.json` so `kv_namespaces[0].id` matches the returned namespace
id.

For object storage tests, create a bucket and add it to `nanoflare.json`:

```sh
nanoflare object-storage bucket create load-test-objects
```

```json
"object_storage_buckets": [
  {
    "binding": "OBJECTS",
    "bucket_id": "<bucket-id>"
  }
]
```

Then register and deploy:

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
- `GET /`, `/assets/app.js`, `/assets/logo.svg`, and `/assets/image.svg`
  serve static assets from `public/`.
- `PUT /object/{key}` writes an object when `OBJECTS` is configured.
- `GET /object/{key}` reads an object when `OBJECTS` is configured.
- `DELETE /object/{key}` deletes an object when `OBJECTS` is configured.
- `GET /objects` returns a small JSON response for object-list traffic.

## k6 examples

```sh
SCENARIO=plain PROFILE=sustained VUS=50 DURATION=2m THINK_TIME=0.01 \
  BASE_URL=http://127.0.0.1:8080 WORKER_ID=<worker-id> \
  k6 run ../../scripts/k6/worker-load.js

SCENARIO=assets PROFILE=sustained VUS=25 DURATION=2m THINK_TIME=0.01 \
  BASE_URL=http://127.0.0.1:8080 WORKER_ID=<worker-id> \
  k6 run ../../scripts/k6/worker-load.js

SCENARIO=objects PROFILE=sustained VUS=25 DURATION=2m THINK_TIME=0.01 \
  BASE_URL=http://127.0.0.1:8080 WORKER_ID=<worker-id> \
  k6 run ../../scripts/k6/worker-load.js
```
