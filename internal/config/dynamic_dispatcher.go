package config

import (
	"fmt"
	"github.com/clas/nanoflare/internal/runtimeassets"
	"strings"
)

// DynamicDispatcher configures one stable, authenticated workerd socket.
// Publication and traffic selection remain the Go gateway's responsibility.
func DynamicDispatcher(options WorkerdOptions, socketPath, brokerAddr, brokerToken, dispatcherToken string) string {
	outbound := "internet"
	var out strings.Builder
	out.WriteString("using Workerd = import \"/workerd/workerd.capnp\";\nconst config :Workerd.Config = (\n services = [\n")
	fmt.Fprintf(&out, "(name = \"internet\", network = (allow = [%s], tlsOptions = (trustBrowserCas = true))),\n", quotedList(normalizedNetworkAllow(options.NetworkAllow)))
	if address := strings.TrimSpace(options.EgressAddr); address != "" {
		outbound = "nanoflare-egress"
		fmt.Fprintf(&out, "(name = %s, external = (address = %s, http = (style = proxy))),\n", quote(outbound), quote(address))
	}
	writeWFPSocketService(&out, options.WFPSockets)
	fmt.Fprintf(&out, `(name = "broker", external = (address = %s, http = (injectRequestHeaders = [(name = "X-Nanoflare-Broker-Token", value = %s)]))),
(name = "dispatcher", worker = (
  compatibilityDate = "2026-09-08",
  compatibilityFlags = ["experimental", "enable_abortsignal_rpc"],
  modules = [(name = "dispatcher.js", esModule = %s), (name = "dispatch.js", esModule = %s)],
  bindings = [
    %s(name = "LOADER", workerLoader = (id = "nanoflare-publications")),
    (name = "BROKER", service = "broker"),
    (name = "OUTBOUND", service = %s),
    (name = "DISPATCHER_TOKEN", text = %s)
  ],
)),
 ],
 sockets = [(name = "dispatcher", address = %s, http = (forwardedProtoHeader = "X-Forwarded-Proto"), service = "dispatcher")],
);
`, quote(brokerAddr), quote(brokerToken), quote(dynamicDispatcherSource), quote(runtimeassets.DispatchModule), strings.Join(append(wfpSocketBindings(options.WFPSockets), ""), ",\n    "), quote(outbound), quote(dispatcherToken), quote("unix:"+socketPath))
	return out.String()
}

