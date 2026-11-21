import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createInterface } from "node:readline";

const binary = process.env.NANOFLARE_TEST_WORKERD || "workerd";
assert.equal(
  execFileSync(binary, ["--version"], { encoding: "utf8" }).trim(),
  "workerd 2026-09-11",
);

const childSource = `
let requests = 0;
export default { fetch() { return Response.json(++requests); } };
`;

const parentSource = `
import { RpcTarget } from "cloudflare:workers";
let callbacks = 0;

function code() {
  callbacks++;
  return {
    compatibilityDate: "2026-09-08",
    mainModule: "child.js",
    modules: { "child.js": ${JSON.stringify(childSource)} },
  };
}

class CodeCallback extends RpcTarget {
  code() { return this.callback(); }
  constructor(callback) {
    super();
    this.callback = callback;
  }
}

function loader(bridge) {
  return {
    get(id, callback) {
      return { getEntrypoint: () => ({ fetch: request =>
        bridge.fetchCached(id, new CodeCallback(callback), request) }) };
    },
    load(code) {
      return { getEntrypoint: () => ({ fetch: request =>
        bridge.fetchFresh(code, request) }) };
    },
  };
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const binding = loader(url.searchParams.has("secondary") ? env.SECONDARY : env.PRIMARY);
    const worker = url.searchParams.has("fresh") ? binding.load(code()) : binding.get("shared-id", code);
    const response = await worker.getEntrypoint().fetch(request);
    return Response.json({ revision: env.REVISION, requests: await response.json(), callbacks });
  },
};
`;

const dispatcherSource = `
import { WorkerEntrypoint } from "cloudflare:workers";
let alphaRevision = "alpha-v1";

export class LoaderBridge extends WorkerEntrypoint {
  fetchCached(id, callback, request) {
    const { revision, binding } = this.ctx.props;
    const key = JSON.stringify([revision, binding, id]);
    const worker = this.env.CHILDREN.get(key, async () => ({
      ...await callback.code(),
      globalOutbound: null,
    }));
    return worker.getEntrypoint().fetch(request);
  }

  fetchFresh(code, request) {
    return this.env.CHILDREN.load({ ...code, globalOutbound: null })
      .getEntrypoint().fetch(request);
  }
}

export default {
  fetch(request, env, ctx) {
    const path = new URL(request.url).pathname;
    if (request.method === "POST" && path === "/publish-alpha-v2") {
      alphaRevision = "alpha-v2";
      return new Response(null, { status: 204 });
    }
    const revision = path === "/alpha" ? alphaRevision : "beta-v1";
    const parent = env.PUBLICATIONS.get(revision, () => ({
      compatibilityDate: "2026-09-08",
      mainModule: "parent.js",
      modules: { "parent.js": ${JSON.stringify(parentSource)} },
      env: {
        REVISION: revision,
        PRIMARY: ctx.exports.LoaderBridge({ props: { revision, binding: "PRIMARY" } }),
        SECONDARY: ctx.exports.LoaderBridge({ props: { revision, binding: "SECONDARY" } }),
      },
      globalOutbound: null,
    }));
    return parent.getEntrypoint().fetch(request);
  },
};
`;

const config = `using Workerd = import "/workerd/workerd.capnp";
const config :Workerd.Config = (
  services = [(name = "dispatcher", worker = (
    compatibilityDate = "2026-09-08",
    compatibilityFlags = ["experimental"],
    modules = [(name = "dispatcher.js", esModule = ${JSON.stringify(dispatcherSource)})],
    bindings = [(name = "PUBLICATIONS", workerLoader = ()),
                (name = "CHILDREN", workerLoader = ())],
  ))],
  sockets = [(name = "http", address = "127.0.0.1:0", http = (), service = "dispatcher")],
);`;

const directory = await mkdtemp(join(tmpdir(), "nanoflare-loader-bridge-"));
let processHandle;
let closed;
try {
  const configPath = join(directory, "bridge.capnp");
  await writeFile(configPath, config);
  processHandle = spawn(binary, ["serve", "--experimental", "--control-fd=3", configPath], {
    stdio: ["ignore", "inherit", "inherit", "pipe"],
  });
  closed = new Promise((resolve) => processHandle.once("close", resolve));
  const control = createInterface({ input: processHandle.stdio[3] });
  const [line] = await once(control, "line", { signal: AbortSignal.timeout(5000) });
  const { port } = JSON.parse(line);
  assert.ok(port, line);
  const url = `http://127.0.0.1:${port}`;

  async function expect(path, revision, requests, callbacks) {
    const response = await fetch(url + path, { signal: AbortSignal.timeout(5000) });
    const body = await response.text();
    assert.equal(response.status, 200, body);
    assert.deepEqual(JSON.parse(body), { revision, requests, callbacks }, path);
  }

  await expect("/alpha", "alpha-v1", 1, 1);
  await expect("/alpha", "alpha-v1", 2, 1);
  await expect("/beta", "beta-v1", 1, 1);
  await expect("/alpha?secondary", "alpha-v1", 1, 2);
  await expect("/alpha?secondary", "alpha-v1", 2, 2);
  await expect("/alpha?fresh", "alpha-v1", 1, 3);
  await expect("/alpha?fresh", "alpha-v1", 1, 4);

  const publication = await fetch(url + "/publish-alpha-v2", {
    method: "POST",
    signal: AbortSignal.timeout(5000),
  });
  assert.equal(publication.status, 204);
  await expect("/alpha", "alpha-v2", 1, 1);
  await expect("/beta", "beta-v1", 2, 1);
  console.log("PASS cached and fresh child execution, binding isolation, and peer continuity");
} finally {
  if (processHandle) {
    processHandle.kill("SIGKILL");
    await closed;
  }
  await rm(directory, { recursive: true, force: true });
}
