import type { AssetFetcher, KVNamespace, ObjectStorageBucket } from "@nanoflare/workers-types"

interface Env {
  VISITS_KV: KVNamespace
  ASSETS: AssetFetcher
  OBJECTS: ObjectStorageBucket
}


async function routeRequest(request: Request, env: Env): Promise<Response> {
  const url = new URL(request.url)
  if (url.pathname === "/preview/auth") {
    const forwardedJwt = request.headers.get("x-nanoflare-user-jwt")
    const forwardedEmail = request.headers.get("x-nanoflare-user-email")
    return Response.json({
      authed: Boolean(forwardedJwt && forwardedEmail),
      email: forwardedEmail,
      jwt: forwardedJwt,
      path: url.pathname,
    })
  }
  if (url.pathname === "/api/visits") {
    const visits = Number((await env.VISITS_KV.get("visits")) ?? "0") + 1
    await env.VISITS_KV.put("visits", String(visits))
    return Response.json({ visits })
  }
  if (url.pathname === "/api/files/latest.txt" && request.method === "PUT") {
    const contentType = request.headers.get("content-type") || "text/plain; charset=utf-8"
    const body = await request.arrayBuffer()
    await env.OBJECTS.put("uploads/latest.txt", body, {
      httpMetadata: { contentType },
    })
    const uploaded = {
      key: "uploads/latest.txt",
      size: body.byteLength,
      uploaded: new Date().toISOString(),
    }
    return Response.json({
      ok: true,
      key: uploaded.key,
      size: uploaded.size,
      uploaded: uploaded.uploaded,
    })
  }
  if (url.pathname === "/api/files/latest.txt" && request.method === "GET") {
    try {
      const file = await env.OBJECTS.get("/api/files/latest.txt")
      if (!file) {
        return Response.json({ ok: false, error: "Not found" }, { status: 404 })
      }
      return new Response(file.body, {
        headers: {
          "content-type": file.httpMetadata.contentType || "text/plain; charset=utf-8",
          etag: file.httpEtag || file.etag,
        },
      })
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      return Response.json({
        ok: false,
        error: message,
      }, { status: 500 })
    }
  }
  if (url.pathname === "/preview/logo.svg") {
    return env.ASSETS.fetch("/logo.svg")
  }
  return new Response("Not found", { status: 404 })
}


export default {
  fetch(request: Request, env: Env): Promise<Response> {
    return routeRequest(request, env)
  },
}
