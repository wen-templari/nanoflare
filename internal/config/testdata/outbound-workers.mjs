import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, writeFile, rm } from "node:fs/promises";
import http from "node:http";
import net from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";

const temp = await mkdtemp(join(tmpdir(), "nf-outbound-"));
const connections = new Set();
const listen = async (server) => {
  server.on("connection", (socket) => {
    connections.add(socket);
    socket.on("error", () => {});
    socket.on("close", () => connections.delete(socket));
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  return server.address().port;
};
let dials = 0,
  hits = 0;
const tcp = net.createServer((socket) => {
  dials++;
  socket.end("socket-ok");
});
const origin = http.createServer((request, response) => {
  hits++;
  response.end("network-ok");
});
let child;
try {
  const tcpPort = await listen(tcp),
    httpPort = await listen(origin);
  const customer = `import {connect} from 'cloudflare:sockets';
// Deliberately use native fetch, without Nanoflare's JavaScript adapter.
export default {async fetch(request,env,ctx) {
 const mode=new URL(request.url).searchParams.get('mode');
 if(mode==='tcp') {
  let socket;
  try {socket=connect('127.0.0.1:${tcpPort}');await socket.opened; return new Response('socket-ok');}
  catch {return new Response('tcp-denied',{status:403});}
  finally {await socket?.close().catch(()=>{});}
 }
 if(mode==='native') {
  try {return await fetch('http://127.0.0.1:${httpPort}');}
  catch {return new Response('native-denied',{status:403});}
 }
 // The production broker similarly lives in per-invocation native props.
 return ctx.props.policy.fetch(request);
}};`;
  const policy = `import {WorkerEntrypoint} from 'cloudflare:workers';
export class Policy extends WorkerEntrypoint {
 async fetch(request) {
  const {customer,tag}=this.ctx.props;
  const env=Object.assign(Object.create(this.env),{customer});
  // Overlap requests on the same warm revision.
  await new Promise(resolve=>setTimeout(resolve,5));
  if(new URL(request.url).searchParams.has('block'))return new Response(tag+':blocked',{status:403});
  const response=await fetch('http://127.0.0.1:${httpPort}');
  const body=await response.text();
  return new Response(new ReadableStream({start(controller){
   controller.enqueue(new TextEncoder().encode(tag+':'+env.customer+':'+env.SECRET+':'+body));controller.close();
  }}));
 }
}
export default {fetch(){return new Response('unused')}};`;
  const host = `import {WorkerEntrypoint} from 'cloudflare:workers';
export class Policy extends WorkerEntrypoint {fetch(request){
 const worker=this.env.LOADER.get('policy',()=>({compatibilityDate:'2026-09-08',mainModule:'policy.js',modules:{'policy.js':{js:${JSON.stringify(policy)}}},env:{SECRET:'platform-secret'},globalOutbound:this.env.NETWORK}));
 return worker.getEntrypoint('Policy',{props:this.ctx.props}).fetch(request);
}}
export default {async fetch(request,env,ctx) {
 const url=new URL(request.url), tag=url.searchParams.get('policy')||'none';
 const restricted=tag!=='none';
 // Only immutable networking mode enters the cached descriptor.
 const worker=env.LOADER.get('customer:'+restricted,()=>({
  compatibilityDate:'2026-09-08',mainModule:'worker.js',modules:{'worker.js':{js:${JSON.stringify(customer)}}},
  env:{},globalOutbound:restricted?null:env.NETWORK,
 }));
 const policy=ctx.props.policy||ctx.exports.Policy({props:{tag,customer:url.searchParams.get('customer')}});
 return worker.getEntrypoint(undefined,{props:{policy}}).fetch(request);
}};`;
  // Exercise an ordinary static host and a loader-hosted platform dispatcher.
  const gateway = `import {WorkerEntrypoint} from 'cloudflare:workers';import host from './host.js';
export {Policy} from './host.js';
export class Dispatch extends WorkerEntrypoint {fetch(request){return host.fetch(request,this.env,this.ctx);}}
export default {fetch(request,env,ctx) {
 const worker=env.LOADER.get('host',()=>({compatibilityDate:'2026-09-08',mainModule:'host.js',
  modules:{'host.js':{js:'export default {fetch(r,e,c){return c.props.dispatch.fetch(r);}}'}},env:{},globalOutbound:null}));
 return worker.getEntrypoint(undefined,{props:{dispatch:ctx.exports.Dispatch({})}}).fetch(request);
}};`;
  const bindings = `(name="LOADER",workerLoader=()),(name="NETWORK",service="network")`;
  const config = `using Workerd=import "/workerd/workerd.capnp";
const config :Workerd.Config=(services=[
 (name="network",network=(allow=["local"])),
 (name="static",worker=(compatibilityDate="2026-09-08",compatibilityFlags=["experimental"],modules=[(name="host.js",esModule=${JSON.stringify(host)})],bindings=[${bindings}])),
 (name="loaded",worker=(compatibilityDate="2026-09-08",compatibilityFlags=["experimental"],modules=[(name="gateway.js",esModule=${JSON.stringify(gateway)}),(name="host.js",esModule=${JSON.stringify(host)})],bindings=[${bindings}]))
],sockets=[(name="static",address="127.0.0.1:0",http=(),service="static"),(name="loaded",address="127.0.0.1:0",http=(),service="loaded")]);`;
  const path = join(temp, "test.capnp");
  await writeFile(path, config);
  child = spawn(process.argv[2], ["serve", "--experimental", "--control-fd=3", path], {
    stdio: ["ignore", "pipe", "pipe", "pipe"],
  });
  let logs = "";
  child.stdout.on("data", (part) => {
    logs += part;
  });
  child.stderr.on("data", (part) => {
    logs += part;
  });
  const ports = await new Promise((resolve, reject) => {
    let buffer = "";
    const ports = {};
    const timer = setTimeout(() => reject(new Error("startup timeout: " + logs)), 10000);
    child.once("error", reject);
    child.once("exit", (code) => {
      clearTimeout(timer);
      reject(new Error("runtime exited " + code + ": " + logs));
    });
    child.stdio[3].on("data", (part) => {
      buffer += part;
      const lines = buffer.split("\n");
      buffer = lines.pop();
      for (const line of lines) {
        const event = JSON.parse(line);
        if (event.port) ports[event.socket] = event.port;
      }
      if (Object.keys(ports).length === 2) {
        clearTimeout(timer);
        resolve(ports);
      }
    });
  });
  for (const [name, port] of Object.entries(ports)) {
    const get = (query) =>
      fetch(`http://127.0.0.1:${port}/?${query}`, { signal: AbortSignal.timeout(5000) });
    assert.equal(
      await (await get("mode=tcp")).text(),
      "socket-ok",
      name + " socket baseline: " + logs,
    );
    assert.equal(await (await get("mode=native")).text(), "network-ok");
    const beforeDials = dials,
      beforeHits = hits;
    for (const tag of ["a", "b"]) {
      for (const mode of ["tcp", "native"]) {
        const response = await get(`policy=${tag}&mode=${mode}`);
        assert.equal(
          response.status,
          403,
          name + " " + mode + ": " + (await response.text()) + logs,
        );
      }
    }
    assert.equal(dials, beforeDials, "restricted customer dialed TCP");
    assert.equal(hits, beforeHits, "restricted customer reached HTTP origin natively");
    await Promise.all(
      Array.from({ length: 24 }, async (_, index) => {
        const tag = index % 2 ? "a" : "b",
          customer = "customer-" + index;
        const response = await get(`policy=${tag}&customer=${customer}`);
        assert.equal(
          await response.text(),
          `${tag}:${customer}:platform-secret:network-ok`,
          name + ": " + logs,
        );
      }),
    );
    const blockedHits = hits;
    const blocked = await get("policy=a&block=1");
    assert.equal(blocked.status, 403);
    assert.equal(await blocked.text(), "a:blocked");
    assert.equal(hits, blockedHits, "blocked request reached Internet");
    // Returning to the unrestricted descriptor must retain its original socket policy.
    assert.equal(await (await get("mode=tcp")).text(), "socket-ok");
    console.log(
      `PASS ${name}: concurrent policies, request-local parameters, ordinary secrets, streamed response, block, native HTTP/TCP denial, unrestricted reuse`,
    );
  }
} finally {
  if (child && child.exitCode === null && child.signalCode === null) {
    const exited = once(child, "exit");
    child.kill();
    await exited;
  }
  for (const socket of connections) socket.destroy();
  await Promise.all([tcp, origin].map((server) => new Promise((resolve) => server.close(resolve))));
  await rm(temp, { recursive: true, force: true });
}
