import type { AssetFetcher, KVNamespace, ObjectStorageBucket } from "@nanoflare/workers-types";
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

interface GalleryEnv {
  ASSETS: AssetFetcher;
  GALLERY_KV: KVNamespace;
  OBJECTS: ObjectStorageBucket;
}

const GALLERY_INDEX_KEY = "gallery:index";
const MAX_ITEMS = 24;

export default {
  async fetch(request: Request, env: GalleryEnv): Promise<Response> {
    const url = new URL(request.url);

    if (request.method === "GET" && url.pathname === "/api/gallery") {
      return Response.json({ items: await readGalleryIndex(env) });
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

  await env.OBJECTS.put(key, bytes, {
    httpMetadata: { contentType },
  });

  const item: GalleryItem = {
    id,
    key,
    filename: uploaded.name || `upload.${extension}`,
    contentType,
    uploadedAt: new Date().toISOString(),
    size: bytes.byteLength,
    previewCount: 0,
  };

  const items = await readGalleryIndex(env);
  items.unshift(item);
  await env.GALLERY_KV.put(GALLERY_INDEX_KEY, JSON.stringify(items.slice(0, MAX_ITEMS)));

  return Response.json({ ok: true, item }, { status: 201 });
}

async function serveImage(pathname: string, env: GalleryEnv): Promise<Response> {
  const id = pathname.slice("/api/gallery/".length);
  if (!id) {
    return new Response("Not found", { status: 404 });
  }

  const items = await readGalleryIndex(env);
  const item = items.find((candidate) => candidate.id === id);
  if (!item) {
    return Response.json({ ok: false, error: "Image not found" }, { status: 404 });
  }

  const object = await env.OBJECTS.get(item.key);
  if (!object) {
    return Response.json({ ok: false, error: "Stored object missing" }, { status: 404 });
  }

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

  const items = await readGalleryIndex(env);
  const item = items.find((candidate) => candidate.id === id);
  if (!item) {
    return Response.json({ ok: false, error: "Image not found" }, { status: 404 });
  }

  const nextItem = {
    ...item,
    previewCount: item.previewCount + 1,
  };

  await writeGalleryIndex(
    env,
    items.map((candidate) => (candidate.id === id ? nextItem : candidate)),
  );

  return Response.json({ ok: true, item: nextItem });
}

async function deleteImage(pathname: string, env: GalleryEnv): Promise<Response> {
  const id = pathname.slice("/api/gallery/".length);
  if (!id) {
    return new Response("Not found", { status: 404 });
  }

  const items = await readGalleryIndex(env);
  const item = items.find((candidate) => candidate.id === id);
  if (!item) {
    return Response.json({ ok: false, error: "Image not found" }, { status: 404 });
  }

  await env.OBJECTS.delete(item.key);
  await writeGalleryIndex(
    env,
    items.filter((candidate) => candidate.id !== id),
  );

  return Response.json({ ok: true, id });
}

async function readGalleryIndex(env: GalleryEnv): Promise<GalleryItem[]> {
  const items = (await env.GALLERY_KV.get<GalleryItem[]>(GALLERY_INDEX_KEY, "json")) ?? [];
  return items.map((item) => ({
    ...item,
    previewCount: item.previewCount ?? 0,
  }));
}

async function writeGalleryIndex(env: GalleryEnv, items: GalleryItem[]): Promise<void> {
  await env.GALLERY_KV.put(GALLERY_INDEX_KEY, JSON.stringify(items.slice(0, MAX_ITEMS)));
}
