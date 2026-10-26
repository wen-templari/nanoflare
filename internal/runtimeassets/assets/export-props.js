import { RpcTarget as ExportPropsRpcTarget } from "cloudflare:workers";

// A getter must return a native RPC promise on the caller side. An async getter
// on a remote target instead puts an ordinary Promise in workerd's synchronous
// method-path traversal, which cannot descend through it.
export function restoreRpcProps(value) {
  if (!value || typeof value !== "object") return value;
  if (Array.isArray(value)) return value.map(restoreRpcProps);
  if (Object.getPrototypeOf(value) !== Object.prototype && Object.getPrototypeOf(value) !== null)
    return value;
  if (Object.hasOwn(value, "__nanoflare_export_bridge")) {
    const { target, methods, properties } = value.__nanoflare_export_bridge;
    class ExportCapability extends ExportPropsRpcTarget {}
    const seen = new Set(["constructor", "ctx", "env", "then", "__proto__", "prototype"]);
    for (const [names, read] of [[methods, false], [properties, true]]) {
      for (const name of names || []) {
        if (typeof name !== "string" || name.startsWith("_") || seen.has(name)) continue;
        seen.add(name);
        Object.defineProperty(ExportCapability.prototype, name, read ? {
          get() { return target.invoke(name, [], true); },
        } : {
          value(...args) { return target.invoke(name, args, false); },
        });
      }
    }
    Object.defineProperty(ExportCapability.prototype, Symbol.dispose, {
      value() { target[Symbol.dispose](); },
    });
    return new ExportCapability();
  }
  return Object.fromEntries(Object.entries(value).map(([name, item]) => [name, restoreRpcProps(item)]));
}
