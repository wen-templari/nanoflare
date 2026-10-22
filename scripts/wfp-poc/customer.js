import { connect } from "cloudflare:sockets";

let rememberedCapability;
export default {
  async fetch(request, env, ctx) {
    const path = new URL(request.url).pathname;
    const operation = path.split("/").pop();
    const base = { version: env.VERSION, props: ctx.props };
    if (operation === "spin" || operation === "cpu-limit") {
      console.log("poc-spin-started");
      for (;;) {} // Parent harness kills this disposable process after a bounded observation.
    }
    if (operation === "context") {
      await new Promise((resolve) => setTimeout(resolve, 20));
      return Response.json({
        ...base,
        hidden: {
          registry: env.REGISTRY !== undefined,
          loader: env.LOADER !== undefined,
        },
      });
    }
    if (operation === "resource") {
      return new Response(
        env.STATE ? await (await env.STATE.fetch("http://state.invalid/value")).text() : "unbound",
      );
    }
    if (operation === "egress") {
      const allowed = await (await fetch("https://allowed.example/data")).json();
      const denied = await fetch("http://127.0.0.1/private");
      return Response.json({ allowed, denied: denied.status });
    }
    if (operation === "network") {
      let httpBlocked = false,
        tcpBlocked = false;
      let httpError, tcpError;
      const target = new URL(request.url).searchParams.get("target");
      try {
        await fetch(target);
      } catch (error) {
        httpBlocked = true;
        httpError = error.message;
      }
      try {
        const socket = connect(new URL(target).host);
        await socket.opened;
        await socket.close();
      } catch (error) {
        tcpBlocked = true;
        tcpError = error.message;
      }
      return Response.json({ httpBlocked, tcpBlocked, httpError, tcpError });
    }
    if (operation === "subrequests") {
      let completed = 0;
      for (let i = 0; i < 5; i++) {
        const response = await fetch("https://allowed.example/" + i);
        await response.text();
        completed++;
      }
      return Response.json({ completed });
    }
    if (operation === "capability") {
      const identity = await ctx.props.CONNECTOR.identify();
      rememberedCapability = ctx.props.CONNECTOR;
      return Response.json({ identity });
    }
    if (operation === "reuse-capability") {
      try {
        return Response.json({ identity: await rememberedCapability.identify() });
      } catch (error) {
        return Response.json({ rejected: true, error: error.message });
      }
    }
    if (operation === "stream") {
      const encoder = new TextEncoder();
      return new Response(
        new ReadableStream({
          start(controller) {
            controller.enqueue(encoder.encode(env.VERSION + ":first\n"));
            setTimeout(() => {
              controller.enqueue(encoder.encode(env.VERSION + ":last\n"));
              controller.close();
            }, 350);
          },
        }),
      );
    }
    if (operation === "cancel") {
      const encoder = new TextEncoder();
      let timer;
      return new Response(
        new ReadableStream({
          start(controller) {
            controller.enqueue(encoder.encode("first\n"));
            timer = setInterval(() => controller.enqueue(encoder.encode("next\n")), 50);
          },
          cancel() {
            clearInterval(timer);
            console.log("poc-stream-cancelled");
          },
        }),
      );
    }
    return Response.json(base);
  },
};
