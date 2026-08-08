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
const createSource = resolve(root, "packages/create-nanoflare");
const output = resolve(root, "dist/npm");
const targets = [
  "darwin-arm64",
  "darwin-x64",
  "linux-arm64",
  "linux-x64",
  "win32-arm64",
  "win32-x64",
];

async function assertPackageVersion(directory, allowWorkspaceOptionalDependencies = false) {
  const manifestPath = resolve(directory, "package.json");
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  if (manifest.version !== version) {
    throw new Error(`${manifestPath} has version ${manifest.version}; expected ${version}`);
  }
  if (manifest.optionalDependencies) {
    for (const [name, dependencyVersion] of Object.entries(manifest.optionalDependencies)) {
      if (
        dependencyVersion !== version &&
        !(allowWorkspaceOptionalDependencies && dependencyVersion === "workspace:*")
      ) {
        throw new Error(`${manifestPath} has ${name}@${dependencyVersion}; expected ${version}`);
      }
    }
  }
}

async function copyPackage(from, to) {
  await assertPackageVersion(from);
  await cp(from, to, { recursive: true });
  await cp(resolve(root, "LICENSE"), resolve(to, "LICENSE"));
}

const sourceVersion = (await readFile(resolve(root, "VERSION"), "utf8")).trim();
if (sourceVersion !== version) {
  throw new Error(`VERSION is ${sourceVersion}; expected ${version} from release tag ${tag}`);
}

await rm(output, { recursive: true, force: true });
await mkdir(output, { recursive: true });

const launcher = resolve(output, "cli");
await mkdir(launcher, { recursive: true });
await assertPackageVersion(source, true);
for (const entry of ["bin", "lib", "README.md", "package.json"]) {
  await cp(resolve(source, entry), resolve(launcher, entry), { recursive: true });
}
const launcherManifestPath = resolve(launcher, "package.json");
const launcherManifest = JSON.parse(await readFile(launcherManifestPath, "utf8"));
for (const name of Object.keys(launcherManifest.optionalDependencies ?? {})) {
  launcherManifest.optionalDependencies[name] = version;
}
await writeFile(launcherManifestPath, `${JSON.stringify(launcherManifest, null, 2)}\n`);
await cp(resolve(root, "LICENSE"), resolve(launcher, "LICENSE"));

for (const target of targets) {
  await copyPackage(resolve(source, "npm", target), resolve(output, target));
}

const createPackage = resolve(output, "create-nanoflare");
await mkdir(createPackage, { recursive: true });
await assertPackageVersion(createSource);
for (const entry of ["bin", "dist", "templates", "README.md", "package.json"]) {
  await cp(resolve(createSource, entry), resolve(createPackage, entry), { recursive: true });
}
await cp(resolve(root, "LICENSE"), resolve(createPackage, "LICENSE"));
