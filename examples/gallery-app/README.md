# gallery-app

`gallery-app` is a gallery app using nanoflare Workers, db, and Object Storage.

## What It Demonstrates

- a Vite-powered React SPA under `src/`
- a dedicated Worker entrypoint under `worker/`
- static assets built into `dist/client/` and served through `ASSETS`
- uploaded image files stored in `OBJECTS`
- gallery metadata and preview counts stored in `GALLERY_DB`
- a bundled Worker artifact at `dist/worker.js`
- a Worker-first asset setup where `/api/*` stays dynamic and `/` serves the SPA

## Setup

From this directory:

```sh
npm install
nanoflare create
nanoflare db create gallery-metadata
nanoflare object-storage bucket create gallery-images
npm run build
```

Update [nanoflare.json](nanoflare.json) so the database id and object storage
bucket id match what the CLI returned, then deploy:

```sh
nanoflare deploy
```

`npm run build` now bundles both the React client and the Worker. The deploy
artifacts are written to `dist/`, so rerun the build after changing either the
frontend or the Worker. TypeScript validation is split between
`tsconfig.app.json` for the UI and `tsconfig.worker.json` for the Worker.

## Project Layout

- `src/main.tsx`, `src/App.tsx`, and `src/styles.css` drive the Vite React UI
- `worker/index.ts` contains the gallery API and asset-serving Worker
- `tsconfig.app.json` checks the browser UI source
- `tsconfig.worker.json` checks the Worker source

## Routes To Try

- `/` serves the gallery UI
- `GET /api/gallery` returns the saved image metadata list
- `POST /api/gallery` accepts a multipart upload with an `image` file field
- `POST /api/gallery/:id/preview` increments and returns that image's preview count
- `GET /api/gallery/:id` streams a stored image back from object storage
- `DELETE /api/gallery/:id` removes the image from object storage and db
