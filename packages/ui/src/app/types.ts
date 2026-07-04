import type { Dispatch, SetStateAction } from "react";

export type Section = "overview" | "workers" | "kv";
export type WorkerAuth = { protected_routes?: string[] };
export type WorkerKVNamespaceBinding = { binding: string; id: string; preview_id?: string };
export type WorkerAssetConfig = {
  binding?: string;
  html_handling?: string;
  not_found_handling?: string;
  run_worker_first?: true | string[];
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
  kv_bindings?: WorkerKVNamespaceBinding[];
};

export type WorkerDetailTab = "overview" | "deployments" | "files" | "output" | "settings";

export type WorkerDeployment = {
  id: string;
  entrypoint: string;
  bundle_size: number;
  asset_count?: number;
  asset_config?: WorkerAssetConfig;
  compatibility_date: string;
  created_at: string;
  kv_namespaces?: WorkerKVNamespaceBinding[];
};

export type WorkerDetailData = { app: Worker; deployment?: WorkerDeployment };

export type ConsoleDeployment = {
  id: string;
  app_id: string;
  app_name: string;
  hostname: string;
  entrypoint: string;
  bundle_size: number;
  compatibility_date: string;
  state: "active" | "inactive";
  created_at: string;
};

export type WorkerFile = { name: string; path: string; size: number; content: string };
export type WorkerOutputLine = { timestamp: string; level: string; message: string };
export type WorkerKVKey = { key: string; size: number };

export type WorkerTraffic = {
  available: boolean;
  requests_per_second: number;
  p95_latency: number;
  error_rate: number;
  traffic: number[];
  status_codes: { code: string; value: number }[];
};

export type KVNamespace = { id: string; name: string; created_at: string };
export type KVNamespaceOption = { id: string; label: string };

export type WorkspaceContextValue = {
  workers: Worker[];
  setWorkers: Dispatch<SetStateAction<Worker[]>>;
  namespaces: KVNamespace[];
  setNamespaces: Dispatch<SetStateAction<KVNamespace[]>>;
  apiConnected: boolean;
  workerDialogOpen: boolean;
  namespaceDialogOpen: boolean;
  openWorkerDialog: () => void;
  closeWorkerDialog: () => void;
  openNamespaceDialog: () => void;
  closeNamespaceDialog: () => void;
  toast: string;
  notify: (message: string) => void;
};
