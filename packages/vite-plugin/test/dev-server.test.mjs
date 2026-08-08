import assert from "node:assert/strict";
import { after, test } from "node:test";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";

import { nanoflare } from "../dist/index.js";

const root = fileURLToPath(new URL("./fixtures", import.meta.url));
const server = await createServer({
  root,
  configFile: false,
  plugins: [
    nanoflare({
      bindings: { MESSAGE: "from a Worker", COUNT: 3 },
    }),
  ],
});

await server.listen();

after(async () => {
  await server.close();
});

test("runs an API request in the local Worker", async () => {
  const response = await fetch(server.resolvedUrls.local[0] + "api/greeting");

  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), {
    message: "from a Worker",
    count: 3,
    configMessage: "from nanoflare.json",
    pathname: "/api/greeting",
  });
});

test("runs document requests in the local Worker before Vite's HTML fallback", async () => {
  const response = await fetch(server.resolvedUrls.local[0] + "dashboard", {
    headers: { accept: "text/html" },
  });

  assert.equal(response.status, 200);
  assert.equal((await response.json()).pathname, "/dashboard");
});

test("forwards multipart files to the Worker", async () => {
  const body = new FormData();
  body.set("image", new File(["image bytes"], "example.png", { type: "image/png" }));

  const response = await fetch(server.resolvedUrls.local[0] + "api/upload", {
    method: "POST",
    body,
  });

  const payload = await response.json();
  assert.equal(payload.isFile, true, JSON.stringify(payload));
  assert.equal(payload.constructor, "File");
  assert.equal(payload.name, "example.png");
});
