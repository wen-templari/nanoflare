export default {
  async fetch(request, env) {
    const path = new URL(request.url).pathname;

    if (path === "/kv-get") {
      return new Response((await env.KV.get("key")) ?? "");
    }

    if (path === "/kv-put") {
      await env.KV.put("key", "value");
      return new Response("stored");
    }

    return new Response("plain");
  },
};
