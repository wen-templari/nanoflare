import assert from "node:assert/strict";
import { test } from "node:test";

import { runBackend, compareObservations } from "./backend.mjs";
function fixture(failInvoke = false) {
  const calls = [];
  let binding;
  const fetcher = async (input, init = {}) => {
    const url = new URL(input);
    calls.push({ path: url.pathname, method: init.method ?? "GET" });
    if (url.hostname === "invoke.test") {
      if (failInvoke) throw new Error("invoke failed");
      return Response.json(
        { context: JSON.parse(url.searchParams.get("context")), missing: null },
        { status: url.pathname === "/block" ? 403 : 200 },
      );
    }
    if (init.method === "PUT") {
      const metadata = JSON.parse(await init.body.get("metadata").text());
      if (metadata.bindings.length) binding = metadata.bindings[0];
    }
    if (!init.method && !url.pathname.endsWith("/settings"))
      return new Response("", { status: 404 });
    return Response.json({
      success: true,
      result: url.pathname.endsWith("/settings") ? { bindings: [binding] } : {},
    });
  };
  return { calls, fetcher };
}
const config = {
  baseURL: "https://api.test/client/v4",
  account: "account",
  token: "secret",
  invokeURL: "https://invoke.test/{script}",
  prefix: "nf-fixture",
};
test("same backend deploys, reads selectors, invokes value cases and cleans up", async () => {
  const mock = fixture(),
    result = await runBackend({ ...config, ...mock });
  assert.equal(result.status, "passed");
  assert.equal(result.observations["parameter-json"].length, 5);
  assert.deepEqual(
    mock.calls.filter((c) => c.method === "DELETE").map((c) => c.path.split("/").pop()),
    ["nf-fixture-dispatcher", "nf-fixture-policy", "nf-fixture"],
  );
  assert.ok(!JSON.stringify(result).includes("secret"));
  assert.ok(compareObservations(result, result).every((c) => c.status === "equal"));
  assert.equal(
    compareObservations(result, { observations: { "policy-block": { status: 200 } } }).find(
      (c) => c.id === "policy-block",
    ).status,
    "different",
  );
});
test("invocation failure still deletes created resources", async () => {
  const mock = fixture(true);
  await assert.rejects(runBackend({ ...config, ...mock }), /invoke failed/);
  assert.equal(mock.calls.filter((c) => c.method === "DELETE").length, 3);
});
test("preexisting target is never overwritten", async () => {
  let writes = 0;
  await assert.rejects(
    runBackend({
      ...config,
      fetcher: async () => {
        writes++;
        return Response.json({ success: true });
      },
    }),
    /must not already exist/,
  );
  assert.equal(writes, 1);
});

test("cleanup errors are a failed backend outcome", async () => {
  const mock = fixture();
  const original = mock.fetcher;
  mock.fetcher = async (input, init) =>
    init?.method === "DELETE" ? new Response("", { status: 500 }) : original(input, init);
  const result = await runBackend({ ...config, ...mock });
  assert.equal(result.status, "cleanup-failed");
  assert.equal(result.cleanupErrors.length, 3);
});
