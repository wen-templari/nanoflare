const test = require("node:test");
const assert = require("node:assert/strict");

const { packageName } = require("../lib/resolve.cjs");

test("selects the matching platform package", () => {
  assert.equal(packageName("darwin", "arm64"), "@nanoflare/cli-darwin-arm64");
  assert.equal(packageName("win32", "x64"), "@nanoflare/cli-win32-x64");
});

test("rejects unsupported platforms with a helpful error", () => {
  assert.throws(() => packageName("freebsd", "x64"), /unsupported platform freebsd\/x64/);
});
