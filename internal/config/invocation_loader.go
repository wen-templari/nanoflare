package config

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtimeassets"
)

const loaderInvocationContextModule = "nanoflare-internal:invocation-context"

// InstrumentLoaderInvocation adds revision-bound resource capabilities and
// invocation completion accounting. The input snapshot is not mutated. The parent must
// admit an invocation before supplying its private props to getEntrypoint().
func InstrumentLoaderInvocation(code WorkerCodeSnapshot, active nanoflare.ActiveDeployment) WorkerCodeSnapshot {
	code.Modules = maps.Clone(code.Modules)
	code.moduleOrder = slices.Clone(code.moduleOrder)
	declarations := struct {
		KV       []string `json:"kv"`
		Fetch    []string `json:"fetch"`
		Services []string `json:"services"`
		Dispatch []string `json:"dispatch"`
	}{KV: []string{}, Fetch: []string{}, Services: []string{}}
	for _, binding := range active.Deployment.KVNamespaces {
		declarations.KV = append(declarations.KV, binding.Binding)
	}
	for _, binding := range active.Deployment.Databases {
		declarations.Fetch = append(declarations.Fetch, binding.Binding)
	}
	for _, binding := range active.Deployment.ObjectStorageBuckets {
		declarations.Fetch = append(declarations.Fetch, binding.Binding)
	}
	if len(active.Deployment.Assets) > 0 || active.Deployment.AssetConfig.Binding != "" {
		declarations.Fetch = append(declarations.Fetch, assetBindingName(active.Deployment.AssetConfig))
	}
	for _, binding := range active.Deployment.Services {
		declarations.Services = append(declarations.Services, binding.Binding)
	}
	for _, binding := range active.Deployment.DispatchNamespaces {
		declarations.Dispatch = append(declarations.Dispatch, binding.Binding)
	}
	declarationJSON, _ := json.Marshal(declarations)
	const importedEnvModule = "__nanoflare_imported_env__.js"
	code.Modules[importedEnvModule] = WorkerCodeModule{JS: `import { installImportedBindings } from "` + loaderInvocationContextModule + `";
import { wrapEnv } from "nanoflare-internal:runtime-bindings";
installImportedBindings(` + string(declarationJSON) + `, env => wrapEnv(env, ` + quote(assetBindingName(active.Deployment.AssetConfig)) + `, ` + dbBindingNamesJSON(active.Deployment.Databases) + `, ` + objectBindingNamesJSON(active.Deployment.ObjectStorageBuckets) + `));
`}
	code.moduleOrder = append(code.moduleOrder, importedEnvModule)
	const main = "__nanoflare_invocation_entrypoint__.js"
	if existing, exists := code.Modules[loaderInvocationContextModule]; !exists || existing.JS != loaderInvocationContextSource {
		code.Modules[loaderInvocationContextModule] = WorkerCodeModule{JS: loaderInvocationContextSource}
		code.moduleOrder = append(code.moduleOrder, loaderInvocationContextModule)
	}
	code.Modules[main] = WorkerCodeModule{JS: `import "./` + importedEnvModule + `";
import UserWorker, * as UserModule from "./` + code.MainModule + `";
export * from "./` + code.MainModule + `";
export class NanoflareExportDispatcher extends WorkerEntrypoint {
 invoke(name,props,method,args,read=false) {
  const state=this._state??=invocationState(this.env,this.ctx);
  const Type=UserModule[name];
  if(typeof Type!=="function" || typeof method!=="string" || method.startsWith("_") || ["constructor","ctx","env","prototype","__proto__"].includes(method)) throw new Error("Invalid named export call");
  return runInvocation(state,()=>{const instance=this._instance??=new Type({...state.ctx,props:restoreRpcProps(props)},state.env);return read?instance[method]:instance[method](...args);});
 }
}
import { WorkerEntrypoint } from "cloudflare:workers";
import { makeInvocation, runInvocation, wrapInvocationExports, restoreRpcProps } from "` + loaderInvocationContextModule + `";
const declarations = ` + string(declarationJSON) + `;
function invocationState(env, ctx) {
  const state = makeInvocation(env, ctx, declarations);
  state.ctx.exports = wrapInvocationExports(state,UserModule,ctx.exports);
  return state;
}
const states = new WeakMap();
const workers = new WeakMap();
let WrappedWorker;
if (typeof UserWorker === "function") {
  WrappedWorker = class extends WorkerEntrypoint {
    constructor(ctx, env) {
      super(ctx, env);
      const state = invocationState(env, ctx);
      states.set(this, state);
    }
  };
  const seen = new Set(["constructor", "env", "ctx"]);
  for (let prototype = UserWorker.prototype; prototype && prototype !== Object.prototype; prototype = Object.getPrototypeOf(prototype)) {
    for (const name of Object.getOwnPropertyNames(prototype)) {
      if (seen.has(name)) continue;
      seen.add(name);
      const descriptor = Object.getOwnPropertyDescriptor(prototype, name);
      if(typeof descriptor.get==="function"){
       Object.defineProperty(WrappedWorker.prototype,name,{configurable:true,get(){const state=states.get(this);return runInvocation(state,()=>{let worker=workers.get(this);if(!worker){worker=new UserWorker(state.ctx,state.env);workers.set(this,worker);}return Reflect.apply(descriptor.get,worker,[]);});}});continue;
      }
      if (typeof descriptor.value !== "function") continue;
      Object.defineProperty(WrappedWorker.prototype, name, {
        configurable: true,
        value: function(...args) {
          const state = states.get(this);
          return runInvocation(state, () => {
            // Construct user code after admission and inside the same context
            // as its methods, including synchronous constructor binding calls.
            let worker = workers.get(this);
            if (!worker) {
              worker = new UserWorker(state.ctx, state.env);
              workers.set(this, worker);
            }
            return descriptor.value.apply(worker, args);
          });
        },
      });
    }
  }
} else {
  WrappedWorker = {};
  for (const [name, method] of Object.entries(UserWorker)) {
    if (typeof method !== "function") { WrappedWorker[name] = method; continue; }
    WrappedWorker[name] = function(first, env, ctx, ...rest) {
      const state = invocationState(env, ctx);
      return runInvocation(state, () => method.call(UserWorker, first, state.env, state.ctx, ...rest));
    };
  }
}
export default WrappedWorker;
`}
	code.moduleOrder = append(code.moduleOrder, main)
	code.MainModule = main
	return code
}

