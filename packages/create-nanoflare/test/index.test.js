import assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import test from "node:test";

import {
  helpMessage,
  latestCompatibilityDate,
  parseArgs,
  readStarterTemplate,
  run,
} from "../dist/index.js";

const templateIDs = ["starter", "bindings", "pages", "spa", "ssr", "api", "mcp", "oauth-mcp"];

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
  assert.match(stdout.text, /mcp\s+A public MCP Worker/);
  assert.match(stdout.text, /oauth-mcp\s+An OAuth-protected MCP Worker/);
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
    main: "worker.ts",
    format: "modules",
    compatibility_date: "2026-06-01",
  });
  assert.equal(
    await readFile(resolve(destination, "worker.ts"), "utf8"),
    await readStarterTemplate(),
  );
  assert.doesNotMatch(
    await readFile(resolve(destination, "nanoflare.json"), "utf8"),
    /{{projectName}}|{{compatibilityDate}}/,
  );
  assert.match(stdout.text, /nanoflare create/);
});

test("warns and uses the latest supported compatibility date when today is newer", async () => {
  const cwd = await mkdtemp(resolve(tmpdir(), "create-nanoflare-"));
  const stderr = output();
  await run(["future-worker", "--template", "starter", "--no-interactive"], {
    cwd,
    stderr,
    stdout: output(),
    now: new Date("2026-08-31T12:00:00Z"),
  });

  const project = JSON.parse(
    await readFile(resolve(cwd, "future-worker", "nanoflare.json"), "utf8"),
  );
  assert.equal(project.compatibility_date, latestCompatibilityDate);
  assert.match(stderr.text, /Warning: compatibility date 2026-08-31 .* using 2026-07-06 instead/);
});

test("prompts for a missing directory and template", async () => {
  const cwd = await mkdtemp(resolve(tmpdir(), "create-nanoflare-"));
  const answers = ["prompted-worker", "starter"];
  await run([], { cwd, interactive: true, prompt: async () => answers.shift(), stdout: output() });
  await readFile(resolve(cwd, "prompted-worker", "worker.ts"));
});

test("rejects unknown templates and preserves non-empty directories unless overwritten", async () => {
  const cwd = await mkdtemp(resolve(tmpdir(), "create-nanoflare-"));
  await assert.rejects(
    run(["worker", "--template", "missing", "--no-interactive"], { cwd, stdout: output() }),
    /Unknown template.*mcp, oauth-mcp/,
  );
  const destination = resolve(cwd, "worker");
  await mkdir(destination);
  await writeFile(resolve(destination, "keep.txt"), "keep");
  await assert.rejects(run(["worker", "--no-interactive"], { cwd, stdout: output() }), /not empty/);
  assert.equal(await readFile(resolve(destination, "keep.txt"), "utf8"), "keep");
  await run(["worker", "--overwrite", "--no-interactive"], { cwd, stdout: output() });
  await assert.rejects(readFile(resolve(destination, "keep.txt")), { code: "ENOENT" });
  await readFile(resolve(destination, "worker.ts"));
});

test("scaffolds public and OAuth MCP templates with the expected dependencies", async () => {
  const cwd = await mkdtemp(resolve(tmpdir(), "create-nanoflare-"));
  const now = new Date("2026-08-29T12:00:00Z");

  await run(["public-echo", "--template", "mcp", "--no-interactive"], {
    cwd,
    now,
    stdout: output(),
  });
  const publicDirectory = resolve(cwd, "public-echo");
  const publicProject = JSON.parse(
    await readFile(resolve(publicDirectory, "nanoflare.json"), "utf8"),
  );
  const publicPackage = JSON.parse(
    await readFile(resolve(publicDirectory, "package.json"), "utf8"),
  );
  assert.equal(publicProject.name, "public-echo");
  assert.equal(publicProject.compatibility_date, latestCompatibilityDate);
  assert.equal(publicProject.kv_namespaces, undefined);
  assert.equal(publicPackage.dependencies["@modelcontextprotocol/server"], "^2.0.0");
  assert.equal(publicPackage.dependencies["@cloudflare/workers-oauth-provider"], undefined);
  assert.equal(publicPackage.devDependencies["@nanoflare/vite-plugin"], "^0.1.0");
  assert.match(await readFile(resolve(publicDirectory, "src/worker.ts"), "utf8"), /"echo"/);
  assert.match(await readFile(resolve(publicDirectory, "vite.config.ts"), "utf8"), /noExternal/);

  await run(["private-echo", "--template", "oauth-mcp", "--no-interactive"], {
    cwd,
    now,
    stdout: output(),
  });
  const oauthDirectory = resolve(cwd, "private-echo");
  const oauthProject = JSON.parse(
    await readFile(resolve(oauthDirectory, "nanoflare.json"), "utf8"),
  );
  const oauthPackage = JSON.parse(await readFile(resolve(oauthDirectory, "package.json"), "utf8"));
  assert.equal(oauthProject.name, "private-echo");
  assert.equal(oauthProject.compatibility_date, latestCompatibilityDate);
  assert.deepEqual(oauthProject.compatibility_flags, ["global_fetch_strictly_public"]);
  assert.deepEqual(oauthProject.kv_namespaces, [
    { binding: "OAUTH_KV", id: "replace-with-oauth-kv-namespace-id" },
  ]);
  assert.equal(oauthPackage.dependencies["@cloudflare/workers-oauth-provider"], "^0.10.3");
  assert.match(
    await readFile(resolve(oauthDirectory, "src/worker-runtime.d.ts"), "utf8"),
    /cloudflare:workers/,
  );
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

test("scaffolds every documented template", async () => {
  const cwd = await mkdtemp(resolve(tmpdir(), "create-nanoflare-"));
  for (const id of templateIDs) {
    const result = await run([id, "--template", id, "--no-interactive"], { cwd, stdout: output() });
    assert.equal(result.template, id);
    const project = JSON.parse(await readFile(resolve(cwd, id, "nanoflare.json"), "utf8"));
    assert.equal(project.name, id);
    if (["pages", "spa", "oauth-mcp"].includes(id)) {
      assert.ok(project.files.includes("dist/worker.js"));
    } else {
      assert.equal(project.files, undefined);
    }
  }
});
