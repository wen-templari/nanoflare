export interface Identity {
  userId: string;
  tenantId: string;
  roles: string[];
}

export interface PlatformEnv {
  KV: {
    get<T>(key: string): Promise<T | null>;
    put(key: string, value: unknown): Promise<void>;
    delete(key: string): Promise<void>;
  };
  OBJECTS: {
    presignUpload(path: string): Promise<string>;
    presignDownload(path: string): Promise<string>;
    delete(path: string): Promise<void>;
  };
  IDENTITY: {
    get(request: Request): Identity | null;
  };
}

export interface RuntimeClientOptions {
  baseURL: string;
  capability: string;
}

export function createRuntimeClient(options: RuntimeClientOptions): PlatformEnv {
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
    KV: {
      async get<T>(key: string): Promise<T | null> {
        const response = await runtimeRequest<{ value: T | null }>("/internal/runtime/kv/get", { key });
        return response.value;
      },
      async put(key: string, value: unknown): Promise<void> {
        await runtimeRequest("/internal/runtime/kv/put", { key, value });
      },
      async delete(key: string): Promise<void> {
        await runtimeRequest("/internal/runtime/kv/delete", { key });
      },
    },
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
