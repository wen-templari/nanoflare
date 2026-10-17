package config

import (
	"fmt"
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
	fmt.Fprintf(&out, `(name = "broker", external = (address = %s, http = (injectRequestHeaders = [(name = "X-Nanoflare-Broker-Token", value = %s)]))),
(name = "dispatcher", worker = (
  compatibilityDate = "2026-09-08",
  modules = [(name = "dispatcher.js", esModule = %s)],
  bindings = [
    (name = "LOADER", workerLoader = (id = "nanoflare-publications")),
    (name = "BROKER", service = "broker"),
    (name = "OUTBOUND", service = %s),
    (name = "DISPATCHER_TOKEN", text = %s)
  ],
)),
 ],
 sockets = [(name = "dispatcher", address = %s, http = (), service = "dispatcher")],
);
`, quote(brokerAddr), quote(brokerToken), quote(dynamicDispatcherSource), quote(outbound), quote(dispatcherToken), quote("unix:"+socketPath))
	return out.String()
}

const dynamicDispatcherSource = `import { WorkerEntrypoint } from "cloudflare:workers";

function brokerURL(path) { return "http://nanoflare.broker" + path; }

async function load(env, ctx, key, validation = false) {
  return env.LOADER.get((validation ? "validation:" : "execution:") + key, async () => {
    const response = await env.BROKER.fetch(brokerURL("/source/" + encodeURIComponent(key)));
    if (!response.ok) throw new Error("Worker source unavailable: " + response.status);
    const code = await response.json();
    const childEnv = {
      ...code.env,
      __NANOFLARE_RESOURCE_BROKER: ctx.exports.ResourceBridge({ props: { key } }),
      __NANOFLARE_LIFECYCLE: ctx.exports.LifecycleBridge({ props: { key } }),
      __NANOFLARE_SERVICE_BROKER: ctx.exports.ServiceBridge({ props: { key } }),
      __NANOFLARE_DNS: ctx.exports.ResourceBridge({ props: { key, binding: "__NANOFLARE_DNS" } }),
      __NANOFLARE_DURATION_COLLECTOR: ctx.exports.ResourceBridge({ props: { key, binding: "__NANOFLARE_DURATION_COLLECTOR" } }),
    };
    let mainModule = code.mainModule;
    let modules = code.modules;
    if (validation) {
      const name = "__nanoflare_validation__.js";
      if (Object.hasOwn(modules, name)) throw new Error("Reserved validation module name");
      modules = { ...modules, [name]: { js: "import " + JSON.stringify("./" + mainModule) + "; export default { fetch() { return new Response(null, {status:204}); } };" } };
      mainModule = name;
    }
    return {
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
    if (request.headers.get("X-Nanoflare-Dispatcher-Token") !== env.DISPATCHER_TOKEN) return new Response("Unauthorized", { status: 401 });
    const key = request.headers.get("X-Nanoflare-Execution");
    if (!key) return new Response("Missing execution identity", { status: 400 });
    const validation = new URL(request.url).pathname === "/__nanoflare_validate";
    const token = request.headers.get("X-Nanoflare-Invocation");
    if (!validation && !token) return new Response("Missing invocation admission", { status: 400 });
    let loaded;
    try { loaded = await load(env, ctx, key, validation); }
    catch (error) {
      if (!validation) ctx.waitUntil(completeWithoutExecution(env, key, token));
      throw error;
    }
    const headers = new Headers(request.headers);
    for (const name of [...headers.keys()]) if (name.toLowerCase().startsWith("x-nanoflare-")) headers.delete(name);
    return loaded.getEntrypoint(undefined, { props: { __nanoflare_invocation: { key, token } } }).fetch(new Request(request, { headers }));
  },
};
`
