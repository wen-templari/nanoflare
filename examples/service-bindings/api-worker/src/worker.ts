export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    const user = url.searchParams.get("user") || "guest";

    if (url.pathname === "/rpc") {
      const identity = await env.IDENTITY.getUser(user);
      return Response.json({ transport: "rpc", identity });
    }

    if (url.pathname === "/http") {
      const upstream = await env.IDENTITY.fetch(
        new Request(`https://identity.internal/users?user=${encodeURIComponent(user)}`),
      );
      return Response.json({ transport: "http", identity: await upstream.json() });
    }

    return Response.json({
      routes: ["/rpc?user=ada", "/http?user=ada"],
    });
  },
};
