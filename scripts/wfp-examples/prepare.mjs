// Build unchanged upstream applications from an immutable revision.
import { execFileSync } from "node:child_process";
import { mkdirSync, existsSync, writeFileSync } from "node:fs";
import { resolve, join } from "node:path";
const revision = "dfdf14099a81a08cfe1ffc78f8463605670ae513";
const root = resolve(process.argv[2] || "work/wfp-examples");
mkdirSync(root, { recursive: true });
const source = join(root, "upstream");
const run = (command, args, cwd) =>
  execFileSync(command, args, {
    cwd,
    stdio: "inherit",
    env: { ...process.env, WRANGLER_SEND_METRICS: "false" },
  });
if (!existsSync(source)) {
  run("git", [
    "clone",
    "--filter=blob:none",
    "--no-checkout",
    "https://github.com/cloudflare/templates.git",
    source,
  ]);
  run(
    "git",
    ["sparse-checkout", "set", "worker-publisher-template", "internal-sites-template"],
    source,
  );
  run("git", ["checkout", "--detach", revision], source);
}
const actual = execFileSync("git", ["rev-parse", "HEAD"], { cwd: source, encoding: "utf8" }).trim();
if (actual !== revision) throw new Error(`Expected pinned revision ${revision}, found ${actual}`);
run(
  "git",
  ["diff", "--exit-code", "HEAD", "--", "worker-publisher-template", "internal-sites-template"],
  source,
);
for (const app of ["worker-publisher-template", "internal-sites-template"]) {
  const cwd = join(source, app);
  run("npm", ["ci", "--ignore-scripts", "--no-audit", "--no-fund"], cwd);
  run(
    process.execPath,
    [
      join(cwd, "node_modules/wrangler/bin/wrangler.js"),
      "deploy",
      "--dry-run",
      ...(app === "internal-sites-template" ? ["--env="] : []),
      "--outdir",
      join(root, app),
    ],
    cwd,
  );
}
run(
  "git",
  ["diff", "--exit-code", "HEAD", "--", "worker-publisher-template", "internal-sites-template"],
  source,
);
writeFileSync(
  join(root, "upstream.json"),
  JSON.stringify(
    {
      revision,
      source,
      bundles: {
        publisher: join(root, "worker-publisher-template/index.js"),
        sites: join(root, "internal-sites-template/index.js"),
      },
    },
    null,
    2,
  ) + "\n",
);
console.log(`Pinned sources and bundles ready in ${root}`);