const dynamicDispatcherSource = `import { WorkerEntrypoint } from "cloudflare:workers";
import { wrapDispatchEnv, setDispatchExports } from "./dispatch.js";
let parentExports;
export { DispatchBroker } from "./dispatch.js";
function restoreExportProps(value,key,token) {
 if(!value||typeof value!=="object")return value;
 if(Array.isArray(value))return value.map(item=>restoreExportProps(item,key,token));
 if(Object.getPrototypeOf(value)!==Object.prototype&&Object.getPrototypeOf(value)!==null)return value;
 if(Object.hasOwn(value,"__nanoflare_export")){const ref=value.__nanoflare_export;return {__nanoflare_export_bridge:{target:parentExports.NamedExportBridge({props:{key,token,name:ref.name,props:restoreExportProps(ref.props,key,token),methods:ref.methods,properties:ref.properties}}),methods:ref.methods,properties:ref.properties}};}
 return Object.fromEntries(Object.entries(value).map(([name,item])=>[name,restoreExportProps(item,key,token)]));
}
export class NamedExportBridge extends WorkerEntrypoint {
 async invoke(method,args,read) {
  const {key,token,name,props,methods=[],properties=[]}=this.ctx.props;
  if(typeof method!=="string" || method.startsWith("_") || ["constructor","ctx","env","then","__proto__","prototype"].includes(method) || !(read?properties:methods).includes(method) || !Array.isArray(args)) throw new TypeError("Invalid named export call");
  this._target??=(async()=>{const response=await this.env.BROKER.fetch(brokerURL("/export/"+key),{method:"POST",headers:{"X-Nanoflare-Invocation":token}});if(!response.ok)throw new Error("Export admission rejected: "+response.status);const child=await response.json();const worker=await load(this.env,this.ctx,key);return worker.getEntrypoint("NanoflareExportDispatcher",{props:{__nanoflare_invocation:{key,token:child.token}}});})();
  return (await this._target).invoke(name,props,method,args,read);
 }
}
export class WFPBridge extends WorkerEntrypoint {
 async invoke(token,binding,script,options,dispatchOptions,root,request) {
  const control=parentExports.ResourceBridge({props:{key:this.ctx.props.key,binding:"__NANOFLARE_DISPATCH:"+binding,token}});
  const env={__NANOFLARE_WFP_SOCKET_OUTBOUND:this.env.__NANOFLARE_WFP_SOCKET_OUTBOUND,__NANOFLARE_RESOURCE_CONTROL:{key:this.ctx.props.key,binding:"__NANOFLARE_DISPATCH:"+binding,token},__NANOFLARE_WORKER_LOADER:this.env.LOADER,__NANOFLARE_DISPATCH_BINDINGS:{[binding]:"control"},control};
  try{return {response:await wrapDispatchEnv(env,this.ctx,root)[binding].get(script,restoreExportProps(options,this.ctx.props.key,token),dispatchOptions).fetch(request)};}
  catch(error){return {error:error.message,status:error.status||500};}
 }
}


function brokerURL(path) { return "http://nanoflare.broker" + path; }

async function reportInitialization(env, key, error) {
  // This endpoint is reachable only from the authenticated parent dispatcher.
  // The broker supplies the frozen snapshot and owning runner session.
  const reference=crypto.randomUUID();
  let detail;
  try {detail=String(error);} catch {detail="[unprintable exception]";}
  const message="initialization ["+reference+"]: "+detail;
  try {await env.BROKER.fetch(brokerURL("/diagnostic/"+encodeURIComponent(key)),{method:"POST",headers:{"content-type":"application/json"},body:JSON.stringify({message:new TextEncoder().encode(message).byteLength<=4096?message:"initialization ["+reference+"]: [exception exceeds collection limit]"})});} catch {}
}

async function load(env, ctx, key, validation = false, state = {}) {
  const source = async () => {
    const response = await env.BROKER.fetch(brokerURL("/source/" + encodeURIComponent(key)));
    if (!response.ok) throw new Error("Worker source unavailable: " + response.status);
    return await response.json();
  };
  // Fetch before entering the loader so even a cached initialization exception
  // can be distinguished from an unavailable source broker.
  const validationCode = validation ? await source() : null;
  state.sourceReady = validation;
  return env.LOADER.get((validation ? "validation:" : "execution:") + key, async () => {
    const code = validationCode || await source();
    const childEnv = {
      ...code.env,
      __NANOFLARE_RESOURCE_BROKER: parentExports.ResourceBridge({ props: { key } }),
      __NANOFLARE_LIFECYCLE: parentExports.LifecycleBridge({ props: { key } }),
      __NANOFLARE_WFP: parentExports.WFPBridge({props:{key}}),
      __NANOFLARE_LOG_COLLECTOR: parentExports.ResourceBridge({props:{key,binding:"__NANOFLARE_LOG_COLLECTOR"}}),
      __NANOFLARE_ACCESS: parentExports.ResourceBridge({props:{key,binding:"__NANOFLARE_ACCESS"}}),
      __NANOFLARE_SERVICE_BROKER: parentExports.ServiceBridge({ props: { key } }),
      __NANOFLARE_DNS: parentExports.ResourceBridge({ props: { key, binding: "__NANOFLARE_DNS" } }),
      __NANOFLARE_DURATION_COLLECTOR: parentExports.ResourceBridge({ props: { key, binding: "__NANOFLARE_DURATION_COLLECTOR" } }),
    };
    let mainModule = code.mainModule;
    let modules = Object.fromEntries(Object.entries(code.modules).map(([name, module]) => [name,
      Object.hasOwn(module, "wasm") ? {wasm: Uint8Array.from(atob(module.wasm), byte => byte.charCodeAt(0)).buffer} : module
    ]));
    if (validation) {
      const name = "__nanoflare_validation__.js";
      if (Object.hasOwn(modules, name)) throw new Error("Reserved validation module name");
      modules = { ...modules, [name]: { js: "import " + JSON.stringify("./" + mainModule) + "; export default { fetch() { return new Response(null, {status:204}); } };" } };
      mainModule = name;
    }
    return {
      allowExperimental: true,
      compatibilityDate: code.compatibilityDate,
      compatibilityFlags: code.compatibilityFlags,
      mainModule, modules, env: childEnv, globalOutbound: env.OUTBOUND,
    };
  });
}

async function completeWithoutExecution(env, key, token) {
  if (!token) return;
  await env.BROKER.fetch(brokerURL("/complete/" + encodeURIComponent(key)), {
    method: "POST", headers: { "X-Nanoflare-Invocation": token },
  });
}

export class ResourceBridge extends WorkerEntrypoint {
  fetch(input) {
    const request = new Request(input);
    const original = new URL(request.url);
    if(this.ctx.props.token) request.headers.set("X-Nanoflare-Control-Invocation",this.ctx.props.token);
    if (this.ctx.props.binding) request.headers.set("X-Nanoflare-Binding-Name", this.ctx.props.binding);
    const path = "/resource/" + encodeURIComponent(this.ctx.props.key) + original.pathname + original.search;
    return this.env.BROKER.fetch(new Request(brokerURL(path), request));
  }
}

export class LifecycleBridge extends WorkerEntrypoint {
  fetch(input) {
    const request = new Request(input);
    const operation = new URL(request.url).pathname;
    if (operation !== "/complete" && operation !== "/start") return new Response("Unknown lifecycle operation", { status: 400 });
    return this.env.BROKER.fetch(new Request(brokerURL(operation + "/" + encodeURIComponent(this.ctx.props.key)), request));
  }
}

const forbiddenMethods = new Set(["constructor", "__proto__", "prototype", "ctx", "env", "waitUntil", "then", "connect", "scheduled", "alarm", "webSocketMessage", "webSocketClose", "webSocketError"]);
export class ServiceBridge extends WorkerEntrypoint {
  async invoke(token, binding, method, args) {
    if (typeof token !== "string" || !token || typeof binding !== "string" || !binding || typeof method !== "string" || method.startsWith("_") || forbiddenMethods.has(method) || !Array.isArray(args)) throw new TypeError("Unsupported service call");
    const response = await this.env.BROKER.fetch(brokerURL("/service/" + encodeURIComponent(this.ctx.props.key) + "/" + encodeURIComponent(binding)), {
      method: "POST", headers: { "X-Nanoflare-Invocation": token },
    });
    if (!response.ok) throw new Error("Service admission rejected: " + response.status);
    const child = await response.json();
    let loaded;
    try { loaded = await load(this.env, this.ctx, child.key); }
    catch (error) { this.ctx.waitUntil(completeWithoutExecution(this.env, child.key, child.token)); throw error; }
    try {
      const target = loaded.getEntrypoint(undefined, { props: { __nanoflare_invocation: { key: child.key, token: child.token } } });
      return await (method === "fetch" ? target.fetch(...args) : target[method](...args));
    } catch (error) {
      // The broker revokes only admissions that never acknowledged /start;
      // started user methods may still have legitimate waitUntil work.
      this.ctx.waitUntil(this.env.BROKER.fetch(brokerURL("/abandon/" + encodeURIComponent(child.key)), {
        method: "POST", headers: { "X-Nanoflare-Invocation": child.token },
      }));
      throw error;
    }
  }
}

export default {
  async fetch(request, env, ctx) {
    parentExports ??= ctx.exports;setDispatchExports(parentExports);
    if (request.headers.get("X-Nanoflare-Dispatcher-Token") !== env.DISPATCHER_TOKEN) return new Response("Unauthorized", { status: 401 });
    const key = request.headers.get("X-Nanoflare-Execution");
    if (!key) return new Response("Missing execution identity", { status: 400 });
    const validation = request.headers.get("X-Nanoflare-Validate") === "true";
    const token = request.headers.get("X-Nanoflare-Invocation");
    if (!validation && !token) return new Response("Missing invocation admission", { status: 400 });
    // Only initialization failures after fetching a valid source response are
    // revision failures. Broker/transport/authentication failures stay fatal.
    if (validation) {
      const state = {};
      try {
        const worker = await load(env, ctx, key, true, state);
        return await worker.getEntrypoint().fetch(request);
      } catch (error) {
        if (!state.sourceReady) throw error;
        await reportInitialization(env,key,error);
        return new Response("Worker initialization failed", {status:422, headers:{"X-Nanoflare-Validation-Failure":"worker_initialization"}});
      }
    }
    let loaded;
    try { loaded = await load(env, ctx, key, validation); }
    catch (error) {
      if (!validation) ctx.waitUntil(completeWithoutExecution(env, key, token));
      throw error;
    }
    const root = request.headers.get("X-Nanoflare-Dispatch-Root");
    const headers = new Headers(request.headers);
    for (const name of [...headers.keys()]) if (name.toLowerCase().startsWith("x-nanoflare-") && !["x-nanoflare-user-jwt", "x-nanoflare-user-email"].includes(name.toLowerCase())) headers.delete(name);
    return loaded.getEntrypoint(undefined, { props: { __nanoflare_invocation: { key, token }, __nanoflare_dispatch_root:root } }).fetch(new Request(request, { headers }));
  },
};
`
