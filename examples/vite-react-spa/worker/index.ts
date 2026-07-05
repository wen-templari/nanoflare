import type { NanoflareEnv } from "@nanoflare/workers-types";

export default {
  async fetch(request: Request, env: NanoflareEnv): Promise<Response> {
    const url = new URL(request.url);

    if (url.pathname === "/api/hello") {
      return Response.json({
        message: "Hello from the Worker API",
        origin: url.origin,
        path: url.pathname,
      });
    }

    if (url.pathname === "/api/time") {
      return Response.json({
        isoTime: new Date().toISOString(),
        method: request.method,
        pathname: url.pathname,
      });
    }

    return env.ASSETS.fetch(request);
  },
};
