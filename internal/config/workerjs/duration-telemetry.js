export default {
  async tail(events, env) {
    await env.COLLECTOR.fetch("http://nanoflare.internal/internal/runtime/logs", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(events),
    });
  },
};
