import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { request as httpsRequest } from "node:https";
import tls from "node:tls";

// Requires the isolated docker/compose.e2e.yml stack. The crash-recovery check
// intentionally kills its shared workerd process; never target a live project.

const project = process.env.E2E_PROJECT || "nanoflare-e2e";
const runID = Date.now();
const control = process.env.E2E_CONTROL_URL || "http://127.0.0.1:8080";
const ingressPort = Number(process.env.E2E_INGRESS_PORT || 8443);
const compose = [
  "compose",
  "-p",
  project,
  "-f",
  "docker/compose.yml",
  "-f",
  "docker/compose.e2e.yml",
];
const pause = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const docker = (...args) =>
  execFileSync("docker", [...compose, ...args], { encoding: "utf8" }).trim();
const report = (name) => console.log(`PASS ${name}`);
let token, org, worker;
async function api(path, body, method = body ? "POST" : "GET") {
  const response = await fetch(control + path, {
    method,
    headers: {
      "Content-Type": "application/json",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
    signal: AbortSignal.timeout(30000),
  });
  const text = await response.text();
  assert.ok(response.ok, `${method} ${path}: ${response.status} ${text}`);
  return text ? JSON.parse(text) : undefined;
}
function ingress(host, path = "/", onChunk) {
  return new Promise((resolve, reject) => {
    const started = Date.now();
    const req = httpsRequest(
      {
        hostname: "127.0.0.1",
        port: ingressPort,
        path,
        rejectUnauthorized: false,
        headers: { Host: host },
        timeout: 15000,
      },
      (response) => {
        let body = "";
        response.on("data", (chunk) => {
          body += chunk;
          onChunk?.(chunk.toString(), Date.now() - started);
        });
        response.on("end", () => resolve({ status: response.statusCode, body }));
        response.on("error", reject);
      },
    );
    req.on("timeout", () => req.destroy(new Error("ingress timeout")));
    req.on("error", reject);
    req.end();
  });
}
const inspectScript = `const fs=require('fs');const pids=fs.readdirSync('/proc').filter(x=>/^\\d+$/.test(x)).filter(x=>{try{return fs.readFileSync('/proc/'+x+'/comm','utf8').trim()==='workerd'}catch{return false}});const root='/tmp/nanoflare-sockets';const dirs=fs.existsSync(root)?fs.readdirSync(root):[];console.log(JSON.stringify({pids,sockets:dirs.flatMap(d=>fs.readdirSync(root+'/'+d).map(f=>root+'/'+d+'/'+f))}));`;
function runtimeState() {
  return JSON.parse(docker("exec", "-T", "nanoflare-runner", "node", "-e", inspectScript));
}
function source(version) {
  return `const version=${JSON.stringify(version)};
export default {
 async fetch(request, env) {
  const path = new URL(request.url).pathname;
  if (path === '/ws') {
   const pair = new WebSocketPair(); pair[1].accept();
   pair[1].addEventListener('message', e => pair[1].send('echo:' + e.data));
   return new Response(null, {status:101,webSocket:pair[0]});
  }
  if (path === '/stream') {
   const encoder = new TextEncoder();
   return new Response(new ReadableStream({start(controller) {
    controller.enqueue(encoder.encode('first\\n'));
    setTimeout(() => {controller.enqueue(encoder.encode('second\\n'));controller.close()},1000);
   }}),{headers:{'Content-Type':'text/event-stream'}});
  }
  if (path === '/slow') await new Promise(r=>setTimeout(r,1500));
  if (path === '/kv') {await env.STATE.put('value','persisted');return new Response(await env.STATE.get('value'))}
  if (path === '/cron') return new Response(await env.STATE.get('cron') || 'pending');
  return new Response(version);
 },
 async scheduled(event,env) {await env.STATE.put('cron',JSON.stringify({cron:event.cron,time:event.scheduledTime}));console.log('full-stack-cron');}
};`;
}
function websocket(host) {
  return new Promise((resolve, reject) => {
    const socket = tls.connect(
      { host: "127.0.0.1", port: ingressPort, rejectUnauthorized: false },
      () => {
        socket.write(
          `GET /ws HTTP/1.1\r\nHost: ${host}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: ${randomBytes(16).toString("base64")}\r\n\r\n`,
        );
      },
    );
    socket.setTimeout(10000, () => socket.destroy(new Error("WebSocket timeout")));
    let upgraded = false,
      buffer = Buffer.alloc(0);
    socket.on("error", reject);
    socket.on("data", (data) => {
      buffer = Buffer.concat([buffer, data]);
      if (!upgraded) {
        const end = buffer.indexOf("\r\n\r\n");
        if (end < 0) return;
        try {
          assert.match(buffer.subarray(0, end).toString(), /^HTTP\/1\.1 101/);
        } catch (error) {
          socket.destroy();
          reject(error);
          return;
        }
        upgraded = true;
        buffer = buffer.subarray(end + 4);
        const payload = Buffer.from("hello"),
          mask = randomBytes(4);
        const frame = Buffer.alloc(6 + payload.length);
        frame[0] = 0x81;
        frame[1] = 0x80 | payload.length;
        mask.copy(frame, 2);
        for (let i = 0; i < payload.length; i++) frame[6 + i] = payload[i] ^ mask[i % 4];
        socket.write(frame);
      }
      if (buffer.length < 2) return;
      const length = buffer[1] & 127;
      if (buffer.length < 2 + length) return;
      try {
        assert.equal(buffer.subarray(2, 2 + length).toString(), "echo:hello");
        socket.destroy();
        resolve();
      } catch (error) {
        socket.destroy();
        reject(error);
      }
    });
  });
}

const session = await api("/v1/setup/signup", {
  email: `stack-${Date.now()}@example.test`,
  password: "Stack-test-password-123!",
  organization_name: "Socket E2E",
});
token = session.access_token;
org = session.active_org_id;
assert.ok(token && org);
report("account and organization backed by PostgreSQL");
const base = `/v1/organizations/${org}`;
const baselineSockets = runtimeState().sockets.length;
const expectedSockets = baselineSockets + 2;
const namespace = await api(base + "/kv-namespaces", { name: "stack-state" });
worker = await api(base + "/workers", {
  name: "Stack worker",
  hostname: `stack-${runID}.workers.example.test`,
});
const other = await api(base + "/workers", {
  name: "Second worker",
  hostname: `second-${runID}.workers.example.test`,
});
const deploy = (app, version) =>
  api(`${base}/workers/${app.id}/deployments`, {
    entrypoint: "worker.js",
    format: "modules",
    compatibility_date: "2026-07-30",
    files: [{ path: "worker.js", content: source(version) }],
    kv_namespaces: [{ binding: "STATE", id: namespace.id }],
    triggers: { crons: ["* * * * *"] },
  });
await deploy(worker, "v1");
await deploy(other, "other");
for (let i = 0; i < 30; i++) {
  const result = await ingress(worker.hostname);
  if (result.status === 200 && result.body === "v1") break;
  if (i === 29) assert.fail(JSON.stringify(result));
  await pause(1000);
}
assert.deepEqual(await ingress(other.hostname), { status: 200, body: "other" });
report("two Workers through Traefik, control gateway, runner and Unix sockets");
let state = runtimeState();
assert.equal(state.pids.length, 1);
assert.equal(state.sockets.length, expectedSockets);
report("one workerd process with separate deployment sockets");
assert.equal((await ingress(worker.hostname, "/kv")).body, "persisted");
report("native KV binding round trip");
let firstByte;
const stream = await ingress(worker.hostname, "/stream", (_chunk, elapsed) => {
  firstByte ??= elapsed;
});
assert.equal(stream.body, "first\nsecond\n");
assert.ok(firstByte < 900, `stream buffered for ${firstByte}ms`);
report("streaming response arrives before completion");
await websocket(worker.hostname);
report("WebSocket upgrade and bidirectional echo through full ingress");
const slow = ingress(worker.hostname, "/slow");
await pause(250);
await deploy(worker, "v2");
assert.equal((await slow).body, "v1");
assert.equal((await ingress(worker.hostname)).body, "v2");
report("deployment replacement drains an in-flight request");
const invalid = await fetch(`${control}${base}/workers/${worker.id}/deployments`, {
  method: "POST",
  headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
  body: JSON.stringify({
    entrypoint: "worker.js",
    format: "modules",
    compatibility_date: "2026-07-30",
    files: [{ path: "worker.js", content: "export default { syntax error" }],
  }),
});
assert.ok(!invalid.ok);
await invalid.text();
assert.equal((await ingress(worker.hostname)).body, "v2");
report("invalid candidate rolls back without disrupting serving");
await pause(1500);
state = runtimeState();
assert.equal(state.pids.length, 1);
assert.equal(state.sockets.length, expectedSockets);
report("retired and failed generation sockets cleaned up");
const pid = state.pids[0];
console.log("Waiting 35 seconds with no Worker requests to check idle persistence...");
await pause(35000);
assert.deepEqual(runtimeState().pids, [pid]);
report("shared process survives former idle timeout");
console.log("Waiting for the scheduled trigger...");
for (let i = 0; i < 65; i++) {
  const result = await ingress(worker.hostname, "/cron");
  if (result.body !== "pending") {
    assert.equal(JSON.parse(result.body).cron, "* * * * *");
    break;
  }
  if (i === 64) assert.fail("cron never fired");
  await pause(1000);
}
report("cron invocation through Unix socket persists state");
docker("exec", "-T", "nanoflare-runner", "node", "-e", `process.kill(${Number(pid)},'SIGKILL')`);
for (let i = 0; i < 30; i++) {
  await pause(500);
  try {
    if ((await ingress(worker.hostname)).body === "v2") break;
  } catch {}
  if (i === 29) assert.fail("runtime did not recover");
}
state = runtimeState();
assert.equal(state.pids.length, 1);
assert.notEqual(state.pids[0], pid);
assert.equal(state.sockets.length, expectedSockets);
report("automatic recovery after workerd crash");
const persistedCron = await ingress(worker.hostname, "/cron");
assert.equal(persistedCron.status, 200);
assert.equal(JSON.parse(persistedCron.body).cron, "* * * * *");
report("persisted storage survives runtime restart");
console.log("Runtime E2E checks passed.");
const services = docker("ps", "--all", "--format", "{{.Service}} {{.State}}").split("\n");
const failed = services.filter((line) => !line.endsWith(" running"));
assert.deepEqual(failed, [], `Full-stack service failures: ${failed.join(", ")}`);
console.log("Full-stack E2E passed. Test resources remain in the isolated Compose project.");
