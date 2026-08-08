import { readdir, readFile, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const cliPackage = "@nanoflare/cli";
const semverPattern = /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/;

export function normalizeVersion(input) {
  const version = input?.startsWith("v") ? input.slice(1) : input;
  if (!version || !semverPattern.test(version)) {
    throw new Error(`CLI version ${input ?? ""} is not a valid npm version`);
  }
  return version;
}

export function parseArgs(args) {
  const check = args.includes("--check");
  const versions = args.filter((arg) => arg !== "--check" && arg !== "--");
  if (versions.length > 1) {
    throw new Error("usage: node scripts/update-create-template-cli.mjs [--check] [cli-version]");
  }
  return { check, version: versions[0] && normalizeVersion(versions[0]) };
}

export async function findTemplateManifests(templatesDirectory) {
  const entries = await readdir(templatesDirectory, { withFileTypes: true });
  const manifests = [];

  for (const entry of entries) {
    if (!entry.isDirectory() || entry.name === "node_modules") continue;

    const manifestPath = join(templatesDirectory, entry.name, "package.json");
    try {
      const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
      if (manifest.devDependencies?.[cliPackage]) manifests.push(manifestPath);
    } catch (error) {
      if (error?.code !== "ENOENT") throw error;
    }
  }

  return manifests.sort();
}

export async function isTemplateCliCurrent(manifestPath, version) {
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  return manifest.devDependencies?.[cliPackage] === `^${version}`;
}

export async function updateTemplateCli(manifestPath, version) {
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  if (manifest.devDependencies?.[cliPackage] === `^${version}`) return false;

  manifest.devDependencies[cliPackage] = `^${version}`;
  await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
  return true;
}

export async function main(args = process.argv.slice(2)) {
  const { check, version: suppliedVersion } = parseArgs(args);
  const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
  const version =
    suppliedVersion ?? normalizeVersion((await readFile(join(root, "VERSION"), "utf8")).trim());
  const manifests = await findTemplateManifests(
    join(root, "packages", "create-nanoflare", "templates"),
  );

  if (manifests.length === 0) {
    throw new Error(`no create-nanoflare templates declare ${cliPackage} in devDependencies`);
  }

  const staleManifests = [];
  for (const manifestPath of manifests) {
    const current = await isTemplateCliCurrent(manifestPath, version);
    if (!current) staleManifests.push(manifestPath);

    if (!check) {
      const updated = await updateTemplateCli(manifestPath, version);
      console.log(
        `${updated ? "Updated" : "Already current"} ${manifestPath.slice(root.length + 1)}`,
      );
    }
  }

  if (check && staleManifests.length > 0) {
    throw new Error(
      `create-nanoflare templates must use ${cliPackage}@^${version}: ${staleManifests
        .map((manifestPath) => manifestPath.slice(root.length + 1))
        .join(", ")}`,
    );
  }
}

const isMain = process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (isMain) {
  main().catch((error) => {
    console.error(error.message);
    process.exitCode = 1;
  });
}
