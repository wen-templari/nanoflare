# `@nanoflare/schema`

This package checks in the Nanoflare control-plane OpenAPI document and the TypeScript types generated from it.

Run `pnpm schema:generate` from the repository root after changing an API route or its request/response type. Consumers import `paths` from `@nanoflare/schema` and use it with `openapi-fetch`.
