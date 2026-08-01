import { desc, eq, sql } from "drizzle-orm";
import { drizzle } from "drizzle-orm/d1";
import { Hono } from "hono";
import { describeRoute, openAPIRouteHandler, resolver, validator } from "hono-openapi";
import mime from "mime";
import * as v from "valibot";

import { galleryItems } from "./db/schema";

interface GalleryItem {
  id: string;
  key: string;
  filename: string;
  contentType: string;
  uploadedAt: string;
  size: number;
  previewCount: number;
}

const MAX_ITEMS = 24;
type AppEnv = { Bindings: Env };

const app = new Hono<AppEnv>();
const galleryItemSchema = v.object({
  id: v.string(),
  key: v.string(),
  filename: v.string(),
  contentType: v.string(),
  uploadedAt: v.string(),
  size: v.number(),
  previewCount: v.number(),
});
const galleryResponseSchema = v.object({ items: v.array(galleryItemSchema) });
const uploadResponseSchema = v.object({ ok: v.boolean(), item: galleryItemSchema });
const deleteResponseSchema = v.object({ ok: v.boolean(), id: v.string() });
const errorResponseSchema = v.object({ ok: v.boolean(), error: v.string() });
const idParamSchema = v.object({ id: v.string() });
const imageUploadSchema = v.object({ image: v.file() });

app.use("/api/*", async (c, next) => {
  await ensureGallerySchema(c.env);
  await next();
});

app.get(
  "/api/gallery",
  describeRoute({
    tags: ["Gallery"],
    summary: "List gallery images",
    responses: {
      200: {
        description: "The most recently uploaded gallery images.",
        content: { "application/json": { schema: resolver(galleryResponseSchema) } },
      },
    },
  }),
  async (c) => c.json({ items: await readGalleryItems(c.env) }),
);

app.post(
  "/api/gallery",
  describeRoute({
    tags: ["Gallery"],
    summary: "Upload an image",
    responses: {
      201: {
        description: "The uploaded image metadata.",
        content: { "application/json": { schema: resolver(uploadResponseSchema) } },
      },
      400: {
        description: "The multipart request did not include an image.",
        content: { "application/json": { schema: resolver(errorResponseSchema) } },
      },
    },
  }),
  validator("form", imageUploadSchema),
  (c) => uploadImage(c.req.valid("form").image, c.env),
);

app.post(
  "/api/gallery/:id/preview",
  describeRoute({
    tags: ["Gallery"],
    summary: "Record an image preview",
    responses: {
      200: {
        description: "The image metadata with its updated preview count.",
        content: { "application/json": { schema: resolver(uploadResponseSchema) } },
      },
      404: {
        description: "The image does not exist.",
        content: { "application/json": { schema: resolver(errorResponseSchema) } },
      },
    },
  }),
  validator("param", idParamSchema),
  (c) => trackPreview(c.req.valid("param").id, c.env),
);

app.delete(
  "/api/gallery/:id",
  describeRoute({
    tags: ["Gallery"],
    summary: "Delete an image",
    responses: {
      200: {
        description: "The deleted image ID.",
        content: { "application/json": { schema: resolver(deleteResponseSchema) } },
      },
      404: {
        description: "The image does not exist.",
        content: { "application/json": { schema: resolver(errorResponseSchema) } },
      },
    },
  }),
  validator("param", idParamSchema),
  (c) => deleteImage(c.req.valid("param").id, c.env),
);

app.get(
  "/api/gallery/:id",
  describeRoute({
    tags: ["Gallery"],
    summary: "Get an uploaded image",
    responses: {
      200: { description: "The image bytes from object storage." },
      404: {
        description: "The image or stored object does not exist.",
        content: { "application/json": { schema: resolver(errorResponseSchema) } },
      },
    },
  }),
  validator("param", idParamSchema),
  (c) => serveImage(c.req.valid("param").id, c.env),
);

app.get(
  "/api/openapi.json",
  openAPIRouteHandler(app, {
    documentation: {
      info: {
        title: "Gallery API",
        version: "1.0.0",
        description: "Upload, retrieve, preview, and delete gallery images.",
      },
    },
  }),
);

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    if (new URL(request.url).pathname.startsWith("/api/")) {
      return app.fetch(request, env);
    }

    return env.ASSETS.fetch(request);
  },
};

