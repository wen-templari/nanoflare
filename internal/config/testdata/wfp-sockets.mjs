import assert from "node:assert/strict";
import { spawn, execFileSync } from "node:child_process";
import { once } from "node:events";
import { readFile, writeFile, mkdtemp, rm } from "node:fs/promises";
import net from "node:net";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import tls from "node:tls";
const fixture = JSON.parse(await readFile(process.argv[2], "utf8"));
const temp = await mkdtemp(join(tmpdir(), "wfp-sockets-"));
const servers = [],
  connections = new Set();
const listen = async (server) => {
  servers.push(server);
  server.on("connection", (s) => {
    connections.add(s);
    s.on("close", () => connections.delete(s));
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  return server.address().port;
};
try {
  execFileSync(
    "openssl",
    [
      "req",
      "-x509",
      "-newkey",
      "rsa:2048",
      "-nodes",
      "-keyout",
      join(temp, "key.pem"),
      "-out",
      join(temp, "cert.pem"),
      "-days",
      "1",
      "-subj",
      "/CN=localhost",
      "-addext",
      "subjectAltName=DNS:localhost",
    ],
    { stdio: "ignore" },
  );
  const cert = await readFile(join(temp, "cert.pem"), "utf8"),
    key = await readFile(join(temp, "key.pem"), "utf8");
  const context = tls.createSecureContext({ key, cert });
  const echo = (s) => {
    s.on("error", () => {});
    s.on("data", (data) => s.write(data));
  };
  const plain = await listen(net.createServer(echo));
  const direct = await listen(
    tls.createServer({ key, cert }, echo).on("tlsClientError", () => {}),
  );
  const upgrade = await listen(
    net.createServer((s) => {
      s.on("error", () => {});
      s.once("data", (data) => {
        assert.equal(data.toString(), "STARTTLS");
        s.write("S");
        const secure = new tls.TLSSocket(s, {
          isServer: true,
          secureContext: context,
        });
        echo(secure);
      });
    }),
  );
  let websocketRequests = 0;
  const websocketServer = createServer((req, res) => {
    websocketRequests++;
    res.end("unexpected native outbound");
  });
  websocketServer.on("upgrade", (req, socket) => {
    websocketRequests++;
    socket.destroy();
  });
  const websocketPort = await listen(websocketServer);
  const source = `import {connect} from 'cloudflare:sockets';
 export default {async fetch(request,env,ctx){
  const url=new URL(request.url), mode=url.searchParams.get('mode');
  if(mode==='fetch') { const a=await fetch('http://outside.invalid/'); const b=await fetch('http://outside.invalid/'); return new Response(a.status+':'+b.status); }
  if(mode==='websocketpair') { try { new WebSocketPair(); return new Response('native pair accepted'); } catch(error) { return new Response(error.message); } }
  if(mode==='websocket') { try { const socket = new WebSocket((url.searchParams.has('secure')?'wss':'ws')+'://127.0.0.1:${websocketPort}'); return new Response('native constructor accepted'); } catch(error) { return new Response(error.message); } }
  if(mode==='nested') return env.CHILD.fetch('http://customer/?mode=plain');
  const plaintext=['plain','mapped','unspecified'].includes(mode);
  const port=plaintext?${plain}:mode==='starttls'?${upgrade}:${direct};
  let hostname=mode==='mapped'?'::ffff:127.0.0.1':mode==='unspecified'?(url.searchParams.get('host')||'0.0.0.0'):url.searchParams.get('wrong')?'127.0.0.1':'localhost';
  if(hostname.includes(':')) hostname='['+hostname+']';
  let socket=connect({hostname,port},{secureTransport:plaintext?'off':mode==='starttls'?'starttls':'on'});
  try {
   await socket.opened;
   if(mode==='starttls'){const w=socket.writable.getWriter();await w.write(new TextEncoder().encode('STARTTLS'));w.releaseLock();const ready=socket.readable.getReader();await ready.read();ready.releaseLock();socket=socket.startTls();await socket.opened;}
   const w=socket.writable.getWriter();await w.write(new TextEncoder().encode('hello'));w.releaseLock();
   const r=socket.readable.getReader();const value=await r.read();r.releaseLock();return new Response(new TextDecoder().decode(value.value));
  } finally {await socket.close();}
 }};`;
  const artifact = {
    deployment: {
      app_id: "customer",
      id: "v1",
      compatibility_date: "2026-09-08",
      files: [{ path: "worker.js", content: source }],
    },
    digest: "v1",
    lease: "lease",
    values: {},
    wrapper: `import {dynamicEnv,withInvocation} from 'nanoflare-internal:dynamic-env.js'; import user from './worker.js'; export default {fetch(r,e,c){return withInvocation(c,()=>user.fetch(r,dynamicEnv(e,c,{services:[{binding:'CHILD'}]}),c));}};`,
    runtime_bindings: fixture.bindings,
    dynamic_env: fixture.dynamicEnv,
  };
  // Run the production dispatch module. The control fixture models broker admission and budgets.
  const host = `import {WorkerEntrypoint} from 'cloudflare:workers';import {wrapDispatchEnv} from './dispatch.js';export {DispatchBroker} from './dispatch.js';
 let effects=0;
 export class Control extends WorkerEntrypoint {async fetch(request){const p=new URL(request.url).pathname;if(p.endsWith('/acquire')||p.endsWith('/nested'))return Response.json(${JSON.stringify(artifact)});if(p.endsWith('/outbound'))return new Response('broker',{status:++effects%2===1?200:429});return new Response(null,{status:204});}}
 export class ResourceBridge extends Control {}
 export default {fetch(r,e,c){return wrapDispatchEnv({...e,__NANOFLARE_RESOURCE_CONTROL:{},control:c.exports.Control({}),__NANOFLARE_DISPATCH_BINDINGS:{NS:'control'}},c,'root').NS.get('customer').fetch(r);}};`;
  for (const policy of [
    "disabled",
    "allowed",
    "denied",
    "untrusted",
    "loopback-denied",
    "explicit-denied",
    "custom-denied",
  ]) {
    const enabled = policy !== "disabled";
    const config = `using Workerd = import "/workerd/workerd.capnp";const config :Workerd.Config = (services=[
   (name="host",worker=(compatibilityDate="2026-09-08",compatibilityFlags=["experimental","enable_abortsignal_rpc"],modules=[(name="host.js",esModule=${JSON.stringify(host)}),(name="dispatch.js",esModule=${JSON.stringify(fixture.dispatch)})],bindings=[(name="__NANOFLARE_WORKER_LOADER",workerLoader=()),${enabled ? '(name="__NANOFLARE_WFP_SOCKET_OUTBOUND",service="nanoflare-wfp-sockets"),' : ""}(name="OUTBOUND",service="internet")])),
   (name="internet",network=(allow=["public"])),
   ${(policy === "loopback-denied" ? fixture.defaultDeny : policy === "custom-denied" ? fixture.customDeny : policy === "explicit-denied" ? fixture.service.replace("deny = []", 'deny = ["127.0.0.1/32", "::1/128"]') : fixture.service).replace('"local"', JSON.stringify(policy === "denied" ? "192.0.2.0/24" : "local")).replace('"CERTIFICATE_PLACEHOLDER"', policy === "untrusted" ? "" : JSON.stringify(cert))}
  ],sockets=[(name="http",address="127.0.0.1:0",http=(),service="host")]);`;
    const path = join(temp, policy + ".capnp");
    await writeFile(path, config);
    const child = spawn(
      process.argv[3],
      ["serve", "--experimental", "--control-fd=3", path],
      {
        stdio: ["ignore", "pipe", "pipe", "pipe"],
      },
    );
    let logs = "";
    child.stderr.on("data", (c) => (logs += c));
    child.stdout.on("data", (c) => (logs += c));
    try {
      const port = await Promise.race([
        new Promise((resolve, reject) => {
          let buffer = "";
          child.once("error", reject);
          child.once("exit", (code) =>
            reject(new Error("runtime exited " + code + ": " + logs)),
          );
          child.stdio[3].on("data", (chunk) => {
            buffer += chunk;
            const lines = buffer.split("\n");
            buffer = lines.pop();
            for (const line of lines) {
              const m = JSON.parse(line);
              if (m.port) resolve(m.port);
            }
          });
        }),
        new Promise((_, reject) => {
          const t = setTimeout(
            () => reject(new Error("startup timeout: " + logs)),
            10000,
          );
          t.unref();
        }),
      ]);
      const get = async (query) => {
        try {
          const r = await fetch("http://127.0.0.1:" + port + "/?" + query, {
            signal: AbortSignal.timeout(5000),
          });
          return { status: r.status, body: await r.text() };
        } catch (e) {
          if (e.name === "TimeoutError") throw e;
          return { status: 500, body: String(e) + " " + logs };
        }
      };
      for (const query of [
        "mode=websocket",
        "mode=websocket&secure=1",
        "mode=websocketpair",
      ])
        assert.deepEqual(await get(query), {
          status: 200,
          body: "Outbound WebSockets are unavailable",
        });
      assert.equal(
        websocketRequests,
        0,
        "native WebSocket reached destination",
      );
      if (policy === "loopback-denied" || policy === "custom-denied")
        assert.equal(
          (await get("mode=mapped")).status,
          500,
          policy + " mapped address bypassed deny policy",
        );
      if (policy === "loopback-denied") {
        for (const hostname of ["0.0.0.0", "::", "::ffff:0.0.0.0"]) {
          assert.equal(
            (await get("mode=unspecified&host=" + encodeURIComponent(hostname)))
              .status,
            500,
            "unspecified destination bypassed local guard: " + hostname,
          );
        }
      }
      if (policy === "allowed") {
        for (const query of [
          "mode=mapped",
          "mode=unspecified",
          "mode=unspecified&host=" + encodeURIComponent("::ffff:0.0.0.0"),
        ]) {
          assert.deepEqual(
            await get(query),
            { status: 200, body: "hello" },
            "positive connection control: " + query + " " + logs,
          );
        }
      }
      for (const mode of ["plain", "tls", "starttls", "nested"]) {
        const r = await get("mode=" + mode);
        const succeeds =
          policy === "allowed" ||
          (policy === "untrusted" && ["plain", "nested"].includes(mode));
        assert.equal(
          r.status,
          succeeds ? 200 : 500,
          policy + " " + mode + ": " + r.body + "\n" + logs,
        );
        if (succeeds) assert.equal(r.body, "hello");
      }
      if (policy === "allowed")
        for (const mode of ["tls", "starttls"])
          assert.equal(
            (await get("mode=" + mode + "&wrong=1")).status,
            500,
            "wrong hostname " + mode,
          );
      assert.deepEqual(
        await get("mode=fetch"),
        { status: 200, body: "200:429" },
        "fetch bypassed broker budget: " + logs,
      );
      console.log(
        "PASS " + policy + " sockets, nested calls, and broker fetch",
      );
    } finally {
      if (child.exitCode === null && child.signalCode === null) {
        const exited = once(child, "exit");
        child.kill();
        await exited;
      }
    }
  }
} finally {
  for (const s of connections) s.destroy();
  await Promise.all(servers.map((s) => new Promise((r) => s.close(r))));
  await rm(temp, { recursive: true, force: true });
}
