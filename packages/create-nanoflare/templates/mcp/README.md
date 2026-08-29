# MCP Worker

This Worker exposes a public MCP endpoint at `/mcp` with an `echo` tool.

Install dependencies, create the Worker, and deploy it:

```sh
npm install
npx nanoflare create
npm run deploy
```

Open the deployed Worker hostname. The root response reports the full MCP URL:

```text
https://<worker-hostname>/mcp
```

Connect an MCP v2-compatible client and call `echo` with:

```json
{ "message": "Hello from MCP" }
```

The tool returns the same text. This template intentionally leaves `/mcp`
public; use the `oauth-mcp` template when the endpoint needs authorization.
