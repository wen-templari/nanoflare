# OAuth MCP calculator

This example runs a calculator MCP server as a Nanoflare Worker. It uses:

- [`@cloudflare/workers-oauth-provider`](https://github.com/cloudflare/workers-oauth-provider) for OAuth 2.1, PKCE, token validation, authorization-server metadata, protected-resource metadata, Client ID Metadata Documents, and Dynamic Client Registration
- [`@modelcontextprotocol/server`](https://github.com/modelcontextprotocol/typescript-sdk) v2 for the MCP 2026-07-28 HTTP handler
- a Nanoflare KV namespace to persist OAuth clients, grants, and tokens

The MCP endpoint is `/mcp` and exposes one tool, `add`, which accepts numeric `a`
and `b` arguments.

## Setup

From this directory:

```sh
npm install
npx nanoflare create
npx nanoflare kv namespace create oauth-mcp-calculator
```

Replace `replace-with-oauth-kv-namespace-id` in
[`nanoflare.json`](nanoflare.json) with the namespace id returned by the last
command, then deploy:

```sh
npm run deploy
```

Open the Worker hostname returned by Nanoflare. The root response reports the
full MCP URL to add to an MCP client:

```text
https://<worker-hostname>/mcp
```

The OAuth provider publishes its discovery documents automatically. An MCP
client first receives a bearer challenge from `/mcp`, discovers the provider,
registers or supplies its client metadata, performs Authorization Code + PKCE,
and returns to `/mcp` with an access token.

## Try the tool

Connect an MCP v2-compatible client to the Worker URL, approve the browser
consent page, then call `add` with:

```json
{ "a": 2, "b": 3 }
```

The tool returns `5`.

## Debug OAuth

The Worker writes structured lines prefixed with `[oauth-debug]` for OAuth
discovery, consent, authorization completion, token exchange, and authenticated
MCP requests. Follow the logs in the terminal running Nanoflare while completing
one authorization attempt. Each request also returns an `X-OAuth-Debug-Id`
header so a browser or Inspector network entry can be matched to the Worker log.

Tool calls add `tool.call_started` and `tool.call_completed` events. Response
stream timing is reported separately as `response.first_chunk` and
`response.body_completed`. If the tool completes quickly but the first chunk is
slow, the delay is in MCP response construction or transport. If both Worker
events are fast but Inspector renders slowly, the delay is client-side.

From this example directory, print the most recent OAuth diagnostics with:

```sh
npx nanoflare deployment output --search oauth-debug --limit 200
```

Add `--deployment <deployment-id>` to select a specific deployment. The deploy
command prints that ID.

The diagnostics intentionally omit authorization codes, access and refresh
tokens, PKCE verifiers, form bodies, and full client identifiers. A successful
flow should contain this sequence:

```text
authorize.consent_rendered
authorize.consent_submitted
authorize.completed
request.completed path=/authorize status=302
request.completed path=/oauth/token status=200
mcp.request
request.completed path=/mcp status=200
```

If authorization returns to the consent page, compare the debug IDs and look
for another `GET /authorize`. A new authorization request after a successful
`POST /oauth/token` indicates a client-side authorization loop; no token request
after `authorize.completed` indicates that the loopback callback was not
processed by the client.

The consent page's Content Security Policy permits form navigation to both the
Worker and the registered OAuth callback origin. Keeping `form-action` limited
to `'self'` blocks the cross-origin redirect to Inspector's loopback callback in
some browsers, even though the Worker correctly returns `302`.

## Important security note

This is deliberately a small OAuth mechanics example. The consent page uses a
fixed `Demo User`; it does not authenticate a real person. Before adapting it
for production, authenticate the user before consent, bind the authorization
request to that login session, and apply your own client and scope policy.

The example enables Client ID Metadata Documents and the
`global_fetch_strictly_public` compatibility flag. It also enables Dynamic
Client Registration for older MCP clients; remove `clientRegistrationEndpoint`
if all of your clients support pre-registration or Client ID Metadata Documents.
