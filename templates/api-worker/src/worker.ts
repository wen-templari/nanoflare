import { drizzle } from "drizzle-orm/d1";
import { Hono } from "hono";

import { notes } from "./schema";
const app = new Hono<{ Bindings: Env }>();
app.get("/notes", async (c) => c.json(await drizzle(c.env.DB).select().from(notes)));
app.post("/notes", async (c) => {
  const { body } = await c.req.json<{ body: string }>();
  await drizzle(c.env.DB).insert(notes).values({ body });
  return c.json({ ok: true }, 201);
});
app.get("/openapi.json", (c) =>
  c.json({
    openapi: "3.1.0",
    info: { title: "Notes API", version: "1.0.0" },
    paths: { "/notes": { get: { summary: "List notes" }, post: { summary: "Create a note" } } },
  }),
);
app.get("/docs", (c) => c.html('<a href="/openapi.json">OpenAPI document</a>'));
export default { fetch: app.fetch };
