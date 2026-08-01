import { cp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const [tag] = process.argv.slice(2);
if (!tag) {
  throw new Error("usage: node scripts/prepare-npm-cli-release.mjs <release-tag>");
}

const version = tag.startsWith("v") ? tag.slice(1) : tag;
if (!/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/.test(version)) {
  throw new Error(`release tag ${tag} is not a valid npm version`);
}

const root = resolve(import.meta.dirname, "..");
const source = resolve(root, "packages/cli");
const output = resolve(root, "dist/npm");
const targets = [
  "darwin-arm64",
  "darwin-x64",
  "linux-arm64",
  "linux-x64",
  "win32-arm64",
  "win32-x64",
];

async function stampPackage(directory) {
  const manifestPath = resolve(directory, "package.json");
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  manifest.version = version;
  if (manifest.optionalDependencies) {
    for (const name of Object.keys(manifest.optionalDependencies)) {
      manifest.optionalDependencies[name] = version;
    }
  }
  await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
  await cp(resolve(root, "LICENSE"), resolve(directory, "LICENSE"));
}

async function copyPackage(from, to) {
  await cp(from, to, { recursive: true });
  await stampPackage(to);
}

await rm(output, { recursive: true, force: true });
await mkdir(output, { recursive: true });

const launcher = resolve(output, "cli");
await mkdir(launcher, { recursive: true });
for (const entry of ["bin", "lib", "README.md", "package.json"]) {
  await cp(resolve(source, entry), resolve(launcher, entry), { recursive: true });
}
await stampPackage(launcher);

for (const target of targets) {
  await copyPackage(resolve(source, "npm", target), resolve(output, target));
}
