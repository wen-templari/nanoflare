import { connect } from "cloudflare:sockets";
import { readFileSync } from "node:fs";

export default {
  async fetch(request, env, ctx) {
    const path = new URL(request.url).pathname;
    if (path.startsWith("/budget-")) {
      const one = () => fetch("http://effect/ok");
      const statuses = [];
      if (path === "/budget-redirect") {
        const response = await fetch("http://effect/redirect/2");
        return Response.json({
          statuses: [response.status],
          leaseVisible: request.headers.has("x-poc-lease"),
        });
      }
      if (path === "/budget-mixed") {
        statuses.push((await one()).status);
        statuses.push(await env.RESOURCE.identify());
        statuses.push(
          (
            await env.RESOURCE.fetch("http://effect/resource", {
              headers: { "x-poc-kind": "resource" },
            })
          ).status,
        );
      } else if (path === "/budget-retry") {
        for (let i = 0; i < 3; i++) statuses.push((await fetch("http://effect/retry")).status);
      } else if (path === "/budget-parallel") {
        statuses.push(
          ...(await Promise.all(Array.from({ length: 6 }, async () => (await one()).status))),
        );
      } else if (path === "/budget-resource") {
        for (let i = 0; i < 3; i++)
          statuses.push(
            (
              await env.RESOURCE.fetch("http://effect/resource", {
                headers: { "x-poc-kind": "resource" },
              })
            ).status,
          );
      } else if (path === "/budget-rpc") {
        for (let i = 0; i < 3; i++) {
          try {
            statuses.push(await env.RESOURCE.identify());
          } catch {
            statuses.push(429);
          }
        }
      } else if (path === "/budget-background") {
        statuses.push((await one()).status);
        ctx.waitUntil(
          (async () => {
            await one();
            await one();
          })(),
        );
      } else {
        for (let i = 0; i < 3; i++) statuses.push((await one()).status);
      }
      return Response.json({ statuses, leaseVisible: request.headers.has("x-poc-lease") });
    }
    if (path === "/spin") {
      console.log("spin-started");
      for (;;) {}
    }
    if (path === "/memory") {
      console.log("memory-started");
      const retained = [];
      // Docker memory/swap cap and a parent deadline bound this disposable test.
      for (;;) {
        const bytes = new Uint8Array(8 * 1024 * 1024);
        bytes.fill(1);
        retained.push(bytes);
      }
    }
    if (path === "/background") {
      ctx.waitUntil(
        (async () => {
          await new Promise((resolve) => setTimeout(resolve, 100));
          console.log("background-spin-started");
          for (;;) {}
        })(),
      );
      return new Response("response-before-background");
    }
    if (path === "/sleep") {
      await new Promise((resolve) => setTimeout(resolve, 250));
      return new Response("slept");
    }
    if (path === "/boundary") {
      const files = {};
      for (const name of ["/fixture/host-secret", "/fixture/config.capnp", "/proc/self/environ"]) {
        try {
          files[name] = { read: true, value: readFileSync(name, "utf8") };
        } catch {
          files[name] = { read: false };
        }
      }
      let httpReached = false,
        tcpReached = false;
      try {
        httpReached = (await fetch("http://127.0.0.1:9090/")).ok;
      } catch {}
      try {
        const socket = connect("127.0.0.1:9090");
        await socket.opened;
        tcpReached = true;
        await socket.close();
      } catch {}
      return Response.json({
        files,
        httpReached,
        tcpReached,
        hiddenLoader: env.LOADER !== undefined,
      });
    }
    if (path === "/stream") {
      return new Response(
        new ReadableStream({
          start(controller) {
            controller.enqueue(new TextEncoder().encode("first\n"));
            setTimeout(() => {
              controller.enqueue(new TextEncoder().encode("last\n"));
              controller.close();
            }, 5000);
          },
        }),
      );
    }
    return new Response("customer-ready");
  },
};
