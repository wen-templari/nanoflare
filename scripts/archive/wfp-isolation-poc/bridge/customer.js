let instance;
let remembered;
export default {
  async fetch(request, env, ctx) {
    instance ??= crypto.randomUUID();
    const path = new URL(request.url).pathname;
    const resource = ctx.props.RESOURCE;
    if (path === "/remember") {
      remembered = resource;
      return Response.json(await resource.identify());
    }
    if (path === "/reuse") {
      try {
        return Response.json({ rejected: false, value: await remembered.identify() });
      } catch {
        return Response.json({ rejected: true });
      }
    }
    if (path === "/unsupported") {
      let rawFetchDenied = false,
        unknownRpcDenied = false;
      try {
        await fetch("http://broker/admin");
      } catch {
        rawFetchDenied = true;
      }
      try {
        await resource.admin();
      } catch {
        unknownRpcDenied = true;
      }
      const undeclaredResourceDenied =
        (await resource.fetch("http://synthetic/admin")).status === 403;
      return Response.json({ rawFetchDenied, unknownRpcDenied, undeclaredResourceDenied });
    }
    if (path === "/background") {
      ctx.waitUntil(
        (async () => {
          await new Promise((resolve) => setTimeout(resolve, 75));
          await resource.fetch("http://synthetic/ok");
          await new Promise((resolve) => setTimeout(resolve, 500));
          try {
            await resource.fetch("http://synthetic/ok");
          } catch {}
        })(),
      );
      return new Response("background-scheduled");
    }
    if (path === "/hold") {
      await new Promise((resolve) => setTimeout(resolve, 600));
      return new Response("held");
    }
    if (path === "/deadline") {
      await new Promise((resolve) => setTimeout(resolve, 1200));
      try {
        return await resource.fetch("http://synthetic/ok");
      } catch {
        return new Response("revoked");
      }
    }
    if (path === "/stream" || path === "/cancel") {
      const identity = await resource.identify();
      return new Response(
        new ReadableStream({
          start(controller) {
            controller.enqueue(new TextEncoder().encode(env.REVISION + ":first\n"));
            setTimeout(async () => {
              try {
                await resource.fetch("http://synthetic/ok");
                controller.enqueue(new TextEncoder().encode(identity.revision + ":last\n"));
                controller.close();
              } catch {
                try {
                  controller.error(new Error("revoked"));
                } catch {}
              }
            }, 350);
          },
        }),
      );
    }
    if (path === "/limit") {
      const statuses = [];
      for (let i = 0; i < 4; i++)
        statuses.push((await resource.fetch("http://synthetic/ok")).status);
      return Response.json({ statuses });
    }
    const identity = await resource.identify();
    // Forged producer headers/paths cannot select broker identity or authority.
    const checked = await resource.fetch(
      new Request("http://synthetic/ok", {
        headers: { "x-poc-lease": "forged", "x-poc-cell-key": "forged" },
      }),
    );
    return Response.json({
      instance,
      revision: env.REVISION,
      identity,
      checked: checked.status,
      leaseVisible: request.headers.has("x-poc-lease"),
    });
  },
};
