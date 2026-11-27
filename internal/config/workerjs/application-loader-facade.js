const publishedApplicationWasm = new WeakMap();
export function registerPublishedApplicationWasm(module, name) {
  publishedApplicationWasm.set(module, name);
}

function encodeApplicationBinding(value, owner, method) {
  const reference = exportReferences.get(value);
  if (!owner || reference?.owner !== owner || !reference.methods.includes(method)) {
    throw new TypeError(
      "Worker Loader " +
        method +
        " binding requires a ctx.exports entrypoint from this load() invocation",
    );
  }
  return { name: reference.name, props: exportProps(reference.props) };
}

function encodeChildCode(descriptor, owner, fresh = false) {
  const limit = fresh ? freshChildCodeLimit : childCodeLimit;
  return encodeLoaderJSON(
    normalizeChildCode(descriptor, false, publishedApplicationWasm, owner, limit),
    limit,
    fresh ? "WorkerCode exceeds 8 MiB" : "WorkerCode exceeds 512 KiB",
  );
}

class ApplicationCodeCallback extends RpcTarget {
  constructor(callback) {
    super();
    this.callback = callback;
  }

  async code() {
    return encodeChildCode(await this.callback());
  }
}

function applicationEntrypoint(fetch, invoke) {
  let disposed = false;
  function checkOpen() {
    if (disposed) throw new TypeError("Worker Loader entrypoint was disposed");
  }
  const entrypoint = {
    fetch(input, init) {
      checkOpen();
      return fetchWithSignal(new Request(input, init), fetch);
    },
    [Symbol.dispose]() {
      disposed = true;
    },
  };
  return new Proxy(entrypoint, {
    get(target, name) {
      if (Object.hasOwn(target, name)) return target[name];
      if (!isApplicationRpcMethod(name)) return undefined;
      return (...args) => {
        checkOpen();
        return invoke(name, args);
      };
    },
  });
}

function applicationLoader(binding) {
  function invocation() {
    const state = storage.getStore();
    if (!state?.applicationLoader)
      throw new Error("Worker Loader used outside an admitted invocation");
    return state;
  }
  return Object.freeze({
    get(id, callback) {
      if (typeof id !== "string" || typeof callback !== "function") {
        throw new TypeError("Worker Loader get requires a string ID and code callback");
      }
      if (!id.isWellFormed() || new TextEncoder().encode(id).byteLength > 256) {
        throw new RangeError("Worker Loader ID must be valid Unicode and at most 256 bytes");
      }
      return {
        getEntrypoint(name, options) {
          const encodedOptions = encodeEntrypointOptions(options);
          function cached(operation, ...args) {
            const state = invocation();
            return state.applicationLoader[operation](
              state.applicationLoaderToken,
              binding,
              id,
              new ApplicationCodeCallback(() => storage.run(state, callback)),
              name,
              encodedOptions,
              ...args,
            );
          }
          return applicationEntrypoint(
            (request) => cached("fetchCached", request),
            (method, args) => cached("invokeCached", method, args),
          );
        },
      };
    },
    load(descriptor) {
      const owner =
        descriptor?.globalOutbound != null || descriptor?.tails?.length ? invocation() : undefined;
      const code = new Blob([encodeChildCode(descriptor, owner, true)]);
      const ownerToken = owner?.applicationLoaderToken;
      const workers = new WeakMap();
      return {
        getEntrypoint(name, options) {
          const encodedOptions = encodeEntrypointOptions(options);
          function workerForInvocation() {
            const state = invocation();
            if (ownerToken && state.applicationLoaderToken !== ownerToken) {
              throw new TypeError("Worker Loader binding belongs to another invocation");
            }
            let worker = workers.get(state);
            if (!worker) {
              worker = state.applicationLoader.createFresh(
                state.applicationLoaderToken,
                binding,
                code.stream(),
              );
              workers.set(state, worker);
            }
            return worker;
          }
          return applicationEntrypoint(
            async (request) => (await workerForInvocation()).fetch(name, encodedOptions, request),
            async (method, args) =>
              (await workerForInvocation()).invoke(name, encodedOptions, method, args),
          );
        },
      };
    },
  });
}
