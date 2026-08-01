import { createContext, useContext, useEffect, useState } from "react";
import { useAuth } from "./auth-context";
import { apiClient } from "./api";
import type {
  Database,
  KVNamespace,
  ObjectStorageBucket,
  Worker,
  WorkspaceContextValue,
} from "./types";
import { sortDatabases, sortNamespaces, sortObjectStorageBuckets } from "./utils";
import { appToastManager } from "./toast";

const WorkspaceContext = createContext<WorkspaceContextValue | null>(null);

export function WorkspaceProvider({ children }: { children: React.ReactNode }) {
  const auth = useAuth();
  const [workers, setWorkers] = useState<Worker[]>([]);
  const [namespaces, setNamespaces] = useState<KVNamespace[]>([]);
  const [databases, setDatabases] = useState<Database[]>([]);
  const [objectStorageBuckets, setObjectStorageBuckets] = useState<ObjectStorageBucket[]>([]);
  const [workerDialogOpen, setWorkerDialogOpen] = useState(false);
  const [namespaceDialogOpen, setNamespaceDialogOpen] = useState(false);
  const [databaseDialogOpen, setDatabaseDialogOpen] = useState(false);
  const [objectStorageBucketDialogOpen, setObjectStorageBucketDialogOpen] = useState(false);
  const [workspaceReady, setWorkspaceReady] = useState(false);
  const [apiConnected, setApiConnected] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setWorkspaceReady(false);

    async function refreshWorkspace() {
      try {
        if (!auth.activeOrgID) {
          if (!cancelled) setWorkspaceReady(true);
          return;
        }
        const [apps, kvNamespaces, dbs, buckets] = await Promise.all([
          apiClient.GET("/v1/workers", { params: { header: { "X-Nanoflare-Org-ID": auth.activeOrgID } } }),
          apiClient.GET("/v1/kv/namespaces", { params: { header: { "X-Nanoflare-Org-ID": auth.activeOrgID } } }),
          apiClient.GET("/v1/db", { params: { header: { "X-Nanoflare-Org-ID": auth.activeOrgID } } }),
          apiClient.GET("/v1/object-storage-buckets", { params: { header: { "X-Nanoflare-Org-ID": auth.activeOrgID } } }),
        ]);
        if (apps.error || kvNamespaces.error || dbs.error || buckets.error) {
          throw new Error("Could not load workspace");
        }
        if (cancelled) return;
        setApiConnected(true);
        const nextWorkers = await Promise.all(
          (apps.data ?? []).map(async (app) => {
            const [detail, traffic] = await Promise.all([
              apiClient.GET("/v1/workers/{workerID}", { params: { header: { "X-Nanoflare-Org-ID": auth.activeOrgID }, path: { workerID: app.id } } }),
              apiClient.GET("/v1/workers/{workerID}/traffic", { params: { header: { "X-Nanoflare-Org-ID": auth.activeOrgID }, path: { workerID: app.id } } }),
            ]);

            return {
              ...app,
              status: detail.data?.deployment ? ("live" as const) : ("draft" as const),
              requests: traffic.data?.available ? formatCount(traffic.data.invocations) : "unavailable",
              deployment: detail.data?.deployment?.id ?? "awaiting deploy",
              bindings: detail.data?.deployment?.bindings ?? [],
            };
          }),
        );
        if (cancelled) return;
        setWorkers(nextWorkers as Worker[]);
        setNamespaces(sortNamespaces(kvNamespaces.data ?? []));
        setDatabases(sortDatabases(dbs.data ?? []));
        setObjectStorageBuckets(sortObjectStorageBuckets(buckets.data ?? []));
        setWorkspaceReady(true);
      } catch {
        if (cancelled) return;
        setApiConnected(false);
        setWorkers([]);
        setNamespaces([]);
        setDatabases([]);
        setObjectStorageBuckets([]);
        setWorkspaceReady(true);
      }
    }

    void refreshWorkspace();
    const interval = window.setInterval(() => void refreshWorkspace(), 15000);

    return () => {
      cancelled = true;
      window.clearInterval(interval);
    };
  }, [auth.activeOrgID]);

  function notify(message: string) {
    appToastManager.add({ title: message });
  }

  return (
    <WorkspaceContext.Provider
      value={{
        workers,
        setWorkers,
        namespaces,
        setNamespaces,
        databases,
        setDatabases,
        objectStorageBuckets,
        setObjectStorageBuckets,
        workspaceReady,
        apiConnected,
        activeOrgID: auth.activeOrgID,
        organizations: auth.organizations,
        setActiveOrgID: auth.setActiveOrgID,
        createOrganization: auth.createOrganization,
        logout: auth.logout,
        workerDialogOpen,
        namespaceDialogOpen,
        databaseDialogOpen,
        objectStorageBucketDialogOpen,
        openWorkerDialog: () => setWorkerDialogOpen(true),
        closeWorkerDialog: () => setWorkerDialogOpen(false),
        openNamespaceDialog: () => setNamespaceDialogOpen(true),
        closeNamespaceDialog: () => setNamespaceDialogOpen(false),
        openDatabaseDialog: () => setDatabaseDialogOpen(true),
        closeDatabaseDialog: () => setDatabaseDialogOpen(false),
        openObjectStorageBucketDialog: () => setObjectStorageBucketDialogOpen(true),
        closeObjectStorageBucketDialog: () => setObjectStorageBucketDialogOpen(false),
        notify,
      }}
    >
      {children}
    </WorkspaceContext.Provider>
  );
}

function formatCount(value = 0) {
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: value < 10 ? 1 : 0 }).format(
    value,
  );
}

export function useWorkspace() {
  const context = useContext(WorkspaceContext);
  if (!context) throw new Error("useWorkspace must be used inside WorkspaceProvider");
  return context;
}
