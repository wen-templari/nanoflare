# create-nanoflare

Create a Nanoflare Worker:

```sh
npm create nanoflare@latest my-worker
```

The initializer writes a TypeScript-first Worker project and `nanoflare.json`.
It does not install dependencies or contact a Nanoflare server.

```sh
cd my-worker
nanoflare create
nanoflare deploy
```

Use `-- --template <name>` to select `starter`, `bindings`, `pages`, `spa`,
`ssr`, `api`, `mcp`, or `oauth-mcp`. Resource templates mark the IDs to replace
in `nanoflare.json`; `api` includes an initial SQL migration and OpenAPI
endpoint. `mcp` creates a public MCP endpoint, while `oauth-mcp` adds OAuth 2.1
with PKCE and KV-backed authorization state.
