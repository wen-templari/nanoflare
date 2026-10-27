import { Miniflare } from "miniflare";
import assert from "node:assert/strict";
import { mkdtemp, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { build, createServer } from "vite";

import { privateDynamicProfile } from "../../cli/lib/vite.mjs";
import { nanoflare } from "../dist/index.js";

test("private profile uses production restrictions in the real dev runtime", async () => {
  const root = await mkdtemp(join(tmpdir(), "nf-private-dev-"));
  let server;
  try {
    await writeFile(
      join(root, "worker.js"),
      `export default {async fetch(request){try{if(new URL(request.url).pathname==='/cache')return caches.default;return await fetch('https://example.invalid',{cf:{cacheTtl:60}});}catch(error){return Response.json({name:error.name,message:error.message},{status:422});}}};`,
    );
    server = await createServer({
      root,
      configFile: false,
      plugins: [
        nanoflare({ entry: "worker.js", configPath: false, runtimeProfile: "private-dynamic" }),
      ],
    });
    await server.listen();
    for (const [path, message] of [
      ["cache", "Cache API is unavailable"],
      ["cf", "Custom fetch/cache options are unavailable"],
    ]) {
      const response = await fetch(server.resolvedUrls.local[0] + path, {
        headers: { accept: "text/html" },
      });
      assert.equal(response.status, 422);
      assert.ok((await response.json()).message.includes(message));
    }
  } finally {
    await server?.close();
    await rm(root, { recursive: true, force: true });
  }
});

test("portable Vite helper bundles production capability checks before Worker dependencies", async () => {
  const root = await mkdtemp(join(tmpdir(), "nf-portable-profile-"));
  let runtime;
  try {
    await writeFile(
      join(root, "dependency.js"),
      `let message;try{caches.default;}catch(error){message=error.message;}export {message};`,
    );
    await writeFile(
      join(root, "worker.js"),
      `import {message} from './dependency.js';export default {fetch(){return new Response(message||'cache unexpectedly available');}};`,
    );
    await build({
      root,
      configFile: false,
      logLevel: "silent",
      plugins: [privateDynamicProfile({ entry: "worker.js" })],
      ssr: { noExternal: true },
      build: {
        ssr: join(root, "worker.js"),
        outDir: join(root, "dist"),
        rollupOptions: { output: { entryFileNames: "worker.mjs" } },
      },
    });
    runtime = new Miniflare({
      modules: true,
      modulesRoot: join(root, "dist"),
      scriptPath: join(root, "dist/worker.mjs"),
      compatibilityDate: "2025-12-10",
    });
    const response = await runtime.dispatchFetch("https://example.test/");
    assert.match(await response.text(), /Cache API is unavailable/);
  } finally {
    await runtime?.dispose();
    await rm(root, { recursive: true, force: true });
  }
});
