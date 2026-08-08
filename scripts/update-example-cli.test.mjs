import assert from "node:assert/strict";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  findExampleManifests,
  isExampleCliCurrent,
  normalizeVersion,
  parseVersionArgs,
} from "./update-example-cli.mjs";

test("normalizes release tags to npm versions", () => {
  assert.equal(normalizeVersion("v1.2.3"), "1.2.3");
  assert.equal(normalizeVersion("1.2.3-rc.1+build.4"), "1.2.3-rc.1+build.4");
});

test("rejects invalid CLI versions", () => {
  for (const value of [undefined, "", "1.2", "v1.2.3.4", "latest"]) {
    assert.throws(() => normalizeVersion(value), /not a valid npm version/);
  }
});

test("accepts pnpm's argument separator", () => {
  assert.equal(parseVersionArgs(["--", "1.2.3"]), "1.2.3");
});

test("finds only examples with the CLI as a dev dependency", async () => {
  const directory = await mkdtemp(join(tmpdir(), "nanoflare-examples-"));
  try {
    await mkdir(join(directory, "uses-cli"));
    await mkdir(join(directory, "no-cli"));
    await mkdir(join(directory, "runtime-cli"));
    await mkdir(join(directory, "nested", "uses-cli"), { recursive: true });
    await writeFile(
      join(directory, "uses-cli", "package.json"),
      JSON.stringify({ devDependencies: { "@nanoflare/cli": "^1.2.3" } }),
    );
    await writeFile(
      join(directory, "no-cli", "package.json"),
      JSON.stringify({ devDependencies: {} }),
    );
    await writeFile(
      join(directory, "runtime-cli", "package.json"),
      JSON.stringify({ dependencies: { "@nanoflare/cli": "^1.2.3" } }),
    );
    await writeFile(
      join(directory, "nested", "uses-cli", "package.json"),
      JSON.stringify({ devDependencies: { "@nanoflare/cli": "^1.2.3" } }),
    );

    assert.deepEqual(await findExampleManifests(directory), [
      join(directory, "nested", "uses-cli", "package.json"),
      join(directory, "uses-cli", "package.json"),
    ]);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("recognizes examples whose manifest and lockfile already match", async () => {
  const directory = await mkdtemp(join(tmpdir(), "nanoflare-examples-"));
  const manifestPath = join(directory, "package.json");
  try {
    await writeFile(
      manifestPath,
      JSON.stringify({ devDependencies: { "@nanoflare/cli": "^1.2.3" } }),
    );
    await writeFile(
      join(directory, "package-lock.json"),
      JSON.stringify({ packages: { "node_modules/@nanoflare/cli": { version: "1.2.3" } } }),
    );

    assert.equal(await isExampleCliCurrent(manifestPath, "1.2.3"), true);
    assert.equal(await isExampleCliCurrent(manifestPath, "1.2.4"), false);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
