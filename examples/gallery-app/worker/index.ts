import type { AssetFetcher, D1Database, ObjectStorageBucket } from "@nanoflare/workers-types";
import mime from "mime";

interface GalleryItem {
  id: string;
  key: string;
  filename: string;
  contentType: string;
  uploadedAt: string;
  size: number;
  previewCount: number;
}

interface GalleryRow {
  id: string;
  object_key: string;
  filename: string;
  content_type: string;
  uploaded_at: string;
  size: number;
  preview_count: number;
}

interface GalleryEnv {
  ASSETS: AssetFetcher;
  GALLERY_DB: D1Database;
  OBJECTS: ObjectStorageBucket;
}

const MAX_ITEMS = 24;

export default {
  async fetch(request: Request, env: GalleryEnv): Promise<Response> {
    const url = new URL(request.url);

    if (url.pathname.startsWith("/api/")) {
      await ensureGallerySchema(env);
    }

    if (request.method === "GET" && url.pathname === "/api/gallery") {
      return Response.json({ items: await readGalleryItems(env) });
    }

    if (request.method === "POST" && url.pathname === "/api/gallery") {
      return uploadImage(request, env);
    }

    if (request.method === "POST" && url.pathname.startsWith("/api/gallery/") && url.pathname.endsWith("/preview")) {
      return trackPreview(url.pathname, env);
    }

    if (request.method === "DELETE" && url.pathname.startsWith("/api/gallery/")) {
      return deleteImage(url.pathname, env);
    }

    if (request.method === "GET" && url.pathname.startsWith("/api/gallery/")) {
      return serveImage(url.pathname, env);
    }

    return env.ASSETS.fetch(request);
  },
};

async function uploadImage(request: Request, env: GalleryEnv): Promise<Response> {
  const form = await request.formData();
  const uploaded = form.get("image");
  if (!(uploaded instanceof File)) {
    return Response.json({ ok: false, error: "image file is required" }, { status: 400 });
  }

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

  await env.GALLERY_DB.prepare(`
    INSERT INTO gallery_items (
      id,
      object_key,
      filename,
      content_type,
      uploaded_at,
      size,
      preview_count
    ) VALUES (?, ?, ?, ?, ?, ?, ?)
  `)
    .bind(item.id, item.key, item.filename, item.contentType, item.uploadedAt, item.size, item.previewCount)
    .run();

  return Response.json({ ok: true, item }, { status: 201 });
}

async function serveImage(pathname: string, env: GalleryEnv): Promise<Response> {
  const id = pathname.slice("/api/gallery/".length);
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

async function trackPreview(pathname: string, env: GalleryEnv): Promise<Response> {
  const id = pathname
    .slice("/api/gallery/".length)
    .replace(/\/preview$/, "");

  if (!id) {
    return new Response("Not found", { status: 404 });
  }

  await env.GALLERY_DB.prepare(`
    UPDATE gallery_items
    SET preview_count = preview_count + 1
    WHERE id = ?
  `)
    .bind(id)
    .run();

  const item = await readGalleryItem(env, id);
  if (!item) {
    return Response.json({ ok: false, error: "Image not found" }, { status: 404 });
  }

  return Response.json({ ok: true, item });
}

async function deleteImage(pathname: string, env: GalleryEnv): Promise<Response> {
  const id = pathname.slice("/api/gallery/".length);
  if (!id) {
    return new Response("Not found", { status: 404 });
  }

  const item = await readGalleryItem(env, id);
  if (!item) {
    return Response.json({ ok: false, error: "Image not found" }, { status: 404 });
  }

  await env.OBJECTS.delete(item.key);
  await env.GALLERY_DB.prepare("DELETE FROM gallery_items WHERE id = ?").bind(id).run();

  return Response.json({ ok: true, id });
}

async function ensureGallerySchema(env: GalleryEnv): Promise<void> {
  await env.GALLERY_DB.exec(`
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
  `);
}

async function readGalleryItems(env: GalleryEnv): Promise<GalleryItem[]> {
  const result = await env.GALLERY_DB.prepare(`
    SELECT
      id,
      object_key,
      filename,
      content_type,
      uploaded_at,
      size,
      preview_count
    FROM gallery_items
    ORDER BY uploaded_at DESC
    LIMIT ?
  `)
    .bind(MAX_ITEMS)
    .all<GalleryRow>();

  return result.results.map(rowToGalleryItem);
}

async function readGalleryItem(env: GalleryEnv, id: string): Promise<GalleryItem | null> {
  const row = await env.GALLERY_DB.prepare(`
    SELECT
      id,
      object_key,
      filename,
      content_type,
      uploaded_at,
      size,
      preview_count
    FROM gallery_items
    WHERE id = ?
  `)
    .bind(id)
    .first<GalleryRow>();

  return row ? rowToGalleryItem(row) : null;
}

function rowToGalleryItem(row: GalleryRow): GalleryItem {
  return {
    id: row.id,
    key: row.object_key,
    filename: row.filename,
    contentType: row.content_type,
    uploadedAt: row.uploaded_at,
    size: Number(row.size),
    previewCount: Number(row.preview_count ?? 0),
  };
}
