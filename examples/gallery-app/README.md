# gallery-app

`gallery-app` is a small browser-based example that makes Nanoflare storage
visible right away.

## What It Demonstrates

- static assets served from `public/` through `ASSETS`
- uploaded image files stored in `OBJECTS`
- gallery metadata stored in `GALLERY_KV`
- a Worker-first asset setup where `/api/*` stays dynamic and `/` stays static

## Setup

From this directory:

```sh
npm install
npm run build
nanoflare create
nanoflare kv namespace create gallery-metadata
nanoflare object-storage bucket create gallery-images
```

Update [nanoflare.json](nanoflare.json) so the KV namespace id and object
storage bucket id match what the CLI returned, then deploy:

```sh
nanoflare deploy
```

## Routes To Try

- `/` serves the gallery UI
- `GET /api/gallery` returns the saved image metadata list
- `POST /api/gallery` accepts a multipart upload with an `image` file field
- `GET /api/gallery/:id` streams a stored image back from object storage