async function uploadImage(uploaded: File, env: Env): Promise<Response> {
  const timestamp = Date.now().toString(36);
  const id = crypto.randomUUID().replace(/-/g, "");
  const contentType = mime.getType(uploaded.name) || uploaded.type || "application/octet-stream";
  const extension = mime.getExtension(contentType) || "bin";
  const key = `gallery/${timestamp}-${id}.${extension}`;
  const bytes = await uploaded.arrayBuffer();
  const item: GalleryItem = {
    id,
    key,
    filename: uploaded.name || `upload.${extension}`,
    contentType,
    uploadedAt: new Date().toISOString(),
    size: bytes.byteLength,
    previewCount: 0,
  };

  console.log("[gallery upload] received file", {
    name: uploaded.name,
    browserType: uploaded.type,
    inferredContentType: contentType,
    extension,
    key,
    size: bytes.byteLength,
  });

  await env.OBJECTS.put(key, bytes, {
    httpMetadata: { contentType },
  });

  const stored = await env.OBJECTS.head(key);
  console.log("[gallery upload] stored object metadata", {
    key,
    requestedContentType: contentType,
    storedContentType: stored?.httpMetadata.contentType ?? "",
    size: stored?.size ?? 0,
    etag: stored?.etag ?? "",
  });

  const db = drizzle(env.GALLERY_DB);
  await db.insert(galleryItems).values({
    id: item.id,
    objectKey: item.key,
    filename: item.filename,
    contentType: item.contentType,
    uploadedAt: item.uploadedAt,
    size: item.size,
    previewCount: item.previewCount,
  });

  return Response.json({ ok: true, item }, { status: 201 });
}

async function serveImage(id: string, env: Env): Promise<Response> {
  if (!id) {
    return new Response("Not found", { status: 404 });
  }

  const item = await readGalleryItem(env, id);
  if (!item) {
    return Response.json({ ok: false, error: "Image not found" }, { status: 404 });
  }

  const object = await env.OBJECTS.get(item.key);
  if (!object) {
    return Response.json({ ok: false, error: "Stored object missing" }, { status: 404 });
  }

  console.log("[gallery serve] object metadata", {
    id: item.id,
    key: item.key,
    indexContentType: item.contentType,
    objectContentType: object.httpMetadata.contentType,
    responseContentType: object.httpMetadata.contentType || item.contentType,
    size: object.size,
  });

  return new Response(object.body, {
    headers: {
      "content-type": object.httpMetadata.contentType || item.contentType,
      "cache-control": "public, max-age=3600",
      etag: object.httpEtag || object.etag,
    },
  });
}

async function trackPreview(id: string, env: Env): Promise<Response> {
  if (!id) {
    return new Response("Not found", { status: 404 });
  }

  const db = drizzle(env.GALLERY_DB);
  await db
    .update(galleryItems)
    .set({ previewCount: sql`${galleryItems.previewCount} + 1` })
    .where(eq(galleryItems.id, id));

  const item = await readGalleryItem(env, id);
  if (!item) {
    return Response.json({ ok: false, error: "Image not found" }, { status: 404 });
  }

  return Response.json({ ok: true, item });
}

async function deleteImage(id: string, env: Env): Promise<Response> {
  if (!id) {
    return new Response("Not found", { status: 404 });
  }

  const item = await readGalleryItem(env, id);
  if (!item) {
    return Response.json({ ok: false, error: "Image not found" }, { status: 404 });
  }

  await env.OBJECTS.delete(item.key);
  await drizzle(env.GALLERY_DB).delete(galleryItems).where(eq(galleryItems.id, id));

  return Response.json({ ok: true, id });
}

async function ensureGallerySchema(env: Env): Promise<void> {
  await drizzle(env.GALLERY_DB).run(
    sql.raw(`
    CREATE TABLE IF NOT EXISTS gallery_items (
      id text PRIMARY KEY,
      object_key text NOT NULL,
      filename text NOT NULL,
      content_type text NOT NULL,
      uploaded_at text NOT NULL,
      size integer NOT NULL,
      preview_count integer NOT NULL DEFAULT 0
    );
    CREATE INDEX IF NOT EXISTS gallery_items_uploaded_at_idx
      ON gallery_items (uploaded_at DESC);
  `),
  );
}

async function readGalleryItems(env: Env): Promise<GalleryItem[]> {
  const rows = await drizzle(env.GALLERY_DB)
    .select()
    .from(galleryItems)
    .orderBy(desc(galleryItems.uploadedAt))
    .limit(MAX_ITEMS);

  return rows.map(rowToGalleryItem);
}

async function readGalleryItem(env: Env, id: string): Promise<GalleryItem | null> {
  const [row] = await drizzle(env.GALLERY_DB)
    .select()
    .from(galleryItems)
    .where(eq(galleryItems.id, id));

  return row ? rowToGalleryItem(row) : null;
}

function rowToGalleryItem(row: typeof galleryItems.$inferSelect): GalleryItem {
  return {
    id: row.id,
    key: row.objectKey,
    filename: row.filename,
    contentType: row.contentType,
    uploadedAt: row.uploadedAt,
    size: Number(row.size),
    previewCount: Number(row.previewCount ?? 0),
  };
}
