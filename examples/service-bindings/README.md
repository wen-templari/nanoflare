# Service bindings

This TypeScript example deploys two Workers in the same Nanoflare organization.
The public `api-worker` has a private Cloudflare-style service binding to
`identity-worker`:

```json
{
  "services": [{ "binding": "IDENTITY", "service": "identity-worker" }]
}
```

It demonstrates both interfaces provided by a service binding:

- RPC: `await env.IDENTITY.getUser(userID)`
- HTTP: `await env.IDENTITY.fetch(request)`

The identity Worker is private: it does not need a public route for the API
Worker to call it. During the API build, `nanoflare types` reads both local
configuration files and generates a typed `IDENTITY` binding. Its RPC methods
are inferred from the identity Worker's compiled entrypoint and always return
promises.

## Install and check

Install each example package, then build the identity target before checking the
API Worker (the generated API declaration imports the target's built entrypoint).

```sh
cd identity-worker
npm install
npm run build

cd ../api-worker
npm install
npm run check
```

## Deploy

Deploy the target before the caller. Both commands must use credentials with
the same active organization.

```sh
cd identity-worker
npm run build
nanoflare create
npm run deploy

cd ../api-worker
npm run build
nanoflare create
npm run deploy
```

## Routes

Use the public hostname displayed by `nanoflare create` for `api-worker`.

- `/rpc?user=ada` calls `WorkerEntrypoint.getUser()` over RPC.
- `/http?user=ada` forwards an HTTP request to the same private Worker.

Both routes return the same JSON user record. The RPC method is asynchronous at
the caller, so it must be awaited even though the service method itself is
synchronous.
