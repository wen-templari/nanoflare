import type { NanoflareEnv } from "@nanoflare/workers-types";

export default {
  async fetch(request: Request, env: NanoflareEnv): Promise<Response> {
    const url = new URL(request.url);

    if (url.pathname === "/") {
      return Response.json({
        ok: true,
        message: "public route",
        hint: "Request /api/auth/me with a valid authenticated session or bearer token.",
      });
    }

    if (url.pathname === "/api/auth/me") {
      return Response.json({
        ok: true,
        path: url.pathname,
        authed: Boolean(env.IDENTITY.get(request)),
        identity: env.IDENTITY.get(request),
        headers: env.IDENTITY.headers(request),
      });
    }

    return new Response("Not found", { status: 404 });
  },
};
