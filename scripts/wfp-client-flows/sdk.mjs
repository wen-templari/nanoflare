import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

// Invoked by the disposable Go fixture. Never uses Cloudflare credentials or a
// Cloudflare network endpoint, including when those exist in the shell.
const packageDir = process.env.NANOFLARE_TEST_CF_SDK_DIR;
const version = JSON.parse(await readFile(resolve(packageDir, "package.json"), "utf8")).version;
assert.equal(version, "7.3.0", "requalify this test before changing SDK versions");
const { default: Cloudflare } = await import(pathToFileURL(resolve(packageDir, "index.mjs")));
const origin = new URL(process.env.NF_SDK_FIXTURE_URL).origin;
assert.ok(["127.0.0.1", "[::1]"].includes(new URL(origin).hostname));
const wireParts = [];
const wireTypes = [];
const client = new Cloudflare({
  apiToken: process.env.NF_SDK_FIXTURE_TOKEN,
  apiEmail: null,
  apiKey: null,
  baseURL: `${origin}/client/v4`,
  maxRetries: 0,
  fetch: async (input, init) => {
    if (String(input) === "data:,") return new Response("");
    const request = new Request(input, init);
    assert.equal(new URL(request.url).origin, origin, "SDK attempted non-fixture network access");
    if (init?.body instanceof FormData) {
      wireParts.push([...init.body.keys()]);
      wireTypes.push(request.headers.get("content-type"));
    } else if (request.headers.get("content-type")?.startsWith("multipart/form-data")) {
      wireParts.push([...(await request.clone().formData()).keys()]);
      wireTypes.push(request.headers.get("content-type"));
    }
    return fetch(request);
  },
});
const account_id = process.env.NF_SDK_FIXTURE_ORG;
const namespaces = client.workersForPlatforms.dispatch.namespaces;
const scripts = namespaces.scripts;
const name = "sdk-flow";
await namespaces.create({ account_id, name });
const listed = [];
for await (const namespace of namespaces.list({ account_id }))
  listed.push(namespace.namespace_name);
assert.ok(listed.includes(name));
assert.equal((await namespaces.get(name, { account_id })).namespace_name, name);
const params = { account_id, dispatch_namespace: name };
const code = "export default {fetch(){return new Response('sdk-code')}}";

// Preserve evidence of the generated default serializer instead of teaching the
// server to interpret ambiguous bracketed fields as a different wire contract.
await assert.rejects(
  scripts.update("alpha", {
    ...params,
    metadata: { main_module: "worker.js" },
    files: [new File([code], "worker.js", { type: "application/javascript+module" })],
  }),
  (error) => error.status === 422,
);
assert.ok(
  wireParts.some((parts) => parts.includes("metadata[main_module]") && parts.includes("files[]")),
);
assert.equal(wireTypes[0], "application/javascript");

function multipart(metadata, source = code) {
  const form = new FormData();
  form.set(
    "metadata",
    new File([JSON.stringify(metadata)], "metadata.json", { type: "application/json" }),
  );
  form.set("worker.js", new File([source], "worker.js", { type: "application/javascript+module" }));
  return form;
}
const first = await scripts.update(
  "alpha",
  { ...params, metadata: {} },
  {
    body: multipart({ main_module: "worker.js", compatibility_date: "2025-12-10" }),
    headers: { "Content-Type": null, "Idempotency-Key": "sdk-first" },
  },
);
assert.equal(first.id, "alpha");
assert.equal(first.nanoflare_runtime_ready_verified, false);
assert.equal((await scripts.get("alpha", params)).script.id, "alpha");
const content = await scripts.content.get("alpha", params);
assert.equal(content.headers.get("CF-WORKER-MAIN-MODULE-PART"), "worker.js");
assert.equal(await (await content.formData()).get("worker.js").text(), code);
await scripts.secrets.update("alpha", {
  ...params,
  name: "TOKEN",
  type: "secret_text",
  text: "fixture-only-value",
});
assert.equal(
  (await scripts.secrets.get("TOKEN", { ...params, script_name: "alpha" })).name,
  "TOKEN",
);
const secrets = [];
for await (const secret of scripts.secrets.list("alpha", params)) secrets.push(secret);
assert.equal(secrets.length, 1);
assert.equal(secrets[0].text, undefined);
const bulk = await scripts.secrets.bulkUpdate("alpha", {
  ...params,
  secrets: {
    TOKEN: null,
    SECOND: { name: "SECOND", type: "secret_text", text: "bulk-fixture-value" },
  },
});
assert.equal(bulk.TOKEN, undefined);
assert.equal(bulk.SECOND.name, "SECOND");
assert.equal(bulk.SECOND.text, undefined);
await scripts.tags.update("alpha", { ...params, body: ["sdk"] });
const tags = [];
for await (const tag of scripts.tags.list("alpha", params)) tags.push(tag);
assert.deepEqual(tags, ["sdk"]);
await scripts.tags.delete("sdk", { ...params, script_name: "alpha" });

await assert.rejects(
  scripts.settings.edit("alpha", { ...params, settings: { tags: ["new"] } }),
  (error) => error.status === 422,
);
await scripts.settings.edit("alpha", {
  ...params,
  settings: new File([JSON.stringify({ tags: ["new"] })], "settings.json", {
    type: "application/json",
  }),
});
assert.deepEqual((await scripts.settings.get("alpha", params)).tags, ["new"]);

const asset = Buffer.from("SDK uploaded asset");
const hash = createHash("sha256").update(asset).digest("hex").slice(0, 32);
const upload = await scripts.assetUpload.create("alpha", {
  ...params,
  manifest: { "/hello.txt": { hash, size: asset.length } },
});
assert.deepEqual(upload.buckets, [[hash]]);
const completion = await client.workers.assets.upload.create(
  { account_id, base64: true, body: { [hash]: asset.toString("base64") } },
  { headers: { Authorization: `Bearer ${upload.jwt}` } },
);
assert.ok(completion.jwt);
await scripts.update(
  "alpha",
  { ...params, metadata: {} },
  {
    body: multipart({
      main_module: "worker.js",
      compatibility_date: "2025-12-10",
      assets: { jwt: completion.jwt },
      bindings: [{ name: "ASSETS", type: "assets" }],
    }),
    headers: { "Content-Type": null, "Idempotency-Key": "sdk-assets" },
  },
);
const bindings = [];
for await (const binding of scripts.bindings.get("alpha", params)) bindings.push(binding);
assert.ok(bindings.some((binding) => binding.name === "ASSETS"));
await scripts.delete("alpha", params);
await namespaces.delete(name, { account_id });
console.log(
  JSON.stringify({
    version,
    json_operations: "passed",
    explicit_multipart: "passed",
    assets: "passed",
    generated_multipart: "rejected",
    generated_upload_parts: wireParts[0],
    generated_upload_content_type: wireTypes[0],
  }),
);
