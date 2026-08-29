# OAuth MCP Worker

This Worker exposes an MCP endpoint at `/mcp`, protected by OAuth 2.1 with PKCE.
It includes a browser consent page and an `echo` tool requiring the `echo:use`
scope. OAuth clients, grants, and tokens are stored in Nanoflare KV.

Install dependencies and create the Worker and KV namespace:

```sh
npm install
npx nanoflare create
npx nanoflare kv namespace create oauth-mcp
```

Replace `replace-with-oauth-kv-namespace-id` in `nanoflare.json` with the
namespace ID returned by the last command, then deploy:

```sh
npm run deploy
```

Open the deployed Worker hostname. The root response reports the full MCP URL:

```text
https://<worker-hostname>/mcp
```

Connect an MCP v2-compatible client, approve the browser consent page, and call
`echo` with:

```json
{ "message": "Hello from OAuth MCP" }
```

The provider publishes its authorization-server and protected-resource
metadata, supports Client ID Metadata Documents, and enables Dynamic Client
Registration for compatible clients.

## Security note

The consent page deliberately uses a fixed `Demo User` so the OAuth mechanics
work immediately. Before using this template in production, authenticate the
person granting access, bind the authorization request to that login session,
and apply your own client and scope policy.
