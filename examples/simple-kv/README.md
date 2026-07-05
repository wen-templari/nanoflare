# simple-kv

`simple-kv` is the smallest Nanoflare example that shows an explicit KV binding.

## What It Demonstrates

- a Worker using a named KV namespace binding, `COUNTER_KV`
- a `"hello world"` JSON response
- a persistent counter stored in KV

## Setup

From this directory:

```sh
npm install
npm run build
nanoflare create
nanoflare kv namespace create simple-kv-counter
```

Update [nanoflare.json](nanoflare.json) so `kv_namespaces[0].id` matches the
namespace id returned by the create command, then deploy:

```sh
nanoflare deploy
```

## Routes To Try

- `/` increments the counter and returns `{ message: "hello world", visits }`
- `/reset` deletes the counter and returns `{ ok: true }`
