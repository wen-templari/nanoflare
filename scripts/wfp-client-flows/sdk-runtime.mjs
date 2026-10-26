import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

const packageDir = process.env.NANOFLARE_TEST_CF_SDK_DIR;
assert.equal(
  JSON.parse(await readFile(resolve(packageDir, "package.json"), "utf8")).version,
  "7.3.0",
);
const { default: Cloudflare } = await import(pathToFileURL(resolve(packageDir, "index.mjs")));
const origin = new URL(process.env.NF_SDK_FIXTURE_URL).origin;
assert.ok(["127.0.0.1", "[::1]"].includes(new URL(origin).hostname));
const localFetch = async (input, init) => {
  if (String(input) === "data:,") return new Response("");
  const request = new Request(input, init);
  assert.equal(new URL(request.url).origin, origin);
  return fetch(request, { redirect: "error" });
};
const client = new Cloudflare({
  apiToken: process.env.NF_SDK_FIXTURE_TOKEN,
  apiEmail: null,
  apiKey: null,
  baseURL: `${origin}/client/v4`,
  maxRetries: 0,
  fetch: localFetch,
});
const account_id = process.env.NF_SDK_FIXTURE_ORG;
const namespaces = client.workersForPlatforms.dispatch.namespaces;
const scripts = namespaces.scripts;
const namespace = "sdk-runtime";
const params = { account_id, dispatch_namespace: namespace };
const file = (code) => new File([code], "worker.js", { type: "application/javascript+module" });
const metadata = (value) =>
  new File([JSON.stringify(value)], "metadata.json", { type: "application/json" });
const code = (version) =>
  `export default {fetch(request, env) {return Response.json({version:${JSON.stringify(version)},secret:env.TOKEN});}}`;
await namespaces.create({ account_id, name: namespace });
await scripts.update("customer", {
  ...params,
  metadata: { main_module: "worker.js", compatibility_date: "2025-12-10" },
  files: [file(code("v1"))],
});
await scripts.secrets.update("customer", {
  ...params,
  name: "TOKEN",
  type: "secret_text",
  text: "sdk-runtime-secret",
});
const dispatcher = await client.workers.scripts.update("sdk-dispatcher", {
  account_id,
  metadata: metadata({
    main_module: "worker.js",
    compatibility_date: "2025-12-10",
    bindings: [{ name: "CUSTOMERS", type: "dispatch_namespace", namespace }],
  }),
  files: [
    file(
      'export default {fetch(request,env){return env.CUSTOMERS.get("customer").fetch(request)}}',
    ),
  ],
});
assert.ok(dispatcher.nanoflare_worker_id);
const invoke = async (path) => {
  const response = await localFetch(
    `${origin}/internal/http/workers/${dispatcher.nanoflare_worker_id}${path}`,
  );
  const text = await response.text();
  assert.equal(response.status, 200, text);
  return text;
};
assert.deepEqual(JSON.parse(await invoke("/api/check")), {
  version: "v1",
  secret: "sdk-runtime-secret",
});
const asset = Buffer.from("SDK runtime asset");
const hash = createHash("sha256").update(asset).digest("hex").slice(0, 32);
const session = await scripts.assetUpload.create("customer", {
  ...params,
  manifest: { "/asset.txt": { hash, size: asset.length } },
});
const completion = await client.workers.assets.upload.create(
  { account_id, base64: true, body: { [hash]: asset.toString("base64") } },
  { headers: { Authorization: `Bearer ${session.jwt}` } },
);
await scripts.update("customer", {
  ...params,
  metadata: {
    main_module: "worker.js",
    compatibility_date: "2025-12-10",
    keep_bindings: ["secret_text"],
    assets: { jwt: completion.jwt, config: { run_worker_first: ["/api/*"] } },
  },
  files: [file(code("v2"))],
});
assert.deepEqual(JSON.parse(await invoke("/api/check")), {
  version: "v2",
  secret: "sdk-runtime-secret",
});
assert.equal(await invoke("/asset.txt"), asset.toString());
await scripts.secrets.update("customer", {
  ...params,
  name: "TOKEN",
  type: "secret_text",
  text: "updated-secret",
});
assert.deepEqual(JSON.parse(await invoke("/api/check")), {
  version: "v2",
  secret: "updated-secret",
});
assert.equal(await invoke("/asset.txt"), asset.toString());
// SDK 7 serializes metadata and files itself, including raw binary modules.
const wasmBytes = (answer) =>
  Buffer.from(
    `0061736d010000000105016000017f030201000707010372756e00000a0601040041${answer.toString(16).padStart(2, "0")}0b00040178fffe`,
    "hex",
  );
const wasmCode =
  'import module from "./answer.wasm";const instance=new WebAssembly.Instance(module);export default {fetch(request,env){return Response.json({version:"wasm",secret:env.TOKEN,answer:instance.exports.run()})}}';
const wasmFiles = (answer) => [
  file(wasmCode),
  new File([wasmBytes(answer)], "answer.wasm", { type: "application/wasm" }),
];
const pause = () => new Promise((resolve) => setTimeout(resolve, 1600));
const binaryContent = async (answer) => {
  const response = await scripts.content.get("customer", params);
  assert.equal(response.headers.get("CF-WORKER-MAIN-MODULE-PART"), "worker.js");
  const modules = await response.formData();
  const binary = modules.get("answer.wasm");
  assert.equal(binary.type, "application/wasm");
  assert.deepEqual(Buffer.from(await binary.arrayBuffer()), wasmBytes(answer));
  assert.equal(await modules.get("worker.js").text(), wasmCode);
};
await pause();
await scripts.update("customer", {
  ...params,
  metadata: {
    main_module: "worker.js",
    compatibility_date: "2025-12-10",
    keep_bindings: ["secret_text"],
    keep_assets: true,
  },
  files: wasmFiles(42),
});
assert.deepEqual(JSON.parse(await invoke("/api/check")), {
  version: "wasm",
  secret: "updated-secret",
  answer: 42,
});
assert.equal(await invoke("/asset.txt"), asset.toString());
await binaryContent(42);
await pause();
await scripts.content.update("customer", {
  ...params,
  metadata: { main_module: "worker.js" },
  files: wasmFiles(7),
});
assert.deepEqual(JSON.parse(await invoke("/api/check")), {
  version: "wasm",
  secret: "updated-secret",
  answer: 7,
});
assert.equal(await invoke("/asset.txt"), asset.toString());
await binaryContent(7);
await pause();
await scripts.secrets.update("customer", {
  ...params,
  name: "TOKEN",
  type: "secret_text",
  text: "wasm-secret",
});
assert.deepEqual(JSON.parse(await invoke("/api/check")), {
  version: "wasm",
  secret: "wasm-secret",
  answer: 7,
});
assert.equal(await invoke("/asset.txt"), asset.toString());
await binaryContent(7);
await scripts.delete("customer", params);
await namespaces.delete(namespace, { account_id });
console.log(
  JSON.stringify({
    sdk: "7.3.0",
    runtime_dispatch: "passed",
    wasm_upload_execute_download: "passed",
    wasm_content_update: "passed",
    wasm_secret_asset_retention: "passed",
    source_update: "passed",
    secret_update: "passed",
    assets: "passed",
    namespace_cleanup: "passed",
    dispatcher_metadata: "JSON File adaptation",
  }),
);
