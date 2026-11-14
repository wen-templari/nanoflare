import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, writeFile, rm } from "node:fs/promises";
import http from "node:http";
import net from "node:net";
import { tmpdir, arch, platform } from "node:os";
import { join } from "node:path";

const temp = await mkdtemp(join(tmpdir(), "nf-outbound-native-"));
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
  hits = 0,
  redirectPort;
const tcp = net.createServer((socket) => {
  dials++;
  socket.end("socket-ok");
});
const origin = http.createServer(async (request, response) => {
  hits++;
  const url = new URL(request.url, "http://origin");
  if (url.searchParams.get("mode") === "redirect") {
    response.writeHead(302, { location: `http://127.0.0.1:${redirectPort}/?mode=final` });
    response.end();
    return;
  }
  if (url.searchParams.get("mode") === "final") {
    response.setHeader("content-type", "application/json");
    response.end(
      JSON.stringify({
        method: request.method,
        authorization: request.headers.authorization ?? "",
      }),
    );
    return;
  }
  if (url.searchParams.get("mode") === "binary") {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    response.setHeader("content-type", "application/octet-stream");
    response.setHeader("x-method", request.method);
    response.setHeader("x-authorization", request.headers.authorization ?? "");
    response.end(Buffer.concat(chunks));
    return;
  }
  response.end("network-ok");
});
const redirectOrigin = http.createServer(origin.listeners("request")[0]);
const measurements = [];
let child;
try {
  const tcpPort = await listen(tcp),
    httpPort = await listen(origin);
  redirectPort = await listen(redirectOrigin);
  const customer = `import {connect} from 'cloudflare:sockets';
import {get as httpGet} from 'node:http';
// Deliberately use native fetch, without Nanoflare's JavaScript adapter.
export default {async fetch(request,env,ctx) {
 const mode=new URL(request.url).searchParams.get('mode');
 if(mode==='node') {
  return new Promise(resolve=>{
   const outgoing=httpGet('http://127.0.0.1:${httpPort}/'+new URL(request.url).search, response=>{
    const chunks=[];response.on('data',chunk=>chunks.push(chunk));response.on('end',()=>resolve(new Response(Buffer.concat(chunks),{status:response.statusCode})));
   });outgoing.on('error',error=>resolve(new Response('node-error:'+error.message,{status:502})));
  });
 }
 if(mode==='redirect') return fetch('http://127.0.0.1:${httpPort}/'+new URL(request.url).search,{method:request.method,headers:request.headers,body:await request.arrayBuffer(),redirect:'follow'});
 if(mode==='binary') return fetch(new Request('http://127.0.0.1:${httpPort}/'+new URL(request.url).search,request));
 if(mode==='tcp') {
  let socket;
  try {socket=connect('127.0.0.1:${tcpPort}');await socket.opened; return new Response('socket-ok');}
  catch {return new Response('tcp-denied',{status:403});}
  finally {await socket?.close().catch(()=>{});}
 }
 if(mode==='native') {
  try {return await fetch('http://127.0.0.1:${httpPort}/'+new URL(request.url).search);}
  catch {return new Response('native-denied',{status:403});}
 }
 return fetch('http://127.0.0.1:${httpPort}/'+new URL(request.url).search);
}};`;
  const policy = `import {WorkerEntrypoint} from 'cloudflare:workers';
export class Policy extends WorkerEntrypoint {
 async fetch(request) {
  const {customer,tag}=this.ctx.props;
  const env=Object.assign(Object.create(this.env),{customer});
  // Overlap requests on the same warm revision.
  await new Promise(resolve=>setTimeout(resolve,5));
  if(new URL(request.url).searchParams.has('block'))return new Response(tag+':blocked',{status:403});
  if(['binary','redirect','final'].includes(new URL(request.url).searchParams.get('mode'))) {
   const response=await fetch(request,{redirect:'manual'});
   const headers=new Headers(response.headers);headers.set('x-policy',tag);headers.set('x-customer',env.customer);
   return new Response(response.body,{status:response.status,headers});
  }
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
 const policy=ctx.props.policy||ctx.exports.Policy({props:{tag,customer:url.searchParams.get('customer')}});
 // Request-bound channels exist only in an anonymous descriptor, never a keyed cache.
 const code=()=>({
  compatibilityDate:'2026-09-08',compatibilityFlags:['nodejs_compat'],mainModule:'worker.js',modules:{'worker.js':{js:${JSON.stringify(customer)}}},
  env:{},globalOutbound:restricted?policy:env.NETWORK,
 });
 const worker=restricted?env.LOADER.get(null,code):env.LOADER.get('customer:unrestricted',code);
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
      for (const mode of ["tcp"]) {
        const response = await get(`policy=${tag}&mode=${mode}`);
        assert.equal(
          response.status,
          403,
          name + " " + mode + ": " + (await response.text()) + logs,
        );
      }
    }
    assert.equal(dials, beforeDials, "restricted customer dialed TCP");
    assert.equal(hits, beforeHits, "restricted customer reached HTTP origin before policy");
    const firstStarted = performance.now();
    const firstPolicy = await get("policy=a&customer=measurement-first");
    assert.equal(await firstPolicy.text(), "a:measurement-first:platform-secret:network-ok");
    const firstPolicyMS = performance.now() - firstStarted;
    const overlappingStarted = performance.now();
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
    measurements.push({
      host: name,
      first_policy_http_ms: firstPolicyMS,
      overlapping_24_ms: performance.now() - overlappingStarted,
    });
    const nodeResponse = await get("policy=a&mode=node&customer=node-customer");
    assert.equal(nodeResponse.status, 200, name + " node HTTP: " + logs);
    assert.equal(
      await nodeResponse.text(),
      "a:node-customer:platform-secret:network-ok",
      name + " node HTTP must reach policy",
    );
    const payload = Uint8Array.from([0, 255, 1, 128, 13, 10]);
    const binary = await fetch(
      `http://127.0.0.1:${port}/?policy=b&mode=binary&customer=binary-customer`,
      {
        method: "POST",
        body: payload,
        headers: { authorization: "Bearer test-token" },
        signal: AbortSignal.timeout(5000),
      },
    );
    assert.deepEqual(new Uint8Array(await binary.arrayBuffer()), payload, name + " binary body");
    assert.equal(binary.headers.get("x-policy"), "b");
    assert.equal(binary.headers.get("x-customer"), "binary-customer");
    assert.equal(binary.headers.get("x-method"), "POST");
    assert.equal(binary.headers.get("x-authorization"), "Bearer test-token");
    const redirected = await fetch(
      `http://127.0.0.1:${port}/?policy=a&mode=redirect&customer=redirect-customer`,
      {
        method: "POST",
        body: "redirect-body",
        headers: { authorization: "Bearer private" },
        signal: AbortSignal.timeout(5000),
      },
    );
    const redirectedBody = await redirected.text();
    assert.equal(redirected.status, 200, name + " redirect: " + redirectedBody + " " + logs);
    assert.deepEqual(
      JSON.parse(redirectedBody),
      { method: "GET", authorization: "" },
      name + " cross-origin redirect",
    );
    assert.equal(redirected.headers.get("x-policy"), "a");
    assert.equal(redirected.headers.get("x-customer"), "redirect-customer");
    const native = await get("policy=b&mode=native&customer=native-customer");
    assert.equal(
      await native.text(),
      "b:native-customer:platform-secret:network-ok",
      name + " native HTTP must reach policy",
    );
    const blockedHits = hits;
    const blocked = await get("policy=a&block=1");
    assert.equal(blocked.status, 403);
    assert.equal(await blocked.text(), "a:blocked");
    assert.equal(hits, blockedHits, "blocked request reached Internet");
    // Returning to the unrestricted descriptor must retain its original socket policy.
    assert.equal(await (await get("mode=tcp")).text(), "socket-ok");
    console.log(
      `PASS ${name}: concurrent policies, request-local parameters, ordinary secrets, streamed response, block, native/Node HTTP interception, binary bodies, cross-origin redirects, TCP denial, unrestricted reuse`,
    );
  }
  if (process.argv[3])
    await writeFile(
      process.argv[3],
      JSON.stringify(
        {
          generated_at: new Date().toISOString(),
          node: process.version,
          platform: platform(),
          arch: arch(),
          workerd: "2026-09-11",
          descriptor_policy:
            "Anonymous restricted customer descriptors; keyed immutable policy source",
          scope:
            "Engine fixture only, including 5ms synthetic policy delay; development observation, no production latency or warm-isolate guarantee",
          measurements,
        },
        null,
        2,
      ) + "\n",
    );
} finally {
  if (child && child.exitCode === null && child.signalCode === null) {
    const exited = once(child, "exit");
    child.kill();
    await exited;
  }
  for (const socket of connections) socket.destroy();
  await Promise.all(
    [tcp, origin, redirectOrigin].map((server) => new Promise((resolve) => server.close(resolve))),
  );
  await rm(temp, { recursive: true, force: true });
}
