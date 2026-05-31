export default {
  async fetch(request, env) {
    const identity = request.headers.get("x-platform-context");
    const visits = Number((await env.KV.get("visits")) ?? "0") + 1;
    await env.KV.put("visits", String(visits));
    return Response.json({
      message: "hello from platform",
      visits,
      identity: identity ? JSON.parse(identity) : null,
    });
  },
};
