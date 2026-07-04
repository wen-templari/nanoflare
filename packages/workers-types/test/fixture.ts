import type { NanoflareEnv } from "..";

async function exerciseBindings(env: NanoflareEnv, request: Request): Promise<Response> {
  await env.KV.put("greeting", "hello");
  const count = await env.KV.get("counter", "arrayBuffer");
  const payload = await env.KV.get<{ ok: boolean }>("payload", "json");
  await env.KV.delete("stale");

  const asset = await env.ASSETS.fetch("/index.html");
  const uploaded = await env.OBJECTS.put("folder/demo.json", JSON.stringify({ ok: true }), {
    httpMetadata: { contentType: "application/json" },
  });
  const body = await env.OBJECTS.get(uploaded.key);
  const head = await env.OBJECTS.head(uploaded.key);
  await env.OBJECTS.delete(uploaded.key);

  const identity = env.IDENTITY.get(request);
  const headers = env.IDENTITY.headers(request);

  return Response.json({
    count: count ? count.byteLength : 0,
    payload,
    asset: asset.status,
    bodyUsed: body?.bodyUsed ?? false,
    head: head?.key ?? null,
    identity,
    headers,
  });
}

void exerciseBindings;
