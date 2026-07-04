export interface Identity {
  userId: string;
  tenantId: string;
  roles: string[];
}

export interface RequestIdentityHeaders {
  jwt: string | null;
  email: string | null;
}

export interface ObjectHTTPMetadata {
  contentType?: string;
}

export interface R2Object {
  key: string;
  size: number;
  etag: string;
  httpEtag: string;
  uploaded: Date;
  httpMetadata: ObjectHTTPMetadata;
}

export interface R2ObjectBody extends R2Object {
  body: ReadableStream | null;
  readonly bodyUsed: boolean;
  arrayBuffer(): Promise<ArrayBuffer>;
  text(): Promise<string>;
  json<T = unknown>(): Promise<T>;
  blob(): Promise<Blob>;
}

export interface R2PutOptions {
  httpMetadata?: ObjectHTTPMetadata;
}

export type R2PutValue =
  | string
  | ArrayBuffer
  | ArrayBufferView
  | Blob
  | ReadableStream
  | Request
  | Response;

export interface R2Bucket {
  put(key: string, value: R2PutValue, options?: R2PutOptions): Promise<R2Object>;
  get(key: string): Promise<R2ObjectBody | null>;
  head(key: string): Promise<R2Object | null>;
  delete(key: string): Promise<void>;
}

export interface NanoflareEnv {
  KV: KVNamespace;
  ASSETS: AssetFetcher;
  OBJECTS: R2Bucket;
  IDENTITY: {
    get(request: Request): Identity | null;
    headers(request: Request): RequestIdentityHeaders;
  };
}

export interface KVNamespace {
  get(key: string): Promise<string | null>;
  get<T = unknown>(key: string, type: "json"): Promise<T | null>;
  get(key: string, type: "arrayBuffer"): Promise<ArrayBuffer | null>;
  get(key: string, type: "stream"): Promise<ReadableStream | null>;
  put(key: string, value: string | ArrayBuffer | ArrayBufferView | ReadableStream): Promise<void>;
  delete(key: string): Promise<void>;
}

export interface RuntimeClientOptions {
  baseURL: string;
  capability: string;
}

export interface AssetFetcher {
  fetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response>;
}

export function createRuntimeClient(options: RuntimeClientOptions): Omit<NanoflareEnv, "KV"> {
  async function runtimeRequest<T>(path: string, init?: RequestInit): Promise<T> {
    const response = await fetch(new URL(path, options.baseURL), {
      ...init,
      headers: {
        authorization: `Bearer ${options.capability}`,
        ...(init?.headers ?? {}),
      },
    });
    if (!response.ok) {
      throw new Error(`nanoflare runtime request failed: ${response.status}`);
    }
    if (response.status === 204) {
      return undefined as T;
    }
    return (await response.json()) as T;
  }

  return {
    ASSETS: {
      fetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
        const target = assetRuntimeURL(input, options.baseURL);
        return fetch(target, {
          method: init?.method ?? "GET",
          headers: { authorization: `Bearer ${options.capability}` },
        });
      },
    } satisfies AssetFetcher,
    OBJECTS: {
      async put(key: string, value: R2PutValue, options?: R2PutOptions): Promise<R2Object> {
        const body = await normalizeObjectBody(value);
        const response = await runtimeRequest<SerializedR2Object>(objectRuntimePath(key), {
          method: "PUT",
          headers: body.contentType || options?.httpMetadata?.contentType ? { "content-type": body.contentType || options?.httpMetadata?.contentType || "" } : undefined,
          body: body.value,
        });
        return deserializeObject(response);
      },
      async get(key: string): Promise<R2ObjectBody | null> {
        const response = await fetch(new URL(objectRuntimePath(key), options.baseURL), {
          headers: { authorization: `Bearer ${options.capability}` },
        });
        if (response.status === 404) {
          return null;
        }
        if (!response.ok) {
          throw new Error(`nanoflare runtime request failed: ${response.status}`);
        }
        return buildObjectBody(response, deserializeHeaders(response.headers, key));
      },
      async head(key: string): Promise<R2Object | null> {
        const response = await fetch(new URL(objectRuntimePath(key), options.baseURL), {
          method: "HEAD",
          headers: { authorization: `Bearer ${options.capability}` },
        });
        if (response.status === 404) {
          return null;
        }
        if (!response.ok) {
          throw new Error(`nanoflare runtime request failed: ${response.status}`);
        }
        return deserializeHeaders(response.headers, key);
      },
      async delete(key: string): Promise<void> {
        await runtimeRequest(objectRuntimePath(key), { method: "DELETE" });
      },
    },
    IDENTITY: {
      get(request: Request): Identity | null {
        const context = request.headers.get("x-nanoflare-context");
        return context ? (JSON.parse(context) as Identity) : null;
      },
      headers(request: Request): RequestIdentityHeaders {
        return {
          jwt: request.headers.get("x-nanoflare-user-jwt"),
          email: request.headers.get("x-nanoflare-user-email"),
        };
      },
    },
  };
}

interface SerializedR2Object {
  key: string;
  size: number;
  etag?: string;
  httpEtag?: string;
  uploaded: string;
  httpMetadata?: ObjectHTTPMetadata;
}

function deserializeObject(value: SerializedR2Object): R2Object {
  return {
    key: value.key,
    size: value.size,
    etag: value.etag ?? "",
    httpEtag: value.httpEtag ?? "",
    uploaded: new Date(value.uploaded),
    httpMetadata: value.httpMetadata ?? {},
  };
}

function deserializeHeaders(headers: Headers, key: string): R2Object {
  return {
    key: headers.get("x-nanoflare-object-key") ?? key,
    size: Number(headers.get("content-length") ?? "0"),
    etag: headers.get("x-nanoflare-object-etag") ?? "",
    httpEtag: headers.get("etag") ?? "",
    uploaded: new Date(headers.get("x-nanoflare-object-uploaded") ?? new Date(0).toISOString()),
    httpMetadata: {
      contentType: headers.get("content-type") ?? undefined,
    },
  };
}

function buildObjectBody(response: Response, object: R2Object): R2ObjectBody {
  return {
    ...object,
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
    json<T = unknown>() {
      return response.json() as Promise<T>;
    },
    blob() {
      return response.blob();
    },
  };
}

async function normalizeObjectBody(value: R2PutValue): Promise<{ value: BodyInit | null; contentType: string }> {
  if (value instanceof Request || value instanceof Response) {
    return {
      value: value.body,
      contentType: value.headers.get("content-type") ?? "",
    };
  }
  if (value instanceof Blob) {
    return { value, contentType: value.type };
  }
  return { value: value as BodyInit, contentType: "" };
}

function assetRuntimeURL(input: RequestInfo | URL, baseURL: string): URL {
  if (typeof input === "string" && input.startsWith("/")) {
    return new URL(`/internal/runtime/assets${input}`, baseURL);
  }
  const request = new Request(input);
  const url = new URL(request.url);
  return new URL(`/internal/runtime/assets${url.pathname}${url.search}`, baseURL);
}

function objectRuntimePath(key: string): string {
  return `/internal/runtime/objects/${encodeURIComponent(key)}`;
}
