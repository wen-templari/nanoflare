import { Hono } from "hono";
const app = new Hono<{ Bindings: Env }>();
app.get("/", (c) => c.json({ bindings: ["KV", "OBJECTS"] }));
app.get("/kv/:key", async (c) => c.json({ value: await c.env.KV.get(c.req.param("key")) }));
app.put("/kv/:key", async (c) => { await c.env.KV.put(c.req.param("key"), await c.req.text()); return c.body(null, 204); });
app.put("/objects/:key", async (c) => { await c.env.OBJECTS.put(c.req.param("key"), c.req.raw.body!); return c.body(null, 204); });
app.get("/objects/:key", async (c) => { const object = await c.env.OBJECTS.get(c.req.param("key")); return object ? new Response(object.body) : c.notFound(); });
export default { fetch: app.fetch };
