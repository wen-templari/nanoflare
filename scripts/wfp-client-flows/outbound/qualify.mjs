import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import vm from "node:vm";

// Offline by default. No account, shell credentials or network requests are used.
const directory = dirname(fileURLToPath(import.meta.url));
const root = resolve(directory, "../../..");
const args = process.argv.slice(2);
const flags = new Set(["--local", "--native", "--race"]);
const options = {};
for (let i = 0; i < args.length; i++) {
  const key = args[i];
  assert.ok(
    flags.has(key) ||
      ["--report", "--wrangler", "--sdk", "--backend", "--compare", "--groups"].includes(key),
    `unknown option ${key}`,
  );
  if (flags.has(key)) options[key] = true;
  else {
    assert.ok(args[i + 1] && !args[i + 1].startsWith("--"), `${key} needs a value`);
    options[key] = args[++i];
  }
}
assert.ok(!options["--native"] || options["--local"], "--native requires --local");
const raw = await readFile(resolve(directory, "cases.json"), "utf8");
const manifest = JSON.parse(raw);
assert.equal(manifest.schema_version, 1);
const evidence = new Set([
  "documented",
  "upstream-client-observed",
  "locally-qualified",
  "hosted-unverified",
]);
const dispositions = new Set([
  "preserve",
  "implement",
  "remove",
  "compare",
  "deployment-policy",
  "platform-dependency",
]);
const ids = new Set();
for (const entry of manifest.cases) {
  assert.ok(!ids.has(entry.id), `duplicate case ${entry.id}`);
  ids.add(entry.id);
  assert.ok(
    entry.cloudflare && entry.local && entry.evidence.length && entry.sources.length,
    `incomplete ${entry.id}`,
  );
  assert.ok(dispositions.has(entry.disposition));
  entry.evidence.forEach((level) => assert.ok(evidence.has(level), `unknown evidence ${level}`));
  entry.sources.forEach((source) =>
    assert.ok(manifest.sources[source], `unknown source ${source}`),
  );
  entry.groups.forEach((group) => assert.ok(manifest.groups[group], `unknown group ${group}`));
}
const sha = (value) => createHash("sha256").update(value).digest("hex");
const git = (args) => spawnSync("git", args, { cwd: root, encoding: "utf8" }).stdout?.trim();
const report = {
  schema_version: 1,
  contract_version: manifest.contract_version,
  manifest_sha256: sha(raw),
  generated_at: new Date().toISOString(),
  source_commit: git(["rev-parse", "HEAD"]),
  source_dirty: Boolean(git(["status", "--porcelain"])),
  node: process.version,
  upstream: {},
  groups: {},
  hosted: {
    status: "not-run",
    reason: "No hosted backend invoked; no WFP-entitled account is required for this runner",
  },
};

if (process.env.NANOFLARE_TEST_WORKERD) {
  const observed = spawnSync(process.env.NANOFLARE_TEST_WORKERD, ["--version"], {
    encoding: "utf8",
  });
  const version = observed.stdout?.trim();
  report.upstream.workerd = {
    status: observed.status === 0 ? "observed" : "failed",
    version,
    expected: manifest.versions.workerd,
  };
  assert.equal(observed.status, 0, "Pinned workerd executable must run");
  assert.equal(version, `workerd ${manifest.versions.workerd}`);
} else
  report.upstream.workerd = { status: "skipped", reason: "NANOFLARE_TEST_WORKERD not configured" };
report.postgres = { configured: Boolean(process.env.NANOFLARE_TEST_POSTGRES_URL) };

