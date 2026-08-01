const path = require("node:path");

const supportedPlatforms = new Set([
  "darwin-arm64",
  "darwin-x64",
  "linux-arm64",
  "linux-x64",
  "win32-arm64",
  "win32-x64",
]);

function packageName(platform = process.platform, arch = process.arch) {
  const target = `${platform}-${arch}`;
  if (!supportedPlatforms.has(target)) {
    throw new Error(
      `unsupported platform ${platform}/${arch}; supported platforms are ${[...supportedPlatforms].join(", ")}`,
    );
  }
  return `@nanoflare/cli-${target}`;
}

function resolveBinary(platform = process.platform, arch = process.arch) {
  const binary = platform === "win32" ? "nanoflare.exe" : "nanoflare";
  const dependency = packageName(platform, arch);
  try {
    return require.resolve(path.posix.join(dependency, "bin", binary));
  } catch (error) {
    if (error.code === "MODULE_NOT_FOUND") {
      throw new Error(
        `the bundled binary package ${dependency} is missing; reinstall @nanoflare/cli`,
      );
    }
    throw error;
  }
}

module.exports = { packageName, resolveBinary };
