export default {
  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url)
    const authHeaders = {
      jwt: request.headers.get("X-Nanoflare-User-JWT"),
      email: request.headers.get("X-Nanoflare-User-Email"),
    }

    if (url.pathname === "/") {
      return Response.json({
        ok: true,
        message: "public route",
        hint: "Request /api/auth/me with a valid authenticated session or bearer token.",
      })
    }

    if (url.pathname === "/api/auth/me") {
      return Response.json({
        ok: true,
        path: url.pathname,
        authed: Boolean(authHeaders.jwt),
        headers: authHeaders,
      })
    }

    return new Response("Not found", { status: 404 })
  },
}
