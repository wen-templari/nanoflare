import assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import test from "node:test";

import { helpMessage, parseArgs, readStarterTemplate, run } from "../dist/index.js";

function output() {
  let text = "";
  return {
    write(value) {
      text += value;
    },
    get text() {
      return text;
    },
  };
}

test("parses supported options", () => {
  assert.deepEqual(parseArgs(["worker", "-t", "starter", "--overwrite", "--no-interactive"]), {
    directory: "worker",
    template: "starter",
    overwrite: true,
    interactive: false,
    help: false,
  });
  assert.throws(() => parseArgs(["one", "two"]), /Usage/);
  assert.throws(() => parseArgs(["--missing"]), /Unknown option/);
  assert.throws(() => parseArgs(["--template="]), /requires a template name/);
});

test("prints help without creating a project", async () => {
  const stdout = output();
  const result = await run(["--help"], {
    stdout,
    cwd: await mkdtemp(resolve(tmpdir(), "create-nanoflare-")),
  });
  assert.equal(result.status, "help");
  assert.equal(stdout.text, helpMessage);
});

test("creates a starter project with the directory name and UTC date", async () => {
  const cwd = await mkdtemp(resolve(tmpdir(), "create-nanoflare-"));
  const stdout = output();
  const result = await run(["hello-worker", "--template", "starter", "--no-interactive"], {
    cwd,
    stdout,
    now: new Date("2026-05-31T23:30:00-08:00"),
  });
  assert.equal(result.template, "starter");
  const destination = resolve(cwd, "hello-worker");
  const project = JSON.parse(await readFile(resolve(destination, "nanoflare.json"), "utf8"));
  assert.deepEqual(project, {
    $schema: "https://raw.githubusercontent.com/wen-templari/nanoflare/main/schemas/nanoflare.json",
    name: "hello-worker",
    main: "worker.js",
    format: "modules",
    compatibility_date: "2026-06-01",
    files: ["worker.js"],
  });
  assert.equal(
    await readFile(resolve(destination, "worker.js"), "utf8"),
    await readStarterTemplate(),
  );
  assert.doesNotMatch(
    await readFile(resolve(destination, "nanoflare.json"), "utf8"),
    /{{projectName}}|{{compatibilityDate}}/,
  );
  assert.match(stdout.text, /nanoflare create/);
});

test("prompts for a missing directory and template", async () => {
  const cwd = await mkdtemp(resolve(tmpdir(), "create-nanoflare-"));
  const answers = ["prompted-worker", "starter"];
  await run([], { cwd, interactive: true, prompt: async () => answers.shift(), stdout: output() });
  await readFile(resolve(cwd, "prompted-worker", "worker.js"));
});

test("rejects unknown templates and preserves non-empty directories unless overwritten", async () => {
  const cwd = await mkdtemp(resolve(tmpdir(), "create-nanoflare-"));
  await assert.rejects(
    run(["worker", "--template", "missing", "--no-interactive"], { cwd, stdout: output() }),
    /Unknown template/,
  );
  const destination = resolve(cwd, "worker");
  await mkdir(destination);
  await writeFile(resolve(destination, "keep.txt"), "keep");
  await assert.rejects(run(["worker", "--no-interactive"], { cwd, stdout: output() }), /not empty/);
  assert.equal(await readFile(resolve(destination, "keep.txt"), "utf8"), "keep");
  await run(["worker", "--overwrite", "--no-interactive"], { cwd, stdout: output() });
  await assert.rejects(readFile(resolve(destination, "keep.txt")), { code: "ENOENT" });
  await readFile(resolve(destination, "worker.js"));
});

test("cancelling an interactive overwrite preserves the destination", async () => {
  const cwd = await mkdtemp(resolve(tmpdir(), "create-nanoflare-"));
  const destination = resolve(cwd, "worker");
  await mkdir(destination);
  await writeFile(resolve(destination, "keep.txt"), "keep");
  const answers = ["starter", "n"];
  await assert.rejects(
    run(["worker"], {
      cwd,
      interactive: true,
      prompt: async () => answers.shift(),
      stdout: output(),
    }),
    /not empty/,
  );
  assert.equal(await readFile(resolve(destination, "keep.txt"), "utf8"), "keep");
});

test("keeps the npm starter asset aligned with the Go CLI template", async () => {
  const packageRoot = resolve(import.meta.dirname, "..");
  const goTemplate = await readFile(
    resolve(packageRoot, "..", "..", "templates", "starter-worker", "worker.js"),
    "utf8",
  );
  assert.deepEqual(await readStarterTemplate(), goTemplate);
});
