import assert from "node:assert/strict";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  findTemplateManifests,
  isTemplateCliCurrent,
  normalizeVersion,
  parseArgs,
  updateTemplateCli,
} from "./update-create-template-cli.mjs";

test("normalizes release tags to npm versions", () => {
  assert.equal(normalizeVersion("v1.2.3"), "1.2.3");
  assert.throws(() => normalizeVersion("latest"), /not a valid npm version/);
});

test("parses update and check commands", () => {
  assert.deepEqual(parseArgs([]), { check: false, version: undefined });
  assert.deepEqual(parseArgs(["--check", "v1.2.3"]), { check: true, version: "1.2.3" });
  assert.throws(() => parseArgs(["1.2.3", "1.2.4"]), /usage/);
});

test("finds and updates create template CLI dependencies", async () => {
  const directory = await mkdtemp(join(tmpdir(), "nanoflare-templates-"));
  const manifestPath = join(directory, "starter", "package.json");
  try {
    await mkdir(join(directory, "starter"));
    await mkdir(join(directory, "no-cli"));
    await writeFile(
      manifestPath,
      JSON.stringify({ devDependencies: { "@nanoflare/cli": "^1.2.3", vite: "^7.0.0" } }),
    );
    await writeFile(
      join(directory, "no-cli", "package.json"),
      JSON.stringify({ devDependencies: {} }),
    );

    assert.deepEqual(await findTemplateManifests(directory), [manifestPath]);
    assert.equal(await isTemplateCliCurrent(manifestPath, "1.2.4"), false);
    assert.equal(await updateTemplateCli(manifestPath, "1.2.4"), true);
    assert.equal(await isTemplateCliCurrent(manifestPath, "1.2.4"), true);
    assert.equal(await updateTemplateCli(manifestPath, "1.2.4"), false);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
