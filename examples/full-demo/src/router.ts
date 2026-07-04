import type { KVNamespace, NanoflareEnv } from "@nanoflare/workers-types";

interface FullDemoEnv extends Omit<NanoflareEnv, "KV"> {
  VISITS_KV: KVNamespace;
}

interface UploadMetadata {
  key: string;
  size: number;
  uploaded: string;
  contentType: string;
}

export async function routeRequest(request: Request, env: FullDemoEnv): Promise<Response> {
  const url = new URL(request.url);
  if (url.pathname === "/preview/auth") {
    const forwardedJwt = request.headers.get("x-nanoflare-user-jwt");
    const forwardedEmail = request.headers.get("x-nanoflare-user-email");
    return Response.json({
      authed: Boolean(forwardedJwt && forwardedEmail),
      email: forwardedEmail,
      jwt: forwardedJwt,
      path: url.pathname,
    });
  }
  if (url.pathname === "/api/visits") {
    const visits = Number((await env.VISITS_KV.get("visits")) ?? "0") + 1;
    await env.VISITS_KV.put("visits", String(visits));
    return Response.json({ visits });
  }
  if (url.pathname === "/api/files/latest.txt" && request.method === "PUT") {
    const contentType = request.headers.get("content-type") || "text/plain; charset=utf-8";
    const body = await request.arrayBuffer();
    await env.OBJECTS.put("uploads/latest.txt", body, {
      httpMetadata: { contentType },
    });
    const uploaded: UploadMetadata = {
      key: "uploads/latest.txt",
      size: body.byteLength,
      uploaded: new Date().toISOString(),
      contentType,
    };
    await env.VISITS_KV.put("uploads:latest.txt:meta", JSON.stringify(uploaded));
    return Response.json({
      ok: true,
      key: uploaded.key,
      size: uploaded.size,
      uploaded: uploaded.uploaded,
    });
  }
  if (url.pathname === "/api/files/latest.txt" && request.method === "GET") {
    const file = await env.OBJECTS.get("uploads/latest.txt");
    if (!file) {
      return new Response("Not found", { status: 404 });
    }
    const meta = await env.VISITS_KV.get<UploadMetadata>("uploads:latest.txt:meta", "json");
    return new Response(file.body, {
      headers: {
        "content-type": meta?.contentType || file.httpMetadata.contentType || "text/plain; charset=utf-8",
        etag: file.httpEtag || file.etag,
      },
    });
  }
  if (url.pathname === "/preview/logo.svg") {
    return env.ASSETS.fetch("/logo.svg");
  }
  return new Response("Not found", { status: 404 });
}
