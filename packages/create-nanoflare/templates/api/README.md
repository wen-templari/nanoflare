# Documented API

Run `npm run deploy` to provision the Nanoflare database automatically, then run
`nanoflare db migrations apply DATABASE_ID` with its Nanoflare database ID.
For an existing database, set `nanoflare.resource_ids.DB` in `wrangler.jsonc`.
The placeholder Cloudflare `database_id` is only for local development.
Visit `/docs` for the OpenAPI document link.
