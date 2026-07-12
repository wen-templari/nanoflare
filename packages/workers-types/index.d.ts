export interface Identity {
  userId: string;
  tenantId: string;
  roles: string[];
}

export interface RequestIdentityHeaders {
  jwt: string | null;
  email: string | null;
}

export interface IdentityBinding {
  get(request: Request): Identity | null;
  headers(request: Request): RequestIdentityHeaders;
}

export interface ObjectHTTPMetadata {
  contentType?: string;
}

export interface ObjectStorageObject {
  key: string;
  size: number;
  etag: string;
  httpEtag: string;
  uploaded: Date;
  httpMetadata: ObjectHTTPMetadata;
}

export interface ObjectStorageObjectBody extends ObjectStorageObject {
  body: ReadableStream | null;
  readonly bodyUsed: boolean;
  arrayBuffer(): Promise<ArrayBuffer>;
  text(): Promise<string>;
  json<T = unknown>(): Promise<T>;
  blob(): Promise<Blob>;
}

export interface ObjectStoragePutOptions {
  httpMetadata?: ObjectHTTPMetadata;
}

export type ObjectStoragePutValue =
  | string
  | ArrayBuffer
  | ArrayBufferView
  | Blob
  | ReadableStream
  | Request
  | Response;

export interface ObjectStorageBucket {
  put(key: string, value: ObjectStoragePutValue, options?: ObjectStoragePutOptions): Promise<ObjectStorageObject>;
  get(key: string): Promise<ObjectStorageObjectBody | null>;
  head(key: string): Promise<ObjectStorageObject | null>;
  delete(key: string): Promise<void>;
}

export interface KVNamespace {
  get(key: string): Promise<string | null>;
  get<T = unknown>(key: string, type: "json"): Promise<T | null>;
  get(key: string, type: "arrayBuffer"): Promise<ArrayBuffer | null>;
  get(key: string, type: "stream"): Promise<ReadableStream | null>;
  put(key: string, value: string | ArrayBuffer | ArrayBufferView | ReadableStream): Promise<void>;
  delete(key: string): Promise<void>;
}

export interface AssetFetcher {
  fetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response>;
}

export interface NanoflareEnv {
  KV: KVNamespace;
  ASSETS: AssetFetcher;
  OBJECTS: ObjectStorageBucket;
  IDENTITY: IdentityBinding;
}

export {};
