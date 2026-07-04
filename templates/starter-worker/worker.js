export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const identity = request.headers.get("x-nanoflare-context");

    if (request.method === "PUT" && url.pathname === "/api/files/latest.txt") {
      const contentType = request.headers.get("content-type") || "text/plain; charset=utf-8";
      const uploaded = await env.OBJECTS.put("uploads/latest.txt", request, {
        httpMetadata: { contentType },
      });
      return Response.json({
        ok: true,
        key: uploaded.key,
        size: uploaded.size,
        uploaded: uploaded.uploaded,
      });
    }

    if (request.method === "GET" && url.pathname === "/api/files/latest.txt") {
      const file = await env.OBJECTS.get("uploads/latest.txt");
      if (!file) {
        return Response.json({ error: "file not found" }, { status: 404 });
      }
      return new Response(file.body, {
        headers: {
          "content-type": file.httpMetadata.contentType || "text/plain; charset=utf-8",
          etag: file.httpEtag || file.etag,
        },
      });
    }

    const visits = Number((await env.KV.get("visits")) ?? "0") + 1;
    await env.KV.put("visits", String(visits));
    await env.OBJECTS.put("visits/latest.json", JSON.stringify({ visits }), {
      httpMetadata: { contentType: "application/json" },
    });
    const latest = await env.OBJECTS.get("visits/latest.json");
    return Response.json({
      message: "hello from nanoflare",
      visits,
      latest: latest ? await latest.json() : null,
      identity: identity ? JSON.parse(identity) : null,
    });
  },
};
