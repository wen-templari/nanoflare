#!/usr/bin/env node

const { spawnSync } = require("node:child_process");
const path = require("node:path");

const supportedPackages = {
  "darwin arm64": "@nanoflare/cli-darwin-arm64",
  "darwin x64": "@nanoflare/cli-darwin-x64",
  "linux arm64": "@nanoflare/cli-linux-arm64",
  "linux x64": "@nanoflare/cli-linux-x64",
  "win32 arm64": "@nanoflare/cli-win32-arm64",
  "win32 x64": "@nanoflare/cli-win32-x64"
};

const packageName = supportedPackages[`${process.platform} ${process.arch}`];

if (!packageName) {
  console.error(`nanoflare: unsupported platform ${process.platform}/${process.arch}`);
  process.exit(1);
}

let binary;
try {
  const packageJSON = require.resolve(`${packageName}/package.json`);
  binary = path.join(path.dirname(packageJSON), "bin", process.platform === "win32" ? "nanoflare.exe" : "nanoflare");
} catch (error) {
  console.error(`nanoflare: failed to find ${packageName}. Try reinstalling @nanoflare/cli.`);
  process.exit(1);
}

const result = spawnSync(binary, process.argv.slice(2), { stdio: "inherit" });

if (result.error) {
  console.error(`nanoflare: ${result.error.message}`);
  process.exit(1);
}

process.exit(result.status === null ? 1 : result.status);
