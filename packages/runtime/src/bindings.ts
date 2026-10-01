/** The fetch-only service binding that workerd exposes to the wrapper. */
export interface RuntimeServiceBinding {
  fetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response>;
}

export interface RuntimeEnvironment {
  [name: string]: unknown;
}

interface ObjectMetadata {
  key: string;
  size: number;
  etag: string;
  httpEtag: string;
  uploaded: string | Date;
  httpMetadata?: Record<string, unknown>;
}

interface PreparedStatement {
  __sql: string;
  __params: unknown[];
  bind(...values: unknown[]): PreparedStatement;
  run(): Promise<unknown>;
  all(): Promise<unknown>;
  raw(options?: { columnNames?: boolean }): Promise<unknown[]>;
  first(columnName?: string): Promise<unknown>;
}

interface BatchStatement {
  __sql?: string;
  sql?: string;
  __params?: unknown[];
  params?: unknown[];
}

interface DatabaseResponse {
  results?: unknown[];
  raw?: unknown[];
  first?: unknown;
  exec?: unknown;
}

function serviceBinding(value: unknown): RuntimeServiceBinding | undefined {
  if (!value || typeof (value as RuntimeServiceBinding).fetch !== "function") return undefined;
  return value as RuntimeServiceBinding;
}

export function wrapAssetBinding(binding: unknown): unknown {
  const service = serviceBinding(binding);
  if (!service) return binding;
  return {
    fetch(input: RequestInfo | URL, init?: RequestInit) {
      if (typeof input === "string" && input.startsWith("/")) {
        return service.fetch(new Request("https://assets.local" + input, init));
      }
      return service.fetch(input, init);
    },
  };
}

function buildObjectBody(response: Response | null, object: ObjectMetadata): unknown {
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

async function objectBindingError(
  bindingName: string,
  operation: string,
  response: Response,
): Promise<Error> {
  const detail = (await response.text()).trim();
  return new Error(
    detail
      ? bindingName + "." + operation + " failed: " + response.status + " - " + detail
      : bindingName + "." + operation + " failed: " + response.status,
  );
}

async function toObjectRequestInit(
  value: unknown,
  options?: { httpMetadata?: { contentType?: string } },
) {
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
      body: value as ArrayBufferView<ArrayBuffer>,
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
  return {
    body: value as BodyInit | null | undefined,
    contentType: options?.httpMetadata?.contentType || "",
    size: undefined,
  };
}

export function wrapObjectsBinding(bindingName: string, binding: unknown): unknown {
  const service = serviceBinding(binding);
  if (!service) return binding;
  return {
    async put(key: string, value: unknown, options?: { httpMetadata?: { contentType?: string } }) {
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
      const object = JSON.parse(raw) as ObjectMetadata;
      object.uploaded = new Date(object.uploaded);
      return object;
    },
    async get(key: string) {
      const response = await service.fetch(
        new Request("https://objects.local/internal/runtime/objects/" + encodeURIComponent(key)),
      );
      if (response.status === 404) return null;
      if (!response.ok) throw await objectBindingError(bindingName, "get", response);
      return buildObjectBody(response, objectMetadata(response, key));
    },
    async head(key: string) {
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
    async delete(key: string) {
      const response = await service.fetch(
        new Request("https://objects.local/internal/runtime/objects/" + encodeURIComponent(key), {
          method: "DELETE",
        }),
      );
      if (!response.ok) throw await objectBindingError(bindingName, "delete", response);
    },
  };
}

function objectMetadata(response: Response, key: string): ObjectMetadata {
  return {
    key: response.headers.get("x-nanoflare-object-key") || key,
    size: Number(response.headers.get("content-length") || "0"),
    etag: response.headers.get("x-nanoflare-object-etag") || "",
    httpEtag: response.headers.get("etag") || "",
    uploaded: response.headers.get("x-nanoflare-object-uploaded") || new Date(0).toISOString(),
    httpMetadata: { contentType: response.headers.get("content-type") || "" },
  };
}

async function dbRequest(
  bindingName: string,
  binding: RuntimeServiceBinding,
  payload: unknown,
): Promise<DatabaseResponse> {
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
  return response.json() as Promise<DatabaseResponse>;
}

function preparedStatement(
  bindingName: string,
  binding: RuntimeServiceBinding,
  sql: string,
  params: unknown[] = [],
): PreparedStatement {
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

export function wrapDBBinding(bindingName: string, binding: unknown): unknown {
  const service = serviceBinding(binding);
  if (!service) return binding;
  return {
    prepare(sql: string) {
      return preparedStatement(bindingName, service, String(sql));
    },
    async batch(statements: BatchStatement[]) {
      const response = await dbRequest(bindingName, service, {
        method: "batch",
        statements: statements.map((statement) => ({
          sql: statement.__sql || statement.sql,
          params: statement.__params || statement.params || [],
        })),
      });
      return response.results ?? [];
    },
    async exec(sql: string) {
      const response = await dbRequest(bindingName, service, {
        method: "exec",
        statements: [{ sql: String(sql) }],
      });
      return response.exec ?? { count: 0, duration: 0 };
    },
    withSession(initialBookmark?: string) {
      let bookmark =
        typeof initialBookmark === "string" &&
        initialBookmark !== "first-primary" &&
        initialBookmark !== "first-unconstrained"
          ? initialBookmark
          : null;
      const base = this as {
        prepare(sql: string): PreparedStatement;
        batch(statements: PreparedStatement[]): Promise<unknown[]>;
      };
      return {
        prepare(sql: string) {
          const statement = base.prepare(sql);
          return {
            bind(...values: unknown[]) {
              const bound = statement.bind(...values);
              return {
                async run() {
                  const result = (await bound.run()) as { meta?: { bookmark?: string } };
                  bookmark = result?.meta?.bookmark || bookmark;
                  return result;
                },
                all() {
                  return this.run();
                },
                raw(options?: { columnNames?: boolean }) {
                  return bound.raw(options);
                },
                first(columnName?: string) {
                  return bound.first(columnName);
                },
              };
            },
            run: () => statement.run(),
            all: () => statement.all(),
            raw: (options?: { columnNames?: boolean }) => statement.raw(options),
            first: (columnName?: string) => statement.first(columnName),
          };
        },
        batch(statements: PreparedStatement[]) {
          return base.batch(statements);
        },
        getBookmark() {
          return bookmark;
        },
      };
    },
  };
}

export function wrapEnv(
  env: RuntimeEnvironment | undefined,
  assetBindingName: string,
  dbBindingNames: string[],
  objectBindingNames: string[],
): RuntimeEnvironment | undefined {
  if (!env) return env;
  const wrapped = Object.create(env) as RuntimeEnvironment;
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

export function installGlobalBindings(
  target: RuntimeEnvironment,
  dbBindingNames: string[],
  objectBindingNames: string[],
): void {
  for (const name of objectBindingNames) target[name] = wrapObjectsBinding(name, target[name]);
  for (const name of dbBindingNames) target[name] = wrapDBBinding(name, target[name]);
}