if (options["--wrangler"]) {
  const dir = resolve(options["--wrangler"]);
  const pkg = JSON.parse(await readFile(resolve(dir, "package.json"), "utf8"));
  assert.equal(pkg.version, manifest.versions.wrangler);
  const schemaRaw = await readFile(resolve(dir, "config-schema.json"), "utf8");
  const schema = JSON.parse(schemaRaw).definitions.DispatchNamespaceOutbound;
  assert.deepEqual(Object.keys(schema.properties).sort(), ["environment", "parameters", "service"]);
  assert.deepEqual(schema.required, ["service"]);
  assert.equal(schema.additionalProperties, false);
  const cli = await readFile(resolve(dir, "wrangler-dist/cli.js"), "utf8");
  const start = cli.indexOf(
    "bindings.dispatch_namespaces?.forEach(({ binding, namespace, outbound }) => {",
  );
  assert.ok(start >= 0, "pinned upload serializer was not found; inspect upstream before updating");
  const end = cli.indexOf("bindings.mtls_certificates", start);
  assert.ok(end > start);
  const serializer = cli.slice(start, end);
  const binding = {
    binding: "D",
    namespace: "customers",
    outbound: { service: "policy", environment: "staging", parameters: ["context", "trace"] },
  };
  const sandbox = { bindings: { dispatch_namespaces: [binding] }, metadataBindings: [] };
  // Execute the actual isolated upstream serialization block, not a local copy.
  vm.runInNewContext(serializer, sandbox, { timeout: 1000 });
  assert.deepEqual(JSON.parse(JSON.stringify(sandbox.metadataBindings)), [
    {
      name: "D",
      type: "dispatch_namespace",
      namespace: "customers",
      outbound: {
        worker: { service: "policy", environment: "staging" },
        params: [{ name: "context" }, { name: "trace" }],
      },
    },
  ]);
  report.upstream.wrangler = {
    status: "passed",
    version: pkg.version,
    schema_sha256: sha(schemaRaw),
    serializer_sha256: sha(serializer),
    observations: [
      "environment forwarded unchanged",
      "entrypoint absent from config schema",
      "parameter names mapped to params objects",
    ],
  };
} else
  report.upstream.wrangler = {
    status: "skipped",
    reason: "Pass --wrangler /absolute/path/to/wrangler package",
  };

if (options["--sdk"]) {
  const dir = resolve(options["--sdk"]);
  const pkg = JSON.parse(await readFile(resolve(dir, "package.json"), "utf8"));
  assert.equal(pkg.version, manifest.versions.cloudflare_sdk);
  const types = await readFile(resolve(dir, "resources/workers/scripts/scripts.d.ts"), "utf8");
  const outboundStart = types.indexOf("interface WorkersBindingKindDispatchNamespace");
  const outboundEnd = types.indexOf(
    "interface WorkersBindingKindDurableObjectNamespace",
    outboundStart,
  );
  const outbound = types.slice(outboundStart, outboundEnd);
  for (const selector of ["entrypoint", "environment", "service"])
    assert.ok(outbound.includes(`${selector}?: string;`));
  const { createForm } = await import(pathToFileURL(resolve(dir, "internal/uploads.mjs")));
  const noNetwork = async (url) => {
    // SDK's File support feature detection uses data:,; no network fallback.
    assert.equal(String(url), "data:,");
    return new Response("");
  };
  const metadata = {
    main_module: "worker.js",
    bindings: [
      {
        name: "D",
        type: "dispatch_namespace",
        namespace: "customers",
        outbound: {
          worker: { service: "policy", environment: "staging", entrypoint: "Policy" },
          params: [{ name: "context" }],
        },
      },
    ],
  };
  const defaultParts = [...(await createForm({ metadata }, noNetwork)).entries()];
  assert.ok(
    defaultParts.some(
      ([key, value]) =>
        key === "metadata[bindings][][outbound][worker][entrypoint]" && value === "Policy",
    ),
  );
  const json = JSON.stringify(metadata);
  const explicitParts = await createForm(
    { metadata: new File([json], "metadata.json", { type: "application/json" }) },
    noNetwork,
  );
  assert.equal(await explicitParts.get("metadata").text(), json);
  report.upstream.sdk = {
    status: "passed",
    version: pkg.version,
    selector_types_sha256: sha(outbound),
    default_keys: defaultParts.map(([key]) => key),
    observations: [
      "types expose service/environment/entrypoint",
      "default object arrays emit unindexed fields",
      "explicit JSON metadata preserves full selector",
    ],
  };
} else
  report.upstream.sdk = {
    status: "skipped",
    reason: "Pass --sdk /absolute/path/to/cloudflare package",
  };

const selectedGroups = options["--groups"]?.split(",");
if (selectedGroups)
  for (const name of selectedGroups) assert.ok(manifest.groups[name], `unknown group ${name}`);
