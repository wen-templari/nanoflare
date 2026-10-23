import assert from "node:assert/strict";
import { test } from "node:test";

import { retryGraphqlFetch } from "./release-please.mjs";

const url = "https://api.github.com/graphql";
const options = {
  method: "POST",
  body: JSON.stringify({ query: "query { repository { name } }" }),
};
const internalError = () =>
  Response.json({ errors: [{ message: "Something went wrong while executing your query." }] });

test("retries an HTTP 200 internal error and preserves the successful body", async () => {
  let calls = 0;
  const waits = [];
  const fetch = retryGraphqlFetch(
    async () => (++calls === 1 ? internalError() : Response.json({ data: { ok: true } })),
    {
      sleep: async (ms) => waits.push(ms),
      warn() {},
    },
  );
  assert.deepEqual(await (await fetch(url, options)).json(), { data: { ok: true } });
  assert.equal(calls, 2);
  assert.deepEqual(waits, [5000]);
});

test("bounds gateway retries and returns the final failure for Release Please to reject", async () => {
  for (const status of [500, 502, 503, 504]) {
    let calls = 0;
    const waits = [];
    const fetch = retryGraphqlFetch(
      async () => {
        calls++;
        return new Response("gateway error", { status });
      },
      {
        sleep: async (ms) => waits.push(ms),
        warn() {},
      },
    );
    assert.equal((await fetch(url, options)).status, status);
    assert.equal(calls, 4);
    assert.deepEqual(waits, [5000, 10000, 20000]);
  }
});

test("does not retry authentication, rate limits, or invalid queries", async () => {
  for (const response of [
    new Response("unauthorized", { status: 401 }),
    new Response("limited", { status: 429 }),
    Response.json({ errors: [{ message: "Field does not exist" }] }),
    Response.json({
      errors: [
        { message: "Something went wrong while executing your query." },
        { message: "Rate limited" },
      ],
    }),
  ]) {
    let calls = 0;
    const fetch = retryGraphqlFetch(async () => {
      calls++;
      return response;
    });
    assert.equal(await fetch(url, options), response);
    assert.equal(calls, 1);
  }
});

test("never retries mutations or REST writes", async () => {
  for (const [target, request] of [
    [url, { ...options, body: JSON.stringify({ query: "mutation { createRelease { id } }" }) }],
    ["https://api.github.com/repos/owner/repo/releases", { method: "POST", body: "{}" }],
  ]) {
    let calls = 0;
    const fetch = retryGraphqlFetch(async () => {
      calls++;
      return internalError();
    });
    await fetch(target, request);
    assert.equal(calls, 1);
  }
});
