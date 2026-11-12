# protected-app

`protected-app` is the smallest example focused on Nanoflare route protection.

## What It Demonstrates

- `auth.protected_routes` in `nanoflare.json`
- a public route and a protected route in the same Worker
- reading auth information from the forwarded auth headers
- returning those auth headers for inspection

## Setup

From this directory:

```sh
pnpm install
pnpm exec nanoflare create
pnpm run deploy
```

To exercise the protected route meaningfully, run `nanoflared` with OIDC or send
an accepted bearer token through Traefik ForwardAuth. Agents can validate ingress
access using `nanoflare workers request protected-app /api/auth/me` with
`workers:read` and `workers:invoke` permissions. This supplies a machine identity;
the example's user JWT/email fields remain empty for machine requests.

## Routes To Try

- `/` returns a public JSON response
- `/api/auth/me` is protected and returns `X-Nanoflare-User-JWT` and
  `X-Nanoflare-User-Email` from the request headers
