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
  OBJECTS: R2Bucket;
  IDENTITY: IdentityBinding;
}

declare global {
  interface NanoflareWorkerEnv extends NanoflareEnv {}
}

export {};
