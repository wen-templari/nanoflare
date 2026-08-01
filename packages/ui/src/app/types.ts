import type { Dispatch, SetStateAction } from "react";
import type { components } from "@nanoflare/schema";

type Normalized<T, Keys extends keyof T> = Omit<T, Keys> & {
  [Key in Keys]-?: NonNullable<T[Key]>;
};

export type Section = "overview" | "workers" | "kv" | "databases" | "object-storage";
export type WorkerAuth = Omit<components["schemas"]["AuthConfig"], "protected_routes"> & {
  protected_routes?: string[];
};
export type WorkerKVNamespaceBinding = components["schemas"]["KVBinding"];
export type WorkerDatabaseBinding = components["schemas"]["DatabaseBinding"];
export type WorkerObjectStorageBucketBinding = components["schemas"]["ObjectStorageBucketBinding"];
export type WorkerTriggerConfig = components["schemas"]["TriggerConfig"];
export type WorkerBinding = components["schemas"]["Binding"];

export type Worker = Omit<components["schemas"]["App"], "auth"> & {
  auth?: WorkerAuth;
  status?: "live" | "draft";
  requests?: string;
  deployment?: string;
  bindings?: WorkerBinding[];
};

export type WorkerDetailTab =
  | "overview"
  | "metrics"
  | "deployments"
  | "files"
  | "output"
  | "settings";

export type WorkerDeployment = Normalized<
  components["schemas"]["WorkerDeployment"],
  "bindings" | "compatibility_flags" | "kv_namespaces" | "db" | "object_storage_buckets"
> & {
  bindings?: WorkerBinding[];
  kv_namespaces?: WorkerKVNamespaceBinding[];
  db?: WorkerDatabaseBinding[];
  object_storage_buckets?: WorkerObjectStorageBucketBinding[];
};

export type WorkerSecret = components["schemas"]["Secret"];
export type WorkerDetailData = {
  app: Worker;
  deployment?: WorkerDeployment;
  secrets?: WorkerSecret[];
};

export type ConsoleDeployment = Omit<
  Normalized<components["schemas"]["ConsoleDeployment"], "compatibility_flags">,
  "triggers"
> & {
  triggers?: WorkerTriggerConfig;
};

export type WorkerFile = components["schemas"]["WorkerFile"];
export type WorkerOutputLine = components["schemas"]["WorkerOutputLine"];
export type WorkerKVKey = components["schemas"]["WorkerKVKey"];
export type ObjectStorageBucket = components["schemas"]["ObjectStorageBucket"];
export type Database = components["schemas"]["Database"];
export type ObjectStorageObject = components["schemas"]["ObjectInfo"];

export type WorkerTraffic = Normalized<
  components["schemas"]["WorkerTraffic"],
  "traffic" | "duration_series" | "status_codes"
>;

export type KVNamespaceMetrics = components["schemas"]["KVNamespaceMetrics"];
export type ObjectStorageBucketMetrics = components["schemas"]["ObjectStorageBucketMetrics"];
export type DatabaseMetrics = components["schemas"]["DatabaseMetrics"];
export type MetricPoint = components["schemas"]["MetricPoint"];
export type DatabaseMetricsTimeseries = Normalized<
  components["schemas"]["DatabaseMetricsTimeseries"],
  | "queries"
  | "read_queries"
  | "write_queries"
  | "rows_read"
  | "rows_written"
  | "storage_bytes"
  | "table_count"
  | "p50_latency_ms"
  | "p95_latency_ms"
  | "p99_latency_ms"
>;

export type KVNamespace = components["schemas"]["KVNamespace"];
export type OAuthClient = Normalized<
  components["schemas"]["OAuthClient"],
  "redirect_uris" | "scopes"
>;
export type OAuthClientCreated = Normalized<
  components["schemas"]["OAuthClientCreated"],
  "redirect_uris" | "scopes"
>;
export type OAuthConnection = Normalized<components["schemas"]["OAuthConnection"], "scopes">;
export type OAuthClientConnection = Normalized<
  components["schemas"]["OAuthClientConnection"],
  "scopes"
>;
export type PersonalAccessToken = Omit<
  Normalized<components["schemas"]["PersonalAccessToken"], "scopes">,
  "scope_type"
> & { scope_type: "user" | "org" };
export type PersonalAccessTokenCreated = Omit<
  Normalized<components["schemas"]["PersonalAccessTokenCreated"], "scopes">,
  "scope_type"
> & { scope_type: "user" | "org" };
export type Organization = Omit<components["schemas"]["Organization"], "scopes"> & {
  scopes?: string[];
};
export type ControlUser = components["schemas"]["User"];
export type AuthSession = Omit<components["schemas"]["AuthSession"], "organizations" | "user"> & {
  organizations: Organization[];
  user: ControlUser;
};
export type OrganizationMember = Normalized<
  components["schemas"]["OrganizationMembership"],
  "scopes"
> & {
  user_email: string;
};
export type OrganizationInvite = Normalized<components["schemas"]["OrganizationInvite"], "scopes">;
export type OrganizationInviteCreated = Normalized<
  components["schemas"]["InviteCreated"],
  "scopes"
>;
export type KVNamespaceOption = { id: string; label: string };
export type ObjectStorageBucketOption = { id: string; label: string };
export type DatabaseOption = { id: string; label: string };

export type WorkspaceContextValue = {
  workers: Worker[];
  setWorkers: Dispatch<SetStateAction<Worker[]>>;
  namespaces: KVNamespace[];
  setNamespaces: Dispatch<SetStateAction<KVNamespace[]>>;
  databases: Database[];
  setDatabases: Dispatch<SetStateAction<Database[]>>;
  objectStorageBuckets: ObjectStorageBucket[];
  setObjectStorageBuckets: Dispatch<SetStateAction<ObjectStorageBucket[]>>;
  workspaceReady: boolean;
  apiConnected: boolean;
  activeOrgID: string;
  organizations: Organization[];
  setActiveOrgID: (orgID: string) => void;
  createOrganization: (name: string) => Promise<void>;
  logout: () => void;
  workerDialogOpen: boolean;
  namespaceDialogOpen: boolean;
  databaseDialogOpen: boolean;
  objectStorageBucketDialogOpen: boolean;
  openWorkerDialog: () => void;
  closeWorkerDialog: () => void;
  openNamespaceDialog: () => void;
  closeNamespaceDialog: () => void;
  openDatabaseDialog: () => void;
  closeDatabaseDialog: () => void;
  openObjectStorageBucketDialog: () => void;
  closeObjectStorageBucketDialog: () => void;
  notify: (message: string) => void;
};
