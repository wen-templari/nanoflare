import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import vm from "node:vm";

import { errorReporter, executionFailure } from "../../../internal/runtimeassets/assets/errors.js";
import { privateDynamicProfile } from "../lib/vite.mjs";

test("private local profile rejects unsupported APIs and keeps signals", async () => {
  const source = (
    await readFile(new URL("../lib/runtime-profile.mjs", import.meta.url), "utf8")
  ).replaceAll("export function", "function");
  class Cache {
    match() {}
    put() {}
    delete() {}
  }
  class CacheStorage {
    get default() {
      return new Cache();
    }
    open() {}
  }
  const calls = [];
  const websocketCalls = [];
  const context = vm.createContext({
    Request,
    Response,
    URL,
    TypeError,
    caches: new CacheStorage(),
    WebSocket: class { constructor(url) { websocketCalls.push(url); } },
    fetch: async (request) => {
      calls.push(request);
      return new Response("ok");
    },
  });
  vm.runInContext(source + "\ninstallPrivateDynamicProfile();", context);
  await assert.rejects(
    context.fetch("https://example.test", { cf: { cacheTtl: 60 } }),
    /Custom fetch\/cache/,
  );
  assert.throws(() => vm.runInContext("caches.default", context), /Cache API is unavailable/);
  assert.throws(() => Cache.prototype.match(), /Cache API is unavailable/);
  await assert.rejects(
    context.fetch("https://example.test", { headers: { Upgrade: "websocket" } }),
    /Outbound WebSockets/,
  );
  for (const url of ["ws://example.test", "wss://example.test"]) {
    assert.throws(() => vm.runInContext(`new WebSocket(${JSON.stringify(url)})`, context), /Outbound WebSockets/);
  }
  assert.throws(() => vm.runInContext("new WebSocketPair()", context), /Outbound WebSockets/);
  assert.equal(websocketCalls.length, 0);
  assert.equal(Object.getOwnPropertyDescriptor(context, "WebSocket").configurable, false);
  const controller = new AbortController();
  assert.equal(
    await (await context.fetch("https://example.test", { signal: controller.signal })).text(),
    "ok",
  );
  controller.abort();
  assert.equal(calls[0].signal.aborted, true);
  vm.runInContext("installPrivateDynamicProfile();", context); // Safe in deployed bundles too.
});

test("Vite profile instruments only the selected Worker entry", () => {
  const plugin = privateDynamicProfile({ entry: "src/worker.ts" });
  plugin.configResolved({ root: "/project" });
  assert.equal(plugin.transform("browser", "/project/src/main.ts"), undefined);
  assert.match(
    plugin.transform("worker", "/project/src/worker.ts?worker").code,
    /^import .*runtime-profile-bootstrap.mjs/,
  );
});

test("exception reporting is bounded, deduplicated and cannot replace failures", async () => {
  const lines = [];
  const report = errorReporter(async (level, message) => {
    lines.push({ level, message });
    throw new Error("collector unavailable");
  }, "reference");
  const original = new TypeError("original");
  await report(original, "fetch");
  await report(original, "waitUntil");
  assert.deepEqual(lines, [{ level: "error", message: "fetch [reference]: TypeError: original" }]);
  await report(new Error("x".repeat(5000)), "stream");
  assert.ok(new TextEncoder().encode(lines[1].message).byteLength <= 4096);
  const response = executionFailure("reference");
  assert.equal(response.status, 500);
  assert.equal(response.headers.get("X-Nanoflare-Error-Id"), "reference");
  assert.doesNotMatch(await response.text(), /original|collector unavailable/);
});
