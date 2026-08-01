import type { components } from "@nanoflare/schema";
import type { Dispatch, SetStateAction } from "react";

export type Section = "overview" | "workers" | "kv" | "databases" | "object-storage";
export type WorkerAuth = components["schemas"]["AuthConfig"];
export type WorkerKVNamespaceBinding = components["schemas"]["KVBinding"];
export type WorkerDatabaseBinding = components["schemas"]["DatabaseBinding"];
export type WorkerObjectStorageBucketBinding = components["schemas"]["ObjectStorageBucketBinding"];
export type WorkerTriggerConfig = components["schemas"]["TriggerConfig"];
export type WorkerBinding = components["schemas"]["Binding"];

export type Worker = components["schemas"]["App"] & {
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

export type WorkerDeployment = components["schemas"]["WorkerDeployment"];

export type WorkerSecret = components["schemas"]["Secret"];
export type WorkerDetailData = components["schemas"]["WorkerDetail"];

export type ConsoleDeployment = components["schemas"]["ConsoleDeployment"];

export type WorkerFile = components["schemas"]["WorkerFile"];
export type WorkerOutputLine = components["schemas"]["WorkerOutputLine"];
export type WorkerKVKey = components["schemas"]["WorkerKVKey"];
export type ObjectStorageBucket = components["schemas"]["ObjectStorageBucket"];
export type Database = components["schemas"]["Database"];
export type ObjectStorageObject = components["schemas"]["ObjectInfo"];

export type WorkerTraffic = components["schemas"]["WorkerTraffic"];

export type KVNamespaceMetrics = components["schemas"]["KVNamespaceMetrics"];
export type ObjectStorageBucketMetrics = components["schemas"]["ObjectStorageBucketMetrics"];
export type DatabaseMetrics = components["schemas"]["DatabaseMetrics"];
export type MetricPoint = components["schemas"]["MetricPoint"];
export type DatabaseMetricsTimeseries = components["schemas"]["DatabaseMetricsTimeseries"];

export type KVNamespace = components["schemas"]["KVNamespace"];
export type OAuthClient = components["schemas"]["OAuthClient"];
export type OAuthClientCreated = components["schemas"]["OAuthClientCreated"];
export type OAuthConnection = components["schemas"]["OAuthConnection"];
export type OAuthClientConnection = components["schemas"]["OAuthClientConnection"];
export type PersonalAccessToken = components["schemas"]["PersonalAccessToken"];
export type PersonalAccessTokenCreated = components["schemas"]["PersonalAccessTokenCreated"];
export type Organization = Omit<components["schemas"]["Organization"], "scopes"> & {
  scopes?: string[];
};
export type ControlUser = components["schemas"]["User"];
export type AuthSession = Omit<components["schemas"]["AuthSession"], "organizations" | "user"> & {
  organizations: Organization[];
  user: ControlUser;
};
export type OrganizationMember = components["schemas"]["OrganizationMembership"];
export type OrganizationInvite = components["schemas"]["OrganizationInvite"];
export type OrganizationInviteCreated = components["schemas"]["InviteCreated"];
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
