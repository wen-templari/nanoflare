import { execFile } from "node:child_process";
import { readdir, readFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);
const cliPackage = "@nanoflare/cli";
const semverPattern = /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/;

export function normalizeVersion(input) {
  const version = input?.startsWith("v") ? input.slice(1) : input;
  if (!version || !semverPattern.test(version)) {
    throw new Error(`CLI version ${input ?? ""} is not a valid npm version`);
  }
  return version;
}

export function parseVersionArgs(args) {
  const versionArgs = args[0] === "--" ? args.slice(1) : args;
  if (versionArgs.length !== 1) {
    throw new Error("usage: pnpm examples:update-cli -- <published-cli-version>");
  }
  return normalizeVersion(versionArgs[0]);
}

export async function findExampleManifests(examplesDirectory) {
  const entries = await readdir(examplesDirectory, { withFileTypes: true });
  const manifests = [];

  for (const entry of entries) {
    if (!entry.isDirectory()) continue;

    const directory = join(examplesDirectory, entry.name);
    if (entry.name === "node_modules") continue;

    const manifestPath = join(directory, "package.json");
    try {
      const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
      if (manifest.devDependencies?.[cliPackage]) manifests.push(manifestPath);
    } catch (error) {
      if (error?.code !== "ENOENT") throw error;
    }

    manifests.push(...(await findExampleManifests(directory)));
  }

  return manifests.sort();
}

export async function isExampleCliCurrent(manifestPath, version) {
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  if (manifest.devDependencies?.[cliPackage] !== `^${version}`) return false;

  try {
    const lockfile = JSON.parse(
      await readFile(join(dirname(manifestPath), "package-lock.json"), "utf8"),
    );
    return lockfile.packages?.[`node_modules/${cliPackage}`]?.version === version;
  } catch (error) {
    if (error?.code === "ENOENT") return false;
    throw error;
  }
}

export async function updateExampleCli(manifestPath, version) {
  if (await isExampleCliCurrent(manifestPath, version)) return false;

  const directory = dirname(manifestPath);
  await execFileAsync(
    "npm",
    [
      "install",
      "--save-dev",
      "--package-lock-only",
      "--ignore-scripts",
      "--no-audit",
      "--no-fund",
      `${cliPackage}@^${version}`,
    ],
    { cwd: directory },
  );
  return true;
}

export async function main(args = process.argv.slice(2)) {
  const version = parseVersionArgs(args);
  const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
  const manifests = await findExampleManifests(join(root, "examples"));
  if (manifests.length === 0) {
    throw new Error(`no example package.json files declare ${cliPackage} in devDependencies`);
  }

  for (const manifestPath of manifests) {
    try {
      const updated = await updateExampleCli(manifestPath, version);
      console.log(
        `${updated ? "Updated" : "Already current"} ${manifestPath.slice(root.length + 1)}`,
      );
    } catch (error) {
      throw new Error(`failed to update ${manifestPath.slice(root.length + 1)}: ${error.message}`, {
        cause: error,
      });
    }
  }
}

const isMain = process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (isMain) {
  main().catch((error) => {
    console.error(error.message);
    process.exitCode = 1;
  });
}
