import assert from "node:assert/strict";
import { cp, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
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

for (const filename of [
  "nanoflare.json",
  "nanoflare.jsonc",
  "nanoflare.toml",
  "wrangler.json",
  "wrangler.jsonc",
  "wrangler.toml",
]) {
  test(`builds the shared configuration from ${filename}`, async (t) => {
    const root = await mkdtemp(join(tmpdir(), "nanoflare-shared-config-"));
    await cp(fixtures, root, { recursive: true });
    t.after(() => rm(root, { force: true, recursive: true }));
    const source = JSON.parse(await readFile(join(root, "nanoflare.json"), "utf8"));
    await rm(join(root, "nanoflare.json"));
    const config = {
      ...source,
      d1_databases: [{ binding: "DB", database_name: "app" }],
      r2_buckets: [{ binding: "BUCKET", bucket_name: "uploads" }],
      dispatch_namespaces: [{ binding: "DISPATCHER", namespace: "customers" }],
      nanoflare: { resource_ids: { DB: "nanoflare-db" } },
    };
    const content = filename.endsWith(".toml")
      ? `name = "${config.name}"\nmain = "${config.main}"\ncompatibility_date = "${config.compatibility_date}"\n[[d1_databases]]\nbinding = "DB"\ndatabase_name = "app"\n[[r2_buckets]]\nbinding = "BUCKET"\nbucket_name = "uploads"\n[[dispatch_namespaces]]\nbinding = "DISPATCHER"\nnamespace = "customers"\n[nanoflare.resource_ids]\nDB = "nanoflare-db"\n`
      : "// shared configuration\n" + JSON.stringify(config).replace(/}$/, ",}");
    await writeFile(join(root, filename), content);
    await build({ root, configFile: false, logLevel: "silent", plugins: [nanoflare()] });
    const manifest = JSON.parse(
      await readFile(join(root, "dist", config.name, "nanoflare.json"), "utf8"),
    );
    assert.equal(manifest.d1_databases[0].binding, "DB");
    assert.equal(manifest.r2_buckets[0].binding, "BUCKET");
    assert.deepEqual(manifest.dispatch_namespaces, config.dispatch_namespaces);
    assert.deepEqual(manifest.nanoflare, config.nanoflare);
    assert.equal(await readFile(join(root, filename), "utf8"), content);
  });
}
