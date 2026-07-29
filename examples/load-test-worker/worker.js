export default {
  async fetch(request, env) {
    const path = new URL(request.url).pathname;

    const databaseRoute = path.match(/^\/db-(init|read|write)(?:\/(\d+))?$/);
    if (databaseRoute) {
      const [, operation, databaseNumber] = databaseRoute;
      const database = databaseNumber === "1" ? env.DB : databaseNumber ? env[`DB_${databaseNumber}`] : env.DB;
      if (!database) {
        return new Response("database is not configured", { status: 501 });
      }
      if (operation === "init") {
        await database.exec(
          "CREATE TABLE IF NOT EXISTS k6_events (id INTEGER PRIMARY KEY AUTOINCREMENT, value TEXT NOT NULL)",
        );
        await database.prepare("INSERT INTO k6_events (value) VALUES (?)")
          .bind("seed")
          .run();
        return new Response("initialized");
      }
      if (operation === "read") {
        const row = await database
          .prepare("SELECT COUNT(*) AS count FROM k6_events")
          .first();
        return Response.json(row);
      }
      await database.prepare("INSERT INTO k6_events (value) VALUES (?)")
        .bind(`${Date.now()}-${Math.random()}`)
        .run();
      return new Response("stored");
    }

    if (path === "/plain") {
      return new Response("plain");
    }

    if (path === "/kv-get") {
      return new Response((await env.KV.get("key")) ?? "");
    }

    if (path === "/kv-put") {
      await env.KV.put("key", "value");
      return new Response("stored");
    }

    if (path === "/objects") {
      if (!env.OBJECTS) {
        return Response.json({ objects: [], configured: false });
      }
      if (!env.OBJECTS.list) {
        return Response.json({ objects: [], configured: true, list: "unsupported" });
      }
      const objects = await env.OBJECTS.list({ prefix: "k6/" });
      return Response.json(objects.objects ?? objects);
    }

    if (path.startsWith("/object/")) {
      if (!env.OBJECTS) {
        return new Response("object storage bucket is not configured", { status: 501 });
      }
      const key = decodeURIComponent(path.slice("/object/".length));
      if (request.method === "PUT") {
        await env.OBJECTS.put(key, await request.arrayBuffer(), {
          httpMetadata: {
            contentType: request.headers.get("content-type") || "application/octet-stream",
          },
        });
        return new Response("stored");
      }
      if (request.method === "DELETE") {
        await env.OBJECTS.delete(key);
        return new Response(null, { status: 204 });
      }
      const object = await env.OBJECTS.get(key);
      if (!object) {
        return new Response("not found", { status: 404 });
      }
      return new Response(object.body, {
        headers: { "content-type": object.httpMetadata?.contentType || "application/octet-stream" },
      });
    }

    return new Response("plain");
  },
};
