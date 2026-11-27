const childCodeLimit = 512 * 1024;
const freshChildCodeLimit = 8 * 1024 * 1024;

function isLoaderRecord(value) {
  if (value === null || typeof value !== "object") return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function normalizeChildCode(descriptor, decode, publishedWasm, owner, limit = childCodeLimit) {
  if (!isLoaderRecord(descriptor)) {
    throw new TypeError("Worker Loader requires a WorkerCode object");
  }
  const fields = new Set([
    "compatibilityDate",
    "compatibilityFlags",
    "mainModule",
    "modules",
    "env",
    "globalOutbound",
    "tails",
    "limits",
  ]);
  for (const field of Object.keys(descriptor)) {
    if (!fields.has(field)) throw new TypeError("Unsupported WorkerCode field: " + field);
  }
  const { globalOutbound, tails, limits } = descriptor;
  function binding(value, method) {
    if (!decode) return encodeApplicationBinding(value, owner, method);
    if (!isLoaderRecord(value) || typeof value.name !== "string" || !isLoaderRecord(value.props)) {
      throw new TypeError("Invalid Worker Loader " + method + " entrypoint reference");
    }
    return value;
  }
  if (tails !== undefined && (!Array.isArray(tails) || tails.length > 8)) {
    throw new TypeError("Worker Loader tails must be an array of up to eight entrypoints");
  }
  if (
    limits !== undefined &&
    (!isLoaderRecord(limits) ||
      Object.entries(limits).some(
        ([name, value]) =>
          !["cpuMs", "subRequests"].includes(name) ||
          (value !== undefined &&
            (!Number.isSafeInteger(value) || value < 0 || value > 0xffffffff)),
      ))
  ) {
    throw new TypeError("Invalid Worker Loader limits");
  }
  const { compatibilityDate, mainModule, modules, env } = descriptor;
  if (
    typeof compatibilityDate !== "string" ||
    typeof mainModule !== "string" ||
    !isLoaderRecord(modules) ||
    !Object.hasOwn(modules, mainModule)
  ) {
    throw new TypeError("WorkerCode requires compatibilityDate, mainModule and modules");
  }
  let wasmBytes = 0;
  const normalizedModules = Object.fromEntries(
    Object.entries(modules).map(([name, module]) => {
      if (typeof module === "string") return [name, module];
      if (!isLoaderRecord(module)) throw new TypeError("Invalid Worker module");
      const fields = Object.keys(module);
      if (fields.length === 1 && fields[0] === "wasm") {
        const wasm = module.wasm;
        if (decode && typeof wasm === "string")
          return [name, { wasm: Uint8Array.fromBase64(wasm).buffer }];
        if (
          decode &&
          isLoaderRecord(wasm) &&
          Object.keys(wasm).length === 1 &&
          typeof wasm.published === "string"
        ) {
          return [name, { wasm }];
        }
        if (!decode && wasm instanceof WebAssembly.Module) {
          const published = publishedWasm.get(wasm);
          if (published === undefined)
            throw new TypeError(
              "Worker Loader compiled Wasm must be imported from the parent revision",
            );
          return [name, { wasm: { published } }];
        }
        if (!decode && (wasm instanceof ArrayBuffer || ArrayBuffer.isView(wasm))) {
          const bytes =
            wasm instanceof ArrayBuffer
              ? new Uint8Array(wasm)
              : new Uint8Array(wasm.buffer, wasm.byteOffset, wasm.byteLength);
          wasmBytes += bytes.byteLength;
          if (wasmBytes > limit)
            throw new RangeError("WorkerCode exceeds " + (limit >> 10) + " KiB");
          return [name, { wasm: bytes.toBase64() }];
        }
        throw new TypeError("Worker Loader Wasm modules require bytes");
      }
      if (
        fields.length !== 1 ||
        !["js", "cjs", "text", "json"].includes(fields[0]) ||
        (fields[0] !== "json" && typeof module[fields[0]] !== "string")
      ) {
        throw new TypeError("Unsupported Worker module type");
      }
      return [name, module];
    }),
  );
  if (env !== undefined && (!env || typeof env !== "object" || Array.isArray(env))) {
    throw new TypeError("WorkerCode.env must be an object");
  }
  return {
    ...descriptor,
    modules: normalizedModules,
    globalOutbound: globalOutbound == null ? null : binding(globalOutbound, "fetch"),
    tails: tails?.map((value) => binding(value, "tail")),
  };
}

function encodeLoaderJSON(input, limit, message) {
  const encoded = JSON.stringify(input, (_, value) => {
    if (
      value !== null &&
      typeof value === "object" &&
      !Array.isArray(value) &&
      !isLoaderRecord(value)
    ) {
      throw new TypeError("WorkerCode supports JSON values only");
    }
    if (
      typeof value === "function" ||
      typeof value === "symbol" ||
      (typeof value === "number" && !Number.isFinite(value))
    ) {
      throw new TypeError("Worker Loader supports JSON values only");
    }
    return value;
  });
  if (new TextEncoder().encode(encoded).byteLength > limit) throw new RangeError(message);
  return encoded;
}

function encodeEntrypointOptions(options) {
  if (options === undefined) return undefined;
  if (
    !options ||
    typeof options !== "object" ||
    Array.isArray(options) ||
    Object.keys(options).some((field) => field !== "props")
  ) {
    throw new TypeError("Worker Loader entrypoint options only support props");
  }
  return encodeLoaderJSON(options, 16 * 1024, "Worker Loader props exceed 16 KiB");
}

function isApplicationRpcMethod(name) {
  return (
    typeof name === "string" &&
    !name.startsWith("_") &&
    ![
      "constructor",
      "prototype",
      "ctx",
      "env",
      "then",
      "toJSON",
      "toString",
      "valueOf",
      "fetch",
      "connect",
      "scheduled",
      "alarm",
      "webSocketMessage",
      "webSocketClose",
      "webSocketError",
    ].includes(name)
  );
}