if (options["--local"]) {
  for (const [name, group] of Object.entries(manifest.groups)) {
    if (selectedGroups && !selectedGroups.includes(name)) {
      report.groups[name] = {
        status: "skipped",
        reason: "Excluded by explicit --groups selection",
      };
      continue;
    }
    if (group.native && !options["--native"]) {
      report.groups[name] = {
        status: "skipped",
        reason: "Native/client tests require --native plus their documented environment variables",
      };
      continue;
    }
    const args = [
      "test",
      ...(options["--race"] ? ["-race"] : []),
      "-json",
      group.package,
      "-run",
      `^${group.test}$`,
      "-count=1",
      "-timeout=120s",
    ];
    const run = spawnSync("go", args, {
      cwd: root,
      encoding: "utf8",
      timeout: 150000,
      maxBuffer: 16 * 1024 * 1024,
    });
    const events = (run.stdout || "")
      .split("\n")
      .filter(Boolean)
      .flatMap((line) => {
        try {
          return [JSON.parse(line)];
        } catch {
          return [];
        }
      });
    const final = events.findLast(
      (event) => event.Test === group.test && ["pass", "fail", "skip"].includes(event.Action),
    );
    const skipped = events
      .filter((event) => event.Action === "skip" && event.Test)
      .map((event) => event.Test);
    const status =
      run.status !== 0 || run.error || final?.Action === "fail"
        ? "failed"
        : final?.Action === "skip" || !final
          ? "skipped"
          : "passed";
    report.groups[name] = {
      status,
      command: ["go", ...args],
      skipped_subtests: skipped,
      transcript_sha256: sha((run.stdout || "") + (run.stderr || "")),
      ...(status === "failed"
        ? { output: (run.stdout || "") + (run.stderr || ""), error: run.error?.message }
        : {}),
    };
    console.error(
      `${name}: ${status}${skipped.length ? ` (${skipped.length} skipped subtests)` : ""}`,
    );
  }
}
if (options["--backend"]) {
  const { runBackend, compareObservations } = await import("./backend.mjs");
  assert.ok(["hosted", "local"].includes(options["--backend"]));
  let result;
  try {
    result = await runBackend({
      baseURL: process.env.NF_OUTBOUND_API_URL,
      account: process.env.NF_OUTBOUND_ACCOUNT,
      token: process.env.NF_OUTBOUND_TOKEN,
      invokeURL: process.env.NF_OUTBOUND_INVOKE_URL,
      publishWorkersDev: options["--backend"] === "hosted",
    });
  } catch (error) {
    result = error.report ?? {
      status: "failed",
      observations: {},
      cleanupErrors: [],
      error: error.message,
    };
    process.exitCode = 1;
  }
  report.backend = { kind: options["--backend"], ...result };
  if (options["--backend"] === "hosted")
    report.hosted = {
      status: result.cleanupErrors.length ? "cleanup-failed" : result.status,
      case_ids: Object.keys(result.observations),
    };
  if (options["--compare"]) {
    const previous = JSON.parse(await readFile(options["--compare"], "utf8"));
    report.comparison = compareObservations(previous.backend, report.backend);
  }
  if (result.cleanupErrors.length || report.comparison?.some((item) => item.status === "different"))
    process.exitCode = 1;
}
report.cases = manifest.cases.map((entry) => ({
  id: entry.id,
  disposition: entry.disposition,
  evidence: entry.evidence,
  local_groups: entry.groups.map((group) => ({
    group,
    status: report.groups[group]?.status || "not-run",
  })),
  // Group success validates the stated local coverage only; it cannot promote a
  // case to hosted equivalence or imply that pending portions were exercised.
  hosted:
    report.backend?.kind === "hosted" && Object.hasOwn(report.backend.observations, entry.id)
      ? "observed"
      : "unverified",
  ...(report.backend?.kind === "hosted" && Object.hasOwn(report.backend.observations, entry.id)
    ? { hosted_observation: report.backend.observations[entry.id] }
    : {}),
}));
report.summary = {
  cases: report.cases.length,
  groups_passed: Object.values(report.groups).filter((group) => group.status === "passed").length,
  groups_failed: Object.values(report.groups).filter((group) => group.status === "failed").length,
};
if (options["--report"])
  await writeFile(resolve(options["--report"]), JSON.stringify(report, null, 2) + "\n");
else console.log(JSON.stringify(report, null, 2));
if (report.summary.groups_failed) process.exitCode = 1;
