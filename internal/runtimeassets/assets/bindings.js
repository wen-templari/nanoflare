function serviceBinding(value) {
  if (!value || typeof value.fetch !== "function") return undefined;
  return value;
}
export function wrapAssetBinding(binding) {
  const service = serviceBinding(binding);
  if (!service) return binding;
  return {
    fetch(input, init) {
      if (typeof input === "string" && input.startsWith("/")) {
        return service.fetch(new Request("https://assets.local" + input, init));
      }
      return service.fetch(input, init);
    },
  };
}
function buildObjectBody(response, object) {
  if (!response) return null;
  return {
    key: object.key,
    size: object.size,
    etag: object.etag,
    httpEtag: object.httpEtag,
    uploaded: new Date(object.uploaded),
    httpMetadata: object.httpMetadata ?? {},
    body: response.body,
    get bodyUsed() {
      return response.bodyUsed;
    },
    arrayBuffer() {
      return response.arrayBuffer();
    },
    text() {
      return response.text();
    },
    json() {
      return response.json();
    },
    blob() {
      return response.blob();
    },
  };
}
async function objectBindingError(bindingName, operation, response) {
  const detail = (await response.text()).trim();
  return new Error(
    detail
      ? bindingName + "." + operation + " failed: " + response.status + " - " + detail
      : bindingName + "." + operation + " failed: " + response.status,
  );
}
async function toObjectRequestInit(value, options) {
  if (value instanceof Request || value instanceof Response) {
    const contentLength = value.headers.get("content-length");
    return {
      body: value.body,
      contentType: value.headers.get("content-type") || options?.httpMetadata?.contentType || "",
      size: contentLength ? Number(contentLength) : undefined,
    };
  }
  if (typeof value === "string") {
    return {
      body: value,
      contentType: options?.httpMetadata?.contentType || "",
      size: new TextEncoder().encode(value).byteLength,
    };
  }
  if (value instanceof ArrayBuffer) {
    return {
      body: value,
      contentType: options?.httpMetadata?.contentType || "",
      size: value.byteLength,
    };
  }
  if (ArrayBuffer.isView(value)) {
    return {
      body: value,
      contentType: options?.httpMetadata?.contentType || "",
      size: value.byteLength,
    };
  }
  if (value instanceof Blob) {
    return {
      body: value,
      contentType: options?.httpMetadata?.contentType || value.type || "",
      size: value.size,
    };
  }
  return { body: value, contentType: options?.httpMetadata?.contentType || "", size: undefined };
}
export function wrapObjectsBinding(bindingName, binding) {
  const service = serviceBinding(binding);
  if (!service) return binding;
  return {
    async put(key, value, options) {
      const init = await toObjectRequestInit(value, options);
      const response = await service.fetch(
        new Request("https://objects.local/internal/runtime/objects/" + encodeURIComponent(key), {
          method: "PUT",
          headers: init.contentType ? { "content-type": init.contentType } : undefined,
          body: init.body,
        }),
      );
      if (!response.ok) throw await objectBindingError(bindingName, "put", response);
      const raw = await response.text();
      if (!raw.trim())
        return {
          key,
          size: init.size ?? 0,
          etag: "",
          httpEtag: "",
          uploaded: new Date(),
          httpMetadata: { contentType: init.contentType || "" },
        };
      const object = JSON.parse(raw);
      object.uploaded = new Date(object.uploaded);
      return object;
    },
    async get(key) {
      const response = await service.fetch(
        new Request("https://objects.local/internal/runtime/objects/" + encodeURIComponent(key)),
      );
      if (response.status === 404) return null;
      if (!response.ok) throw await objectBindingError(bindingName, "get", response);
      return buildObjectBody(response, objectMetadata(response, key));
    },
    async head(key) {
      const response = await service.fetch(
        new Request("https://objects.local/internal/runtime/objects/" + encodeURIComponent(key), {
          method: "HEAD",
        }),
      );
      if (response.status === 404) return null;
      if (!response.ok) throw await objectBindingError(bindingName, "head", response);
      const object = objectMetadata(response, key);
      return { ...object, uploaded: new Date(object.uploaded) };
    },
    async delete(key) {
      const response = await service.fetch(
        new Request("https://objects.local/internal/runtime/objects/" + encodeURIComponent(key), {
          method: "DELETE",
        }),
      );
      if (!response.ok) throw await objectBindingError(bindingName, "delete", response);
    },
  };
}
function objectMetadata(response, key) {
  return {
    key: response.headers.get("x-nanoflare-object-key") || key,
    size: Number(response.headers.get("content-length") || "0"),
    etag: response.headers.get("x-nanoflare-object-etag") || "",
    httpEtag: response.headers.get("etag") || "",
    uploaded: response.headers.get("x-nanoflare-object-uploaded") || new Date(0).toISOString(),
    httpMetadata: { contentType: response.headers.get("content-type") || "" },
  };
}
async function dbRequest(bindingName, binding, payload) {
  const response = await binding.fetch(
    new Request("https://db.local/internal/runtime/db/query", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(payload),
    }),
  );
  if (!response.ok) {
    const detail = (await response.text()).trim();
    throw new Error(
      detail
        ? bindingName + " query failed: " + response.status + " - " + detail
        : bindingName + " query failed: " + response.status,
    );
  }
  return response.json();
}
function preparedStatement(bindingName, binding, sql, params = []) {
  return {
    __sql: sql,
    __params: params,
    bind(...values) {
      return preparedStatement(bindingName, binding, sql, values);
    },
    async run() {
      const response = await dbRequest(bindingName, binding, {
        method: "run",
        statements: [{ sql, params }],
      });
      return response.results?.[0] ?? { success: true, meta: {}, results: [] };
    },
    async all() {
      return this.run();
    },
    async raw(options) {
      const response = await dbRequest(bindingName, binding, {
        method: "raw",
        column_names: !!options?.columnNames,
        statements: [{ sql, params }],
      });
      return response.raw ?? [];
    },
    async first(columnName) {
      const response = await dbRequest(bindingName, binding, {
        method: "first",
        first_column: columnName || "",
        statements: [{ sql, params }],
      });
      return response.first ?? null;
    },
  };
}
export function wrapDBBinding(bindingName, binding) {
  const service = serviceBinding(binding);
  if (!service) return binding;
  return {
    prepare(sql) {
      return preparedStatement(bindingName, service, String(sql));
    },
    async batch(statements) {
      const response = await dbRequest(bindingName, service, {
        method: "batch",
        statements: statements.map((statement) => ({
          sql: statement.__sql || statement.sql,
          params: statement.__params || statement.params || [],
        })),
      });
      return response.results ?? [];
    },
    async exec(sql) {
      const response = await dbRequest(bindingName, service, {
        method: "exec",
        statements: [{ sql: String(sql) }],
      });
      return response.exec ?? { count: 0, duration: 0 };
    },
    withSession(initialBookmark) {
      let bookmark =
        typeof initialBookmark === "string" &&
        initialBookmark !== "first-primary" &&
        initialBookmark !== "first-unconstrained"
          ? initialBookmark
          : null;
      const base = this;
      return {
        prepare(sql) {
          const statement = base.prepare(sql);
          return {
            bind(...values) {
              const bound = statement.bind(...values);
              return {
                async run() {
                  const result = await bound.run();
                  bookmark = result?.meta?.bookmark || bookmark;
                  return result;
                },
                all() {
                  return this.run();
                },
                raw(options) {
                  return bound.raw(options);
                },
                first(columnName) {
                  return bound.first(columnName);
                },
              };
            },
            run: () => statement.run(),
            all: () => statement.all(),
            raw: (options) => statement.raw(options),
            first: (columnName) => statement.first(columnName),
          };
        },
        batch(statements) {
          return base.batch(statements);
        },
        getBookmark() {
          return bookmark;
        },
      };
    },
  };
}
export function wrapEnv(env, assetBindingName, dbBindingNames, objectBindingNames) {
  if (!env) return env;
  const wrapped = Object.create(env);
  if (env[assetBindingName])
    Object.defineProperty(wrapped, assetBindingName, {
      value: wrapAssetBinding(env[assetBindingName]),
      enumerable: true,
    });
  for (const name of objectBindingNames)
    if (env[name])
      Object.defineProperty(wrapped, name, {
        value: wrapObjectsBinding(name, env[name]),
        enumerable: true,
      });
  for (const name of dbBindingNames)
    if (env[name])
      Object.defineProperty(wrapped, name, {
        value: wrapDBBinding(name, env[name]),
        enumerable: true,
      });
  return wrapped;
}
export function installGlobalBindings(target, dbBindingNames, objectBindingNames) {
  for (const name of objectBindingNames) target[name] = wrapObjectsBinding(name, target[name]);
  for (const name of dbBindingNames) target[name] = wrapDBBinding(name, target[name]);
}
