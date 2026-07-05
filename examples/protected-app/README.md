# protected-app

`protected-app` is the smallest example focused on Nanoflare route protection.

## What It Demonstrates

- `auth.protected_routes` in `nanoflare.json`
- a public route and a protected route in the same Worker
- reading auth information through `env.IDENTITY`
- returning the forwarded auth headers for inspection

## Setup

From this directory:

```sh
npm install
npm run build
nanoflare create
nanoflare deploy
```

To exercise the protected route meaningfully, run `nanoflared` with OIDC or send
an accepted bearer token through Traefik ForwardAuth.

## Routes To Try

- `/` returns a public JSON response
- `/api/auth/me` is protected and returns the resolved identity plus forwarded auth headers
