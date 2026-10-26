# Bindings Worker

Run `npm run dev` for local KV and object-storage emulation. Run `npm run deploy`
to provision independent Nanoflare resources automatically. To bind existing
Nanoflare resources, set `nanoflare.resource_ids.KV` and
`nanoflare.resource_ids.OBJECTS` in `wrangler.jsonc`. Cloudflare resource IDs
are separate and do not identify Nanoflare resources.

`GET` and `PUT /kv/:key` use KV; `GET` and `PUT /objects/:key` use object storage.
