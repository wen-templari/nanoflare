import type { KVNamespace } from "@nanoflare/workers-types"

interface Env {
  COUNTER_KV: KVNamespace
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url)

    if (url.pathname === "/reset") {
      await env.COUNTER_KV.delete("visits")
      return Response.json({ ok: true, visits: 0 })
    }

    const visits = Number((await env.COUNTER_KV.get("visits")) ?? "0") + 1
    await env.COUNTER_KV.put("visits", String(visits))

    return Response.json({
      message: "hello world",
      visits,
      pathname: url.pathname,
    })
  },
}
