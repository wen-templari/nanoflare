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
// SDK 7 omits array indices. These two valid inherit arrays differ in which
// binding receives old_name, but serialize to exactly the same scalar fields.
const { createForm } = await import(pathToFileURL(resolve(packageDir, "internal/uploads.mjs")));
const ambiguousA = [
  { name: "A", type: "inherit" },
  { old_name: "SOURCE", name: "B", type: "inherit" },
];
const ambiguousB = [
  { name: "A", type: "inherit", old_name: "SOURCE" },
  { name: "B", type: "inherit" },
];
assert.notDeepEqual(ambiguousA, ambiguousB);
assert.deepEqual(
  [...(await createForm({ metadata: { bindings: ambiguousA } }, fetch)).entries()],
  [...(await createForm({ metadata: { bindings: ambiguousB } }, fetch)).entries()],
);
for (const bindings of [ambiguousA, ambiguousB]) {
  await assert.rejects(
    scripts.update("ambiguous", {
      ...params,
      metadata: { main_module: "worker.js", bindings },
      files: [
        new File(["export default {}"], "worker.js", { type: "application/javascript+module" }),
      ],
    }),
    (error) => error.status === 422,
  );
}

const code = "export default {fetch(){return new Response('sdk-code')}}";

// Exercise the generated serializer without request-header or body overrides.
await scripts.update("alpha", {
  ...params,
  metadata: {
    main_module: "worker.js",
    compatibility_date: "2025-12-10",
    tags: ["generated"],
    keep_assets: false,
  },
  files: [new File([code], "worker.js", { type: "application/javascript+module" })],
});
assert.deepEqual((await scripts.settings.get("alpha", params)).tags, ["generated"]);
assert.ok(
  wireParts.some((parts) => parts.includes("metadata[main_module]") && parts.includes("files[]")),
);
assert.equal(wireTypes[0], "application/javascript");

function jsonMetadata(metadata) {
  return new File([JSON.stringify(metadata)], "metadata.json", { type: "application/json" });
}
const first = await scripts.update(
  "alpha",
  {
    ...params,
    metadata: jsonMetadata({ main_module: "worker.js", compatibility_date: "2025-12-10" }),
    files: [new File([code], "worker.js", { type: "application/javascript+module" })],
  },
  { headers: { "Idempotency-Key": "sdk-first" } },
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

await scripts.settings.edit("alpha", { ...params, settings: { tags: ["sdk-default", "second"] } });
assert.deepEqual((await scripts.settings.get("alpha", params)).tags, ["sdk-default", "second"]);
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
// Default SDK upload can attach assets without a named ASSETS binding.
await scripts.update("alpha", {
  ...params,
  metadata: {
    main_module: "worker.js",
    compatibility_date: "2025-12-10",
    assets: {
      jwt: completion.jwt,
      config: {
        html_handling: "auto-trailing-slash",
        not_found_handling: "404-page",
        run_worker_first: ["/api/*", "/rpc/*"],
      },
    },
  },
  files: [new File([code], "worker.js", { type: "application/javascript+module" })],
});
await scripts.update(
  "alpha",
  {
    ...params,
    metadata: jsonMetadata({
      main_module: "worker.js",
      compatibility_date: "2025-12-10",
      assets: { jwt: completion.jwt },
      bindings: [{ name: "ASSETS", type: "assets" }],
    }),
    files: [new File([code], "worker.js", { type: "application/javascript+module" })],
  },
  { headers: { "Idempotency-Key": "sdk-assets" } },
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
    sdk_json_metadata_file: "passed",
    assets: "passed",
    generated_multipart: "passed",
    binding_array_ambiguity: "reproduced_and_rejected",
    generated_upload_parts: wireParts[0],
    generated_upload_content_type: wireTypes[0],
  }),
);
