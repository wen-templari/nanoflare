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

export type D1BindValue = string | number | boolean | null | ArrayBuffer | ArrayBufferView;

export interface D1Meta {
  served_by: string;
  served_by_primary: boolean;
  duration: number;
  changes: number;
  last_row_id: number;
  changed_db: boolean;
  size_after: number;
  rows_read: number;
  rows_written: number;
}

export interface D1Result<T = Record<string, unknown>> {
  success: boolean;
  meta: D1Meta;
  results: T[];
}

export interface D1ExecResult {
  count: number;
  duration: number;
}

export interface D1PreparedStatement {
  bind(...values: D1BindValue[]): D1PreparedStatement;
  run<T = Record<string, unknown>>(): Promise<D1Result<T>>;
  all<T = Record<string, unknown>>(): Promise<D1Result<T>>;
  raw<T = unknown[]>(options?: { columnNames?: boolean }): Promise<T[]>;
  first<T = Record<string, unknown>>(columnName?: string): Promise<T | null>;
}

export interface D1DatabaseSession {
  getBookmark(): string | null;
  prepare(query: string): D1PreparedStatement;
  batch<T = Record<string, unknown>>(statements: D1PreparedStatement[]): Promise<D1Result<T>[]>;
}

export interface D1Database {
  prepare(query: string): D1PreparedStatement;
  batch<T = Record<string, unknown>>(statements: D1PreparedStatement[]): Promise<D1Result<T>[]>;
  exec(query: string): Promise<D1ExecResult>;
  withSession(initialBookmark?: "first-primary" | "first-unconstrained" | string): D1DatabaseSession;
}

export interface AssetFetcher {
  fetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response>;
}

export interface NanoflareEnv {
  KV: KVNamespace;
  DB: D1Database;
  ASSETS: AssetFetcher;
  OBJECTS: ObjectStorageBucket;
  IDENTITY: IdentityBinding;
}

export {};
