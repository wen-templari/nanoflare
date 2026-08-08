import assert from "node:assert/strict";
import { cp, mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { build } from "vite";

import { nanoflare } from "../dist/index.js";

const fixtures = fileURLToPath(new URL("./fixtures", import.meta.url));

test("builds a Worker and writes a deploy manifest", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "nanoflare-vite-plugin-"));
  await cp(fixtures, root, { recursive: true });
  t.after(() => rm(root, { force: true, recursive: true }));

  await build({
    root,
    configFile: false,
    logLevel: "silent",
    plugins: [nanoflare()],
  });

  const manifest = JSON.parse(
    await readFile(join(root, "dist", "vite-plugin-test", "nanoflare.json"), "utf8"),
  );

  assert.equal(manifest.main, "dist/vite-plugin-test/worker.mjs");
  assert.deepEqual(manifest.files, ["dist/vite-plugin-test/worker.mjs"]);
  assert.equal(manifest.format, "modules");
});
