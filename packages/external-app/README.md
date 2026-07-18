# Nanoflare External App

This package simulates an external platform that connects to Nanoflare through
OAuth and manages resources with the existing `/v1` API.

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

Register this external app in Nanoflare once, using a Nanoflare control-plane
token from `nanoflare auth login` or `/v1/auth/login`. The client registration
is owned by the organization in `X-Nanoflare-Org-ID`; users can still approve
that client for any Nanoflare organization they belong to:

```sh
curl -s -X POST http://127.0.0.1:8080/v1/oauth/clients \
  -H "Authorization: Bearer $NANOFLARE_TOKEN" \
  -H "X-Nanoflare-Org-ID: $NANOFLARE_OWNER_ORG_ID" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "External App UI",
    "redirect_uris": ["http://127.0.0.1:8787/oauth/callback"],
    "scopes": ["apps:read", "apps:write", "deployments:write", "kv:read", "kv:write"]
  }'
```

Then run the browser-based external app with the returned client credentials:

```sh
EXTERNAL_APP_CLIENT_ID=... \
EXTERNAL_APP_CLIENT_SECRET=... \
npm --prefix packages/external-app run dev
```

Open `http://127.0.0.1:8787`. The UI walks through the full user flow:

- click **Connect Nanoflare** in the external app;
- leave the external app and land on Nanoflare's authorization page;
- approve the connection with Nanoflare credentials;
- return to the external app callback;
- provision a worker using the OAuth access token stored by the external app;
- let the external app generate hostname and external ID internally;
- inspect token response metadata such as scopes and expiry;
- refresh or revoke the token.

The browser flow does not send Nanoflare user credentials to the external app.
The external app only stores its own OAuth client credentials and the tokens it
receives after the user approves access on Nanoflare.

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
EXTERNAL_APP_CLIENT_ID=...
EXTERNAL_APP_CLIENT_SECRET=...
EXTERNAL_APP_SCOPES="apps:write kv:write"
EXTERNAL_APP_PORT=8787
EXTERNAL_APP_ORIGIN=http://127.0.0.1:8787
NANOFLARE_UI_URL=http://127.0.0.1:5173
EXTERNAL_APP_REDIRECT_URI=https://external.example.com/oauth/callback
EXTERNAL_WORKER_HOSTNAME=external-managed.example.com
```
