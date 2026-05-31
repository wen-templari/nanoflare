export interface Identity {
  userId: string;
  tenantId: string;
  roles: string[];
}

export interface PlatformEnv {
  KV: KVNamespace;
  OBJECTS: {
    presignUpload(path: string): Promise<string>;
    presignDownload(path: string): Promise<string>;
    delete(path: string): Promise<void>;
  };
  IDENTITY: {
    get(request: Request): Identity | null;
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

export function createRuntimeClient(options: RuntimeClientOptions): Omit<PlatformEnv, "KV"> {
  async function runtimeRequest<T>(path: string, body: unknown): Promise<T> {
    const response = await fetch(new URL(path, options.baseURL), {
      method: "POST",
      headers: {
        authorization: `Bearer ${options.capability}`,
        "content-type": "application/json",
      },
      body: JSON.stringify(body),
    });
    if (!response.ok) {
      throw new Error(`platform runtime request failed: ${response.status}`);
    }
    if (response.status === 204) {
      return undefined as T;
    }
    return (await response.json()) as T;
  }

  return {
    OBJECTS: {
      async presignUpload(path: string): Promise<string> {
        const response = await runtimeRequest<{ url: string }>("/internal/runtime/objects/presign-upload", { path });
        return response.url;
      },
      async presignDownload(path: string): Promise<string> {
        const response = await runtimeRequest<{ url: string }>("/internal/runtime/objects/presign-download", { path });
        return response.url;
      },
      async delete(path: string): Promise<void> {
        await runtimeRequest("/internal/runtime/objects/delete", { path });
      },
    },
    IDENTITY: {
      get(request: Request): Identity | null {
        const context = request.headers.get("x-platform-context");
        return context ? (JSON.parse(context) as Identity) : null;
      },
    },
  };
}
