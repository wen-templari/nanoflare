#!/usr/bin/env node

const { resolveBinary } = require("../lib/resolve.cjs");
const { spawn } = require("node:child_process");

let binary;
try {
  binary = resolveBinary();
} catch (error) {
  console.error(`nanoflare: ${error.message}`);
  process.exitCode = 1;
  return;
}

const child = spawn(binary, process.argv.slice(2), { stdio: "inherit" });

child.on("error", (error) => {
  console.error(`nanoflare: failed to start bundled binary: ${error.message}`);
  process.exitCode = 1;
});

child.on("exit", (code, signal) => {
  if (signal) {
    process.kill(process.pid, signal);
    return;
  }
  process.exitCode = code ?? 1;
});