var loaderInvocationContextSource = runtimeassets.ExportPropsModule + "\n" + `import { AsyncLocalStorage } from "node:async_hooks";
import { env as nativeEnv, RpcTarget } from "cloudflare:workers";
const storage = new AsyncLocalStorage();
function recordLog(state,level,args) {
 if(!state?.log) return;
 let message;
 try{message=args.map(value=>typeof value==="string"?value:JSON.stringify(value)).join(" ");}catch{message="[unserializable log]";}
 state.log(level,message);
}
for(const level of ["debug","info","log","warn","error"]){const original=console[level];console[level]=(...args)=>{recordLog(storage.getStore(),level,args);return original.apply(console,args);};}

export function currentInvocationEnv() { return storage.getStore()?.env ?? nativeEnv; }

// workerd dispatches WebSocket events outside the registering async context.
// Capture it at registration, without changing synchronous EventTarget behavior.
// Native property handlers and stream callbacks also run outside the registering
// async context. Preserve their originating admission rather than borrowing an
// unrelated live request from the revision.
function socketCallback(state,listener,receiver,args){
 const result=storage.run(state,()=>Reflect.apply(listener,receiver,args));
 if(result&&typeof result.then==="function")state.ctx.waitUntil(result);
 return result;
}
const acceptedSockets=new WeakSet();
const nativeAccept=WebSocket.prototype.accept;
WebSocket.prototype.accept=function(...args){
 const result=Reflect.apply(nativeAccept,this,args),state=storage.getStore();
 if(state&&!acceptedSockets.has(this)){
  acceptedSockets.add(this);let release;state.ctx.waitUntil(new Promise(resolve=>{release=resolve;}));
  const finished=()=>queueMicrotask(release);
  addSocketListener.call(this,"close",finished,{once:true});
  addSocketListener.call(this,"error",finished,{once:true});
 }
 return result;
};
for(const name of ["onmessage","onclose","onerror"]){
 const descriptor=Object.getOwnPropertyDescriptor(WebSocket.prototype,name);
 const handlers=new WeakMap();
 if(!descriptor){Object.defineProperty(WebSocket.prototype,name,{configurable:true,enumerable:true,get(){return handlers.get(this)?.wrapped??null;},set(listener){const state=storage.getStore();const wrapped=state&&typeof listener==="function"?function(...args){return socketCallback(state,listener,this,args);}:listener;handlers.set(this,{original:listener,wrapped});}});continue;}
 if(!descriptor.set||!descriptor.get)continue;
 Object.defineProperty(WebSocket.prototype,name,{...descriptor,get(){return handlers.get(this)?.original??descriptor.get.call(this);},set(listener){
  const state=storage.getStore();let wrapped=listener;
  if(state&&typeof listener==="function")wrapped=function(...args){return socketCallback(state,listener,this,args);};
  descriptor.set.call(this,wrapped);handlers.set(this,{original:listener});
 }});
}
function scopeStreamConstructor(name,callbacks){
 const NativeStream=globalThis[name];
 globalThis[name]=new Proxy(NativeStream,{construct(Type,args,newTarget){
  const state=storage.getStore(),source=args[0];
  if(state&&source&&typeof source==="object")args=[new Proxy(source,{get(target,name){const value=Reflect.get(target,name,target);return callbacks.includes(name)&&typeof value==="function"?function(...args){return storage.run(state,()=>Reflect.apply(value,target,args));}:value;}}),...args.slice(1)];
  return Reflect.construct(Type,args,newTarget);
 }});
}
scopeStreamConstructor("ReadableStream",["start","pull","cancel"]);
scopeStreamConstructor("WritableStream",["start","write","close","abort"]);
scopeStreamConstructor("TransformStream",["start","transform","flush","cancel"]);
const socketListeners = new WeakMap();
const addSocketListener = WebSocket.prototype.addEventListener;
const removeSocketListener = WebSocket.prototype.removeEventListener;
function socketListener(state, listener) {
  return function(...args) {
    return socketCallback(state,typeof listener==="function"?listener:listener.handleEvent,typeof listener==="function"?this:listener,args);
  };
}
WebSocket.prototype.addEventListener = function(type, listener, options) {
  const state = storage.getStore();
  if (!state || !listener || (typeof listener !== "function" && typeof listener !== "object")) {
    return addSocketListener.call(this, type, listener, options);
  }
  type = String(type);
  const capture = typeof options === "boolean" ? options : Boolean(options?.capture);
  const once = typeof options === "object" && Boolean(options?.once);
  const signal = typeof options === "object" ? options?.signal : undefined;
  let listeners = socketListeners.get(this);
  if (!listeners) { listeners = new WeakMap(); socketListeners.set(this, listeners); }
  let entries = listeners.get(listener);
  if (!entries) { entries = new Map(); listeners.set(listener, entries); }
  const key = JSON.stringify([type, capture]);
  let entry = entries.get(key);
  if (!entry || entry.signal?.aborted) {
    const bound = socketListener(state, listener);
    entry = { signal, wrapped: function(...args) {
      if (once && entries.get(key) === entry) entries.delete(key);
      return Reflect.apply(bound, this, args);
    } };
    entries.set(key, entry);
  }
  return addSocketListener.call(this, type, entry.wrapped, options);
};
WebSocket.prototype.removeEventListener = function(type, listener, options) {
  type = String(type);
  const capture = typeof options === "boolean" ? options : Boolean(options?.capture);
  const entries = listener && socketListeners.get(this)?.get(listener);
  const key = JSON.stringify([type, capture]);
  const entry = entries?.get(key);
  if (entry) entries.delete(key);
  return removeSocketListener.call(this, type, entry?.wrapped ?? listener, options);
};

// Resource facades carry revision authority in their parent capability. They
// live in the base environment so module captures and native callbacks need no
// ambient invocation context. Service calls retain their caller-frame semantics.
export function installImportedBindings(declarations, wrapEnv) {
  const resources = {};
  for (const name of declarations.fetch) resources[name] = resourceBinding(nativeEnv.__NANOFLARE_RESOURCE_BROKER, name);
  for (const name of declarations.kv) resources[name] = namespaceBinding(resourceBinding(nativeEnv.__NANOFLARE_RESOURCE_BROKER, name));
  const wrapped = wrapEnv(resources);
  for (const name of [...declarations.kv, ...declarations.fetch]) {
    Object.defineProperty(nativeEnv, name, { enumerable: true, configurable: true, value: wrapped[name] });
  }
  for (const name of declarations.dispatch || []) {
  Object.defineProperty(nativeEnv,name,{enumerable:true,configurable:true,value:{get(script,options={},dispatchOptions={}) {return {fetch(input,init){const state=storage.getStore();if(!state)throw new Error("Dispatch outside admitted invocation");return state.env.__NANOFLARE_WFP.invoke(name,script,options,dispatchOptions,null,new Request(input,init));}};}}});
 }
 for (const name of declarations.services) {
    // The callback wrappers above retain this exact admission. Never substitute
    // another live invocation when a callback's original lifetime has drained.
    const binding = new Proxy({}, { get(_, method) {
      if (method === "then") return undefined;
      if (typeof method !== "string") return undefined;
      return (...args) => {
        const state = storage.getStore();
        if (!state) throw new Error("Service binding used outside an admitted invocation");
        const target = state.env[name];
        return Reflect.apply(target[method], target, args);
      };
    } });
    Object.defineProperty(nativeEnv, name, { enumerable: true, configurable: true, value: binding });
  }
}

const exportReferences=new WeakMap();
function exportProps(value,seen=new WeakSet()) {
 if(!value||typeof value!=="object") return value;
 const reference=exportReferences.get(value);
 if(reference)return {__nanoflare_export:{...reference,props:exportProps(reference.props,seen)}};
 if(Array.isArray(value))return value.map(item=>exportProps(item,seen));
 if(Object.getPrototypeOf(value)!==Object.prototype&&Object.getPrototypeOf(value)!==null)return value;
 if(seen.has(value))throw new TypeError("Cyclic native props");seen.add(value);
 const out={};for(const [name,item]of Object.entries(value))Object.defineProperty(out,name,{enumerable:true,configurable:true,writable:true,value:exportProps(item,seen)});seen.delete(value);return out;
}
export function wrapInvocationExports(state,module,nativeExports) {
 if(!nativeExports)return nativeExports;
 return new Proxy(nativeExports,{get(target,name){
  const Type=module[name];
  if(typeof Type!=="function" || name==="default") return target[name];
  return (options={})=>{
   let instance;
   const resolveInstance=()=>instance??=new Type({...state.ctx,props:options.props??{}},state.env);
   resolveInstance.prototype=Type.prototype;
   resolveInstance.dispose=()=>instance?.[Symbol.dispose]?.();
   const facade=returnedValue(state,new (class extends RpcTarget {})(),new WeakMap(),resolveInstance);
   const methods=[],properties=[],seen=new Set(["constructor","ctx","env","then"]);
   for(let proto=Type.prototype;proto&&proto!==Object.prototype;proto=Object.getPrototypeOf(proto)){for(const name of Object.getOwnPropertyNames(proto)){if(seen.has(name)||name.startsWith("_"))continue;seen.add(name);const descriptor=Object.getOwnPropertyDescriptor(proto,name);if(typeof descriptor.value==="function")methods.push(name);else if(typeof descriptor.get==="function")properties.push(name);}}
   exportReferences.set(facade,{name,props:options.props??{},methods,properties});return facade;
  };
 }});
}
function returnedValue(state, value, seen = new WeakMap(), exportedInstance) {
  if (!value || typeof value !== "object") return value;
  if (value instanceof RpcTarget) {
    if (state.capabilities.has(value)) return state.capabilities.get(value);
    // Forward public prototype methods and getters in the same admission.
    // Native disposal forwards the original hook without instantiating unused
    // named entrypoints. Own fields retain native RPC's prototype-only boundary.
    class ScopedCapability extends RpcTarget {}
    const resolveInstance=exportedInstance??(()=>value);
    const methods = new Set(["constructor", "then", "__proto__", "prototype","ctx","env"]);
    for (let prototype = exportedInstance?.prototype ?? Object.getPrototypeOf(value); prototype && prototype !== RpcTarget.prototype && prototype!==Object.prototype; prototype = Object.getPrototypeOf(prototype)) {
      for (const name of Object.getOwnPropertyNames(prototype)) {
        if (methods.has(name) || name.startsWith("_")) continue;
        methods.add(name);
        const descriptor = Object.getOwnPropertyDescriptor(prototype, name);
        if(typeof descriptor.get==="function"){Object.defineProperty(ScopedCapability.prototype,name,{get(){return runInvocation(state,()=>Reflect.apply(descriptor.get,resolveInstance(),[]));}});continue;}
        if (typeof descriptor.value !== "function") continue;
        Object.defineProperty(ScopedCapability.prototype, name, {
          value: function(...args) { return runInvocation(state, () => Reflect.apply(descriptor.value, resolveInstance(), args)); },
        });
      }
    }
    if(exportedInstance || typeof value[Symbol.dispose]==="function")Object.defineProperty(ScopedCapability.prototype,Symbol.dispose,{value(){const result=storage.run(state,()=>exportedInstance?exportedInstance.dispose?.():value[Symbol.dispose]());if(result&&typeof result.then==="function")state.ctx.waitUntil(result);}});
    const capability = new ScopedCapability();
    state.capabilities.set(value, capability);
    return capability;
  }
  if (seen.has(value)) return seen.get(value);
  if (Array.isArray(value)) {
    const output = []; seen.set(value, output);
    for (const item of value) output.push(returnedValue(state, item, seen));
    return output;
  }
  if (Object.getPrototypeOf(value) === Object.prototype || Object.getPrototypeOf(value) === null) {
    const output = {}; seen.set(value, output);
    for (const [name, item] of Object.entries(value)) Object.defineProperty(output, name, { enumerable:true, configurable:true, writable:true, value:returnedValue(state, item, seen) });
    return output;
  }
  return value;
}

function resourceBinding(broker, name) {
  return { fetch(input, init) {
    const request = new Request(typeof input === "string" && input.startsWith("/") ? "https://assets.local" + input : input, init);
    request.headers.delete("X-Nanoflare-Invocation");
    request.headers.set("X-Nanoflare-Binding-Name", name);
    return broker.fetch(request);
  } };
}
function namespaceBinding(fetcher) {
  async function checked(response) {
    if (!response.ok) throw new Error("KV operation failed: " + response.status + " " + await response.text());
    return response;
  }
  function keyURL(key) {
    if (typeof key !== "string" || key.length === 0 || key === "." || key === "..") throw new TypeError("KV key must be a non-empty string other than . or ..");
    return new URL("https://kv.local/" + encodeURIComponent(key) + "?urlencoded=true");
  }
  return {
    async get(key, options) {
      const type = typeof options === "string" ? options : options?.type ?? "text";
      if (options && typeof options === "object" && Object.keys(options).some(key => key !== "type")) throw new TypeError("KV cache and metadata options are not supported");
      if (!["text", "json", "arrayBuffer", "stream"].includes(type)) throw new TypeError("Unsupported KV value type");
      const response = await fetcher.fetch(keyURL(key));
      if (response.status === 404) return null;
      await checked(response);
      return type === "stream" ? response.body : response[type]();
    },
    async put(key, value, options = {}) {
      if (Object.keys(options).some(key => !["expiration", "expirationTtl"].includes(key))) throw new TypeError("KV metadata options are not supported");
      const url = keyURL(key);
      if (options.expiration !== undefined) url.searchParams.set("expiration", String(options.expiration));
      if (options.expirationTtl !== undefined) url.searchParams.set("expiration_ttl", String(options.expirationTtl));
      await checked(await fetcher.fetch(url, { method: "PUT", body: value }));
    },
    async delete(key) { await checked(await fetcher.fetch(keyURL(key), { method: "DELETE" })); },
    async list(options = {}) {
      if (Object.keys(options).some(key => !["prefix", "limit", "cursor"].includes(key))) throw new TypeError("Unsupported KV list options");
      const url = new URL("https://kv.local/");
      if (options.prefix !== undefined) url.searchParams.set("prefix", options.prefix);
      if (options.limit !== undefined) url.searchParams.set("key_count_limit", String(options.limit));
      if (options.cursor !== undefined) url.searchParams.set("cursor", options.cursor);
      return (await checked(await fetcher.fetch(url))).json();
    },
  };
}

export function makeInvocation(rawEnv, rawCtx, declarations) {
  const metadata = rawCtx.props?.__nanoflare_invocation;
  if (!metadata || typeof metadata.token !== "string" || !metadata.token || typeof metadata.key !== "string" || !metadata.key) throw new Error("Missing admitted invocation metadata");
  const token = metadata.token;
  const state = { env: {}, pending: 0, running: 0, mainDone: false, completing: false, epoch: 0, capabilities: new WeakMap() };
  for (const [name, value] of Object.entries(rawEnv)) {
    if (!["__NANOFLARE_RESOURCE_BROKER", "__NANOFLARE_LIFECYCLE", "__NANOFLARE_SERVICE_BROKER", "__NANOFLARE_LOG_COLLECTOR", "__NANOFLARE_WFP"].includes(name)) state.env[name] = value;
  }
  for (const name of declarations.services) {
    state.env[name] = new Proxy({}, { get(_, method) {
      if (method === "then") return undefined;
      if (method === "fetch") return (input, init) => rawEnv.__NANOFLARE_SERVICE_BROKER.invoke(token, name, "fetch", [new Request(input, init)]);
      if (typeof method !== "string") return undefined;
      return (...args) => rawEnv.__NANOFLARE_SERVICE_BROKER.invoke(token, name, method, args);
    } });
  }
  state.env.__NANOFLARE_WFP={invoke(binding,script,options,dispatchOptions,root,request){return rawEnv.__NANOFLARE_WFP.invoke(token,binding,script,exportProps(options),dispatchOptions,rawCtx.props.__nanoflare_dispatch_root,request).then(result=>{if(result.error)throw Object.assign(new Error(result.error),{status:result.status});return result.response;});}};
  for(const name of ["__NANOFLARE_ACCESS","__NANOFLARE_DURATION_COLLECTOR"]){if(rawEnv[name])state.env[name]={fetch(input,init){const request=new Request(input,init);request.headers.set("X-Nanoflare-Invocation",token);return rawEnv[name].fetch(request);}};}
  let finish, fail;
  async function lifecycle(operation, epoch) {
    let attempt = 0;
    for (;;) {
      try {
        const response = await rawEnv.__NANOFLARE_LIFECYCLE.fetch("http://nanoflare.lifecycle/" + operation, {
          method: "POST", headers: { "X-Nanoflare-Invocation": token, "X-Nanoflare-Invocation-Epoch": String(epoch) },
        });
        await response.body?.cancel();
        if (response.ok) return;
        if (response.status < 500) {
          const error = new Error("Invocation " + operation + " rejected: " + response.status);
          error.definitive = true;
          throw error;
        }
      } catch (error) { if (error.definitive) throw error; }
      await new Promise(resolve => setTimeout(resolve, Math.min(1000, [20, 100, 500][attempt++] ?? 1000)));
    }
  }
  function beginEpoch() {
    state.epoch++;
    state.started = false;
    state.mainDone = false;
    state.completing = false;
    const completion = new Promise((resolve, reject) => { finish = resolve; fail = reject; });
    rawCtx.waitUntil(completion);
    state.startPromise = lifecycle("start", state.epoch).then(() => { state.started = true; }).catch(error => { finish(); throw error; });
    return state.startPromise;
  }
  state.start = () => {
    if (state.completing) {
      if (!state.transitionPromise) {
        state.transitionPromise = state.completionAck.then(beginEpoch).finally(() => { state.transitionPromise = undefined; });
      }
      return state.transitionPromise;
    }
    return state.startPromise ?? beginEpoch();
  };
  state.maybeComplete = () => {
    if (!state.started || !state.mainDone || state.running || state.pending || state.completing || state.completionQueued) return;
    state.completionQueued = true;
    queueMicrotask(() => {
    state.completionQueued = false;
    if (!state.mainDone || state.running || state.pending || state.completing) return;
    state.completing = true;
    state.completionAck = lifecycle("complete", state.epoch);
    state.completionAck.then(finish, fail);
    });
  };
  state.ctx = {
    props: Object.fromEntries(Object.entries(rawCtx.props ?? {}).filter(([name]) => name !== "__nanoflare_invocation" && name !== "__nanoflare_dispatch_root")),
    exports: rawCtx.exports,
    waitUntil(value) {
      state.pending++;
      const tracked = Promise.resolve(value).then(
        result => { state.pending--; state.maybeComplete(); return result; },
        error => { state.pending--; state.maybeComplete(); throw error; },
      );
      rawCtx.waitUntil(tracked);
    },
    passThroughOnException() { rawCtx.passThroughOnException(); },
  };
  if(rawEnv.__NANOFLARE_LOG_COLLECTOR) state.log=(level,message)=>state.ctx.waitUntil(rawEnv.__NANOFLARE_LOG_COLLECTOR.fetch("http://nanoflare.internal/internal/runtime/logs",{method:"POST",headers:{"content-type":"application/json","X-Nanoflare-Invocation":token},body:JSON.stringify([{logs:[{level,message:[message]}]}])}));
  return state;
}

export function runInvocation(state, callback) {
  state.running++;
  const done = () => { state.running--; state.mainDone = true; state.maybeComplete(); };
  const invoke = () => storage.run(state, () => {
    try {
      const result = callback();
      if (result && typeof result.then === "function") return Promise.resolve(result).then(value => { try { return returnedValue(state,value); } finally { done(); } }, error => {recordLog(state,"error",[String(error)]); done(); throw error; });
      const wrapped = returnedValue(state,result);
      done();
      return wrapped;
    } catch (error) {recordLog(state,"error",[String(error)]); done(); throw error; }
  });
  // Once admitted, ordinary synchronous calls between methods retain their
  // synchronous behavior. Only the first runtime entry waits for the handshake.
  return state.started && !state.completing ? invoke() : state.start().then(invoke, error => {recordLog(state,"error",[String(error)]); done(); throw error; });
}
`
