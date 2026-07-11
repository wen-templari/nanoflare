import type { Dispatch, SetStateAction } from "react";

export type Section = "overview" | "workers" | "kv" | "object-storage";
export type WorkerAuth = { protected_routes?: string[] };
export type WorkerKVNamespaceBinding = { binding: string; id: string; preview_id?: string };
export type WorkerObjectStorageBucketBinding = { binding: string; bucket_id: string };
export type WorkerAssetConfig = {
  binding?: string;
  html_handling?: string;
  not_found_handling?: string;
  run_worker_first?: true | string[];
};
export type WorkerTriggerConfig = { crons?: string[] };
export type WorkerBinding = {
  kind: "kv" | "asset" | "object_storage_bucket";
  binding: string;
  namespace_id?: string;
  namespace_name?: string;
  bucket_id?: string;
  bucket_name?: string;
  asset_count?: number;
};

export type Worker = {
  id: string;
  name: string;
  hostname: string;
  created_at: string;
  auth?: WorkerAuth;
  status?: "live" | "draft";
  requests?: string;
  deployment?: string;
  bindings?: WorkerBinding[];
};

export type WorkerDetailTab = "overview" | "deployments" | "files" | "output" | "settings";

export type WorkerDeployment = {
  id: string;
  entrypoint: string;
  bundle_size: number;
  asset_count?: number;
  asset_config?: WorkerAssetConfig;
  compatibility_date: string;
  triggers?: WorkerTriggerConfig;
  vars?: Record<string, unknown>;
  created_at: string;
  kv_namespaces?: WorkerKVNamespaceBinding[];
  object_storage_buckets?: WorkerObjectStorageBucketBinding[];
  bindings?: WorkerBinding[];
};

export type WorkerSecret = { name: string; created_at: string; updated_at: string };
export type WorkerDetailData = { app: Worker; deployment?: WorkerDeployment; secrets?: WorkerSecret[] };

export type ConsoleDeployment = {
  id: string;
  app_id: string;
  app_name: string;
  hostname: string;
  entrypoint: string;
  bundle_size: number;
  compatibility_date: string;
  triggers?: WorkerTriggerConfig;
  state: "active" | "inactive";
  created_at: string;
};

export type WorkerFile = { name: string; path: string; size: number; content: string };
export type WorkerOutputLine = { timestamp: string; level: string; message: string };
export type WorkerKVKey = { key: string; size: number };
export type ObjectStorageBucket = { id: string; name: string; created_at: string };
export type ObjectStorageObject = {
  key: string;
  size: number;
  etag?: string;
  httpEtag?: string;
  uploaded: string;
  httpMetadata?: { contentType?: string };
};

export type WorkerTraffic = {
  available: boolean;
  requests_per_second: number;
  p95_latency: number;
  error_rate: number;
  invocations: number;
  errors: number;
  bundle_size: number;
  traffic: number[];
  duration_ms_avg: number;
  duration_ms_p95: number;
  duration_ms_per_second: number;
  duration_series: number[];
  status_codes: { code: string; value: number }[];
};

export type KVNamespaceMetrics = { available: boolean; reads: number; writes: number };
export type ObjectStorageBucketMetrics = { available: boolean; reads: number; writes: number; size: number };

export type KVNamespace = { id: string; name: string; created_at: string };
export type Organization = { id: string; name: string; created_at: string };
export type ControlUser = { id: string; email: string; created_at: string };
export type AuthSession = {
  token: string;
  user: ControlUser;
  organizations: Organization[];
  active_org_id?: string;
};
export type KVNamespaceOption = { id: string; label: string };
export type ObjectStorageBucketOption = { id: string; label: string };

export type WorkspaceContextValue = {
  workers: Worker[];
  setWorkers: Dispatch<SetStateAction<Worker[]>>;
  namespaces: KVNamespace[];
  setNamespaces: Dispatch<SetStateAction<KVNamespace[]>>;
  objectStorageBuckets: ObjectStorageBucket[];
  setObjectStorageBuckets: Dispatch<SetStateAction<ObjectStorageBucket[]>>;
  apiConnected: boolean;
  activeOrgID: string;
  organizations: Organization[];
  setActiveOrgID: (orgID: string) => void;
  logout: () => void;
  workerDialogOpen: boolean;
  namespaceDialogOpen: boolean;
  objectStorageBucketDialogOpen: boolean;
  openWorkerDialog: () => void;
  closeWorkerDialog: () => void;
  openNamespaceDialog: () => void;
  closeNamespaceDialog: () => void;
  openObjectStorageBucketDialog: () => void;
  closeObjectStorageBucketDialog: () => void;
  toast: string;
  notify: (message: string) => void;
};
