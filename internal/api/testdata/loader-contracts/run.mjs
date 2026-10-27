import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";
import { readFile, writeFile, mkdtemp, rm } from "node:fs/promises";
import { createServer, get as httpGet } from "node:http";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(fileURLToPath(import.meta.url));
const binary = process.env.WORKERD_BIN;
if (!binary || !process.env.NANOFLARE_LOADER_FIXTURE)
  throw new Error("run through TestWorkerdDynamicLoaderContracts with NANOFLARE_TEST_WORKERD set");
const fixture = JSON.parse(await readFile(process.env.NANOFLARE_LOADER_FIXTURE, "utf8"));
const version = spawnSync(binary, ["--version"], { encoding: "utf8" });
if (version.error || version.status !== 0) throw version.error || new Error(version.stderr);
const events = [];
const server = createServer(async (request, response) => {
  if (request.url.startsWith("/disconnect/")) {
    response.writeHead(200, { "content-type": "text/plain" });
    response.write("started");
    const timer = setInterval(() => response.write("heartbeat"), 20);
    response.on("close", () => {
      clearInterval(timer);
      events.push({ path: "/disconnect-closed", body: request.url.split("/").pop() });
    });
    return;
  }
  let body = "";
  for await (const chunk of request) body += chunk;
  events.push({ path: request.url, body });
  response.end("ok");
});
server.listen(0, "127.0.0.1");
await once(server, "listening");
const eventsAddr = `127.0.0.1:${server.address().port}`;
const temp = await mkdtemp(join(tmpdir(), "nanoflare-loader-contracts-"));
const host = await readFile(join(root, "host.js"), "utf8");
const external = (name, headers) =>
  `(name = ${JSON.stringify(name)}, external = (address = ${JSON.stringify(fixture.backend)}, http = (injectRequestHeaders = [${Object.entries(
    { Authorization: `Bearer ${fixture.token}`, ...headers },
  )
    .map(([name, value]) => `(name = ${JSON.stringify(name)}, value = ${JSON.stringify(value)})`)
    .join(",")}]))),`;
const config = `using Workerd = import "/workerd/workerd.capnp";
const config :Workerd.Config = (
  services = [
    (name = "host", worker = (
      compatibilityDate = "2026-09-08",
      compatibilityFlags = ["experimental", "enable_abortsignal_rpc"],
      modules = [(name = "host.js", esModule = ${JSON.stringify(host)})],
      bindings = [(name = "LOADER", workerLoader = (id = "contracts")),
                  (name = "EVENTS", service = "events"),
                  (name = "DNS", service = "dns-backend"),
                  (name = "EGRESS", service = "egress-backend"),
                  (name = "KV", kvNamespace = "kv-backend"),
                  (name = "KV_BACKEND", service = "kv-backend"),
                  (name = "DB", service = "db-backend"),
                  (name = "OBJECTS", service = "objects-backend"),
                  (name = "ASSETS", service = "assets-backend"),
                  (name = "APP_ID", text = ${JSON.stringify(fixture.appID)}),
                  (name = "DEPLOYMENT_ID", text = ${JSON.stringify(fixture.deploymentID)})],
    )),
    (name = "events", external = (address = "${eventsAddr}", http = ())),
    (name = "dns-backend", external = (address = ${JSON.stringify(fixture.dnsAddr)}, http = (injectRequestHeaders = [(name = "X-Nanoflare-DNS-Profile", value = "loader-private")]))),
    (name = "egress-backend", external = (address = ${JSON.stringify(fixture.egressAddr)}, http = (style = proxy))),
    ${external("kv-backend", { "X-Nanoflare-KV-Namespace-ID": fixture.namespaceID })}
    ${external("db-backend", { "X-Nanoflare-Database-ID": fixture.databaseID })}
    ${external("objects-backend", { "X-Nanoflare-Object-Bucket-ID": fixture.bucketID })}
    ${external("assets-backend", { "X-Nanoflare-Deployment-ID": fixture.deploymentID })}
  ],
  sockets = [(name = "http", address = "127.0.0.1:0", http = (), service = "host")],
);`;
await writeFile(join(temp, "config.capnp"), config);
const child = spawn(
  binary,
  ["serve", "--experimental", "--control-fd=3", join(temp, "config.capnp")],
  { stdio: ["ignore", "pipe", "pipe", "pipe"] },
);
let logs = "";
child.stdout.on("data", (chunk) => (logs += chunk));
child.stderr.on("data", (chunk) => (logs += chunk));
const results = [];
const sockets = [];
let base;
const check = async (name, operation) => {
  try {
    const detail = await operation();
    results.push({ name, passed: true, detail });
    console.log(`PASS ${name}`);
  } catch (error) {
    results.push({ name, passed: false, error: String(error) });
    console.log(`FAIL ${name}: ${error}`);
  }
};
const bounded = (promise) =>
  Promise.race([
    promise,
    new Promise((_, reject) => {
      const timer = setTimeout(() => reject(new Error("deadline exceeded")), 5000);
      timer.unref();
    }),
  ]);
