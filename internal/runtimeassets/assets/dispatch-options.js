function dispatchOptionsError(message) {
  return Object.assign(new TypeError(message), { status: 422 });
}
// Outbound data is JSON, not an RPC-capability channel. Snapshot at get(), before
// user code can mutate its inputs while fetch admission is pending.
export function snapshotDispatchOptions(options = {}) {
  if (
    !options ||
    Array.isArray(options) ||
    typeof options !== "object" ||
    Object.keys(options).some((name) => name !== "outbound")
  )
    throw dispatchOptionsError("Custom CPU/limit options are not supported by this profile");
  if (options.outbound === undefined) return {};
  const input = options.outbound;
  if (!input || Array.isArray(input) || typeof input !== "object")
    throw dispatchOptionsError("Outbound parameters must be an object");
  const seen = new Set();
  function copy(value, depth = 0) {
    if (depth > 64) throw dispatchOptionsError("Outbound parameters exceed nesting limit");
    if (value === null || typeof value === "string" || typeof value === "boolean") return value;
    if (typeof value === "number" && Number.isFinite(value)) return value;
    if (
      !value ||
      typeof value !== "object" ||
      seen.has(value) ||
      (!Array.isArray(value) &&
        Object.getPrototypeOf(value) !== Object.prototype &&
        Object.getPrototypeOf(value) !== null)
    )
      throw dispatchOptionsError("Outbound parameters must contain only JSON data");
    if (
      Reflect.ownKeys(value).some((key) => typeof key === "symbol") ||
      (Array.isArray(value) &&
        (Object.keys(value).length !== value.length ||
          Object.keys(value).some((key, index) => key !== String(index))))
    )
      throw dispatchOptionsError("Outbound parameters must contain only JSON data");
    seen.add(value);
    const result = Array.isArray(value)
      ? value.map((item) => copy(item, depth + 1))
      : Object.fromEntries(
          Object.entries(value).map(([key, item]) => [key, copy(item, depth + 1)]),
        );
    seen.delete(value);
    return result;
  }
  const outbound = copy(input);
  if (new TextEncoder().encode(JSON.stringify(outbound)).byteLength > 16 * 1024)
    throw dispatchOptionsError("Outbound parameters exceed 16 KiB");
  return { outbound };
}
