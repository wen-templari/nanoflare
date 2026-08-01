# Nanoflare External App

This package simulates an external platform that can automatically provision a
tenant organization through a partner integration, or connect an existing
organization through OAuth. Its browser UI talks only to this package's local
server; credentials remain server-side.

Start `nanoflared` first:

```sh
go run ./cmd/nanoflared \
  -addr :8080 \
  -config-dir ./var/generated \
  -base-hostname workers.example.test
```

Start the Nanoflare UI too:

```sh
npm --prefix packages/ui run dev
```

## Partner-managed connection UI

Create a partner integration once from the organization selected in your CLI
auth store:

```sh
./bin/nanoflare auth whoami
npm --prefix packages/external-app run setup-partner
```

The command reads the same OS-specific default location as `./bin/nanoflare`:
`~/Library/Application Support/nanoflare/auth.json` on macOS and
`~/.config/nanoflare/auth.json` on Linux. Set `NANOFLARE_AUTH_STORE` when the
CLI uses another store.
It prints `NANOFLARE_PARTNER_INTEGRATION_ID` and a one-time
`NANOFLARE_PARTNER_SECRET` to export into the external app server environment.

Equivalent API request:

```sh
curl -s -X POST http://127.0.0.1:8080/v1/organizations/$NANOFLARE_OWNER_ORG_ID/partner-integrations \
  -H "Authorization: Bearer $NANOFLARE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "External App UI",
    "allowed_scopes": ["workers:read", "workers:write", "deployments:write", "kv:read", "kv:write"]
  }'
```

Start the browser UI with the returned integration ID and one-time secret:

```sh
NANOFLARE_PARTNER_INTEGRATION_ID=... \
NANOFLARE_PARTNER_SECRET=... \
npm --prefix packages/external-app run dev
```

Open `http://127.0.0.1:8787`. The UI walks through the partner flow:

- enter an immutable external workspace ID and organization name;
- click **Connect workspace** to provision or reconnect the tenant organization;
- provision a worker using the tenant-scoped token stored by the external app;
- let the external app generate hostname and external ID internally;
- inspect token response metadata such as scopes and expiry;
- refresh the rotating token pair or disconnect the workspace.

The browser never receives the partner credential or refresh token. This demo
keeps its state in memory; use encrypted, tenant-keyed persistent storage in a
real external platform.

## Existing-organization OAuth

The same UI retains the previous **Connect existing organization** action. To
enable it, register an OAuth client and start the app with
`EXTERNAL_APP_CLIENT_ID` and `EXTERNAL_APP_CLIENT_SECRET`.

You can also run the original CLI smoke test:

```sh
npm --prefix packages/external-app run run
```

The script will:

- log in, or create the first Nanoflare user if setup has not run;
- register an OAuth client unless one is supplied through env vars;
- authorize the client for the active Nanoflare organization;
- exchange the authorization code for access and refresh tokens;
- create a worker through the OAuth access token;
- confirm missing scopes are rejected;
- refresh the token;
- revoke the refreshed access token and confirm it is rejected.

Useful environment variables:

```sh
NANOFLARE_URL=http://127.0.0.1:8080
NANOFLARE_EMAIL=external-admin@example.com
NANOFLARE_PASSWORD=secret
NANOFLARE_ORG_NAME="External App Test"
NANOFLARE_PARTNER_INTEGRATION_ID=...
NANOFLARE_PARTNER_SECRET=...
NANOFLARE_PARTNER_SCOPES="workers:read workers:write deployments:write secrets:write kv:read kv:write db:read db:write objects:read objects:write"
EXTERNAL_APP_CLIENT_ID=...
EXTERNAL_APP_CLIENT_SECRET=...
EXTERNAL_APP_SCOPES="workers:write kv:write"
EXTERNAL_APP_PORT=8787
EXTERNAL_APP_ORIGIN=http://127.0.0.1:8787
NANOFLARE_UI_URL=http://127.0.0.1:5173
EXTERNAL_APP_REDIRECT_URI=https://external.example.com/oauth/callback
EXTERNAL_WORKER_HOSTNAME=external-managed.example.com
```