try {
  const port = await bounded(
    new Promise((resolve, reject) => {
      let buffer = "";
      child.once("error", reject);
      child.once("exit", (code) => reject(new Error(`workerd exited ${code}: ${logs}`)));
      child.stdio[3].on("data", (chunk) => {
        buffer += chunk;
        const lines = buffer.split("\n");
        buffer = lines.pop();
        for (const line of lines) {
          const message = JSON.parse(line);
          if (message.port) resolve(message.port);
        }
      });
    }),
  );
  base = `http://127.0.0.1:${port}`;
  const publish = async (name, revision, source) => {
    const response = await fetch(base + "/publish", {
      method: "POST",
      body: JSON.stringify({
        name,
        revision,
        ...(typeof source === "string" ? { source } : { program: source }),
      }),
      signal: AbortSignal.timeout(5000),
    });
    assert.equal(response.status, 200, await response.text());
  };
  const get = async (path) => {
    const response = await fetch(base + path, { signal: AbortSignal.timeout(5000) });
    const text = await response.text();
    assert.equal(response.status, 200, text);
    return JSON.parse(text);
  };
  const identity = (label) => `import { WorkerEntrypoint, RpcTarget } from "cloudflare:workers";
    class Session extends RpcTarget { greet() { return "${label}:session"; } }
    export default class extends WorkerEntrypoint {
      getUser(id) { return { id, version: "${label}" }; }
      session() { return new Session(); }
      async fetch() { return Response.json(this.getUser("http")); }
    }`;
  const caller = `export default { async fetch(request, env) {
    if (new URL(request.url).pathname === "/nested") {
      using session = await env.IDENTITY.session(); return Response.json({ value: await session.greet() });
    }
    return Response.json(await env.IDENTITY.getUser("ada"));
  } };`;
  await publish("identity", "v1", identity("v1"));
  await publish("caller", "v1", caller);
  await check("direct dynamic entrypoint transfer is explicitly rejected", async () => {
    await assert.rejects(
      get("/caller/"),
      /Entrypoints to dynamically-loaded workers cannot be transferred/,
    );
  });
  await publish("identity", "v2", identity("v2"));
  await publish(
    "native-kv",
    "v1",
    `export default { async fetch(request, env) { return Response.json({ value: await env.KV.get("key") }); } };`,
  );
  await check("direct native KV transfer is explicitly rejected", async () => {
    await assert.rejects(get("/native-kv/"), /KV namespace binding cannot be serialized/);
  });
  await publish("bridged-caller", "v1", caller);
  await check("parent bridge preserves dynamic RPC on cold and warm caller", async () => {
    assert.deepEqual(await get("/bridged-caller/"), { id: "ada", version: "v2" });
    assert.deepEqual(await get("/bridged-caller/"), { id: "ada", version: "v2" });
  });
  await check("parent bridge forwards nested RPC result capabilities", async () => {
    assert.deepEqual(await get("/bridged-caller/nested"), { value: "v2:session" });
  });
  await publish("identity", "v3", identity("v3"));
  await check("parent bridge resolves new service revision for warm caller", async () => {
    assert.deepEqual(await get("/bridged-caller/"), { id: "ada", version: "v3" });
  });
  await publish(
    "bridged-kv",
    "v1",
    `export default { async fetch(request, env) { return Response.json({ value: await env.KV.get("key") }); } };`,
  );
  // This value is written through the real native KV bridge, not a mock backend.
  await publish(
    "bridged-kv",
    "seed",
    `export default { async fetch(request, env) { await env.KV.put("key", "ok"); return Response.json({ value: await env.KV.get("key") }); } };`,
  );
  await check("parent bridge exposes native KV get to dynamic Worker", async () => {
    assert.deepEqual(await get("/bridged-kv/"), { value: "ok" });
    assert.deepEqual(await get("/bridged-kv/"), { value: "ok" });
  });
  for (const [name, program] of Object.entries(fixture.programs)) {
    await publish(`resource-${name}`, "v1", program);
    await check(`Nanoflare generated ${name} wrapper uses real runtime backend`, async () => {
      if (name === "cron") {
        const response = await fetch(
          base + "/resource-cron/cdn-cgi/handler/scheduled?cron=" + encodeURIComponent("5 * * * *"),
        );
        assert.equal(response.status, 204, await response.text());
        await bounded(
          (async () => {
            while ((await get("/resource-cron/")).cron !== "5 * * * *")
              await new Promise((resolve) => setTimeout(resolve, 10));
          })(),
        );
        return;
      }
      if (name === "assets") {
        const response = await fetch(base + "/resource-assets/");
        assert.equal(response.status, 200);
        assert.equal(await response.text(), "<svg/>|<h1>loader</h1>");
        return;
      }
      const result = await get(`/resource-${name}/`);
      if (name === "kv")
        assert.deepEqual(result, {
          text: "hello",
          json: { ok: true },
          missing: null,
          firstKey: "grant:a",
          firstComplete: false,
          secondKey: "grant:b",
          secondComplete: true,
        });
      if (name === "legacy") {
        assert.equal(result.legacy, "ok");
        const scheduled = await fetch(
          base +
            "/resource-legacy/cdn-cgi/handler/scheduled?cron=" +
            encodeURIComponent("7 * * * *"),
        );
        assert.equal(scheduled.status, 204, await scheduled.text());
        const after = await get("/resource-legacy/");
        assert.equal(after.cron, "7 * * * *");
        assert.equal(after.background, "done");
      }
      if (name === "network")
        assert.deepEqual(result, {
          callback: "10.42.0.7",
          promise: { address: "10.42.0.7", family: 4 },
          egress: "proxy-egress",
        });
      if (name === "db") assert.deepEqual(result, { first: "hello", raw: [[1, "hello"]] });
      if (name === "objects") {
        assert.equal(result.created.key, "folder/demo.json");
        assert.equal(result.created.contentType, "application/json");
        assert.deepEqual(result.body, { ok: true });
        assert.equal(result.missing, true);
      }
      if (name === "rpc") assert.deepEqual(result, { id: "http", version: "compiled" });
      if (name === "logs") {
        assert.deepEqual(result, { logged: true });
        await bounded(
          (async () => {
            while (!logs.includes(`loader-contract-output`))
              await new Promise((resolve) => setTimeout(resolve, 10));
          })(),
        );
      }
    });
  }
  await publish(
    "tcp",
    "v1",
    `import { connect } from "cloudflare:sockets";
    export default { async fetch() {
      const socket = connect(${JSON.stringify(fixture.echoAddr)});
      const writer = socket.writable.getWriter(); await writer.write(new TextEncoder().encode("ping"));
      const reader = socket.readable.getReader(); const {value} = await reader.read();
      await reader.cancel(); await writer.close();
      return Response.json({ value: new TextDecoder().decode(value) });
    } };`,
  );
  await check("dynamic raw TCP connect uses configured egress NO_PROXY path", async () => {
    assert.deepEqual(await get("/tcp/"), { value: "pong" });
  });
  await publish("identity", "compiled", fixture.programs.rpc);
  await check("generated WorkerEntrypoint instrumentation preserves RPC", async () => {
    assert.deepEqual(await get("/bridged-caller/"), { id: "ada", version: "compiled" });
  });
  await check("generated wrappers emit Nanoflare duration telemetry", async () => {
    const deadline = Date.now() + 4000;
    while (
      !events.some(
        (event) =>
          event.path === "/internal/runtime/durations" && event.body.includes(fixture.appID),
      )
    ) {
      if (Date.now() > deadline) throw new Error("missing deployment-scoped telemetry");
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
  });

  const lifecycle = (label) => `export default { async fetch(request, env, ctx) {
    const path = new URL(request.url).pathname;
    if (path === "/background") {
      ctx.waitUntil((async () => { await new Promise(resolve => setTimeout(resolve, 150)); await env.EVENTS.fetch("https://events.invalid/background", { method: "POST", body: "${label}" }); })());
      return Response.json({ queued: "${label}" });
    }
    if (path === "/ws") {
      const [client, server] = Object.values(new WebSocketPair()); server.accept();
      server.addEventListener("message", event => server.send("${label}:" + event.data));
      server.addEventListener("close", () => server.close(1000, "done"));
      return new Response(null, { status: 101, webSocket: client });
    }
    if (path === "/upstream") return env.EVENTS.fetch("http://events.invalid/disconnect/dynamic");
    if (path === "/cancel") {
      let timer;
      return new Response(new ReadableStream({
        start(controller) { controller.enqueue(new TextEncoder().encode("started")); timer = setInterval(() => controller.enqueue(new TextEncoder().encode("heartbeat")), 20); },
        cancel() { clearInterval(timer); console.log("CANCELLED:${label}"); },
      }));
    }
    return Response.json({ version: "${label}" });
  } };`;
  await publish("lifecycle", "v1", lifecycle("v1"));
  const waitEvent = async (path, body) => {
    const deadline = Date.now() + 4000;
    while (!events.some((event) => event.path === path && event.body === body)) {
      if (Date.now() > deadline) throw new Error(`missing event ${path}:${body}`);
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
  };
  const wsOpen = async () => {
    const socket = new WebSocket(base.replace("http:", "ws:") + "/lifecycle/ws");
    sockets.push(socket);
    await bounded(
      new Promise((resolve, reject) => {
        socket.addEventListener("open", resolve, { once: true });
        socket.addEventListener("error", () => reject(new Error("WebSocket failed")), {
          once: true,
        });
      }),
    );
    return socket;
  };
  for (const [name, program] of Object.entries(fixture.invocationPrograms)) {
    await publish(`invocation-${name}`, "v1", program);
    if (name === "isolation") continue;
    await check(`invocation-scoped ${name} wrapper runs and completes`, async () => {
      const path = name === "cron" ? "/cdn-cgi/handler/scheduled?cron=9%20*%20*%20*%20*" : "/";
      const response = await fetch(base + `/invocation-${name}` + path);
      assert.equal(response.status, name === "cron" ? 204 : 200, await response.text());
      await waitEvent("/invocation-complete", `invocation-${name}`);
      assert.equal(
        events.filter(
          (event) => event.path === "/invocation-complete" && event.body === `invocation-${name}`,
        ).length,
        1,
      );
    });
  }
  await check(
    "concurrent invocations share revision resources and complete after nested waitUntil",
    async () => {
      await Promise.all(
        ["alpha", "beta"].map(async (label) => {
          const token = `invocation-${label}`;
          const response = await fetch(base + "/invocation-isolation/" + label, {
            headers: { "X-Test-Invocation": token },
          });
          assert.equal(response.status, 200, await response.clone().text());
          assert.deepEqual(await response.json(), {
            value: label,
            props: { publicValue: "safe" },
            hidden: true,
          });
          await waitEvent("/invocation-complete", token);
          const done = events.findIndex(
            (event) => event.path === "/invocation-complete" && event.body === token,
          );
          for (const path of ["/invocation-finally", "/invocation-nested"]) {
            const task = events.findIndex((event) => event.path === path && event.body === label);
            assert.ok(task >= 0 && task < done, `${path} must finish before completion`);
          }
          const calls = events
            .filter((event) => event.path === "/invocation-resource")
            .map((event) => JSON.parse(event.body))
            .filter((event) => event.path.includes(label));
          assert.equal(calls.length, 2);
          assert.ok(
            calls.every(
              (event) => event.grant === "invocation-isolation:v1" && event.binding === "KV",
            ),
          );
        }),
      );
    },
  );
  await check("service broker preserves HTTP, RPC, and nested RPC capabilities", async () => {
    await publish("identity", "invocation", identity("invocation"));
    assert.deepEqual(await get("/invocation-isolation/rpc"), {
      user: { id: "invocation", version: "invocation" },
      nested: "invocation:session",
      http: { id: "http", version: "invocation" },
    });
  });
  await check("main invocation exceptions still acknowledge runtime completion", async () => {
    const response = await fetch(base + "/invocation-isolation/fail", {
      headers: { "X-Test-Invocation": "invocation-failure" },
    });
    assert.equal(response.status, 500);
    assert.match(await response.text(), /intentional invocation failure/);
    await waitEvent("/invocation-complete", "invocation-failure");
  });
  await check(
    "WorkerEntrypoint RPC preserves scoped env, private context, and returned capabilities",
    async () => {
      const response = await fetch(base + "/invocation-class/method", {
        headers: { "X-Test-Invocation": "invocation-class-method" },
      });
      assert.equal(response.status, 200, await response.clone().text());
      assert.deepEqual(await response.json(), {
        value: "direct-rpc",
        props: { publicValue: "safe" },
        hidden: true,
      });
      await waitEvent("/invocation-complete", "invocation-class-method");
      const started = events.findIndex(
        (event) => event.path === "/invocation-start" && event.body === "invocation-class-method",
      );
      const constructed = events.findIndex(
        (event, index) =>
          index > started &&
          event.path === "/invocation-resource" &&
          (() => {
            const call = JSON.parse(event.body);
            return (
              call.grant === "invocation-class:v1" &&
              call.binding === "KV" &&
              call.path === "/constructor"
            );
          })(),
      );
      assert.ok(
        started >= 0 && constructed > started,
        "constructor resource access must follow admission acknowledgement",
      );
      assert.deepEqual(await get("/invocation-class/nested"), {
        value: "scoped-session",
        status: "active",
      });
      await waitEvent("/disposed", "session");
    },
  );
  const message = async (socket, value) => {
    const received = new Promise((resolve) =>
      socket.addEventListener("message", (event) => resolve(event.data), { once: true }),
    );
    socket.send(value);
    return bounded(received);
  };
  await check("waitUntil finishes after response and revision replacement", async () => {
    assert.deepEqual(await get("/lifecycle/background"), { queued: "v1" });
    await publish("lifecycle", "v2", lifecycle("v2"));
    assert.deepEqual(await get("/lifecycle/"), { version: "v2" });
    await waitEvent("/background", "v1");
  });
  await check("WebSocket stays on old revision across replacement", async () => {
    const old = await wsOpen();
    assert.equal(await message(old, "before"), "v2:before");
    await publish("lifecycle", "v3", lifecycle("v3"));
    assert.equal(await message(old, "after"), "v2:after");
    const current = await wsOpen();
    assert.equal(await message(current, "new"), "v3:new");
    old.close();
    current.close();
  });
  // Destroy an actual HTTP transport after receiving bytes. This avoids
  // confusing client library body cancellation with a confirmed disconnect.
  const disconnect = async (path) =>
    bounded(
      new Promise((resolve, reject) => {
        const request = httpGet(base + path, { agent: false }, (response) => {
          response.once("data", () => {
            response.destroy();
            request.destroy();
            resolve();
          });
          response.on("error", () => {});
        });
        request.once("error", reject);
      }),
    );
  const cancellation = {
    dynamic: false,
    static: false,
    upstream: { dynamic: false, static: false },
  };
  {
    await disconnect("/lifecycle/cancel");
    await publish("lifecycle", "v4", lifecycle("v4"));
    const deadline = Date.now() + 500;
    while (!logs.includes("CANCELLED:v3")) {
      if (Date.now() > deadline) break;
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
    cancellation.dynamic = logs.includes("CANCELLED:v3");
  }
  {
    await disconnect("/static-cancel");
    const deadline = Date.now() + 500;
    while (!logs.includes("CANCELLED:static")) {
      if (Date.now() > deadline) break;
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
    cancellation.static = logs.includes("CANCELLED:static");
  }
  await disconnect("/lifecycle/upstream");
  await disconnect("/static-upstream");
  const cleanupDeadline = Date.now() + 1500;
  while (Date.now() < cleanupDeadline) {
    for (const kind of ["dynamic", "static"])
      cancellation.upstream[kind] = events.some(
        (event) => event.path === "/disconnect-closed" && event.body === kind,
      );
    if (cancellation.upstream.dynamic && cancellation.upstream.static) break;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  await check(
    "client disconnect closes upstream transport in dynamic and static baseline",
    async () => {
      assert.deepEqual(cancellation.upstream, { dynamic: true, static: true });
    },
  );
  assert.equal(child.exitCode, null);
  const report = {
    workerd: version.stdout.trim(),
    pid: child.pid,
    passed: results.every((result) => result.passed),
    cutoverQualified: false,
    scope:
      "Exploratory loader and invocation contracts; this fixture is not the production publication gate",
    productionCoverage:
      "Separate production dispatcher and publication end-to-end tests cover local and remote runtime publication, revision-aware authorization, and recovery; those results are not inferred from this fixture",
    results,
    cancellation,
    remaining: [
      "Client-disconnect JavaScript callbacks and retained-memory bounds remain unqualified; see static/dynamic callback and upstream-close observations",
      "Production publication qualification belongs to the separate production dispatcher and publication end-to-end gates",
      "Returned RpcTarget facades support ordinary public prototype methods; instance fields, RPC getters, and custom Symbol.dispose forwarding are outside the supported subset",
    ],
    logs,
  };
  if (process.env.NANOFLARE_LOADER_REPORT)
    await writeFile(process.env.NANOFLARE_LOADER_REPORT, JSON.stringify(report, null, 2) + "\n");
  console.log(
    "Disconnect observations (JavaScript callbacks remain unqualified):",
    JSON.stringify(cancellation),
  );
  console.log(
    `\n${results.filter((result) => result.passed).length}/${results.length} contracts passed`,
  );
  process.exitCode = results.every((result) => result.passed) ? 0 : 1;
} finally {
  for (const socket of sockets) socket.close();
  if (child.exitCode === null && child.signalCode === null) {
    const stopped = once(child, "exit");
    child.kill("SIGTERM");
    const timer = setTimeout(() => child.kill("SIGKILL"), 2000);
    await stopped;
    clearTimeout(timer);
  }
  server.closeAllConnections();
  server.close();
  await rm(temp, { recursive: true, force: true });
}
