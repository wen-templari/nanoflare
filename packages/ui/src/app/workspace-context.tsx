import { createContext, useContext, useEffect, useState } from "react";
import { fetchJSON } from "./api";
import type { KVNamespace, ObjectStorageBucket, Worker, WorkerDetailData, WorkerTraffic, WorkspaceContextValue } from "./types";
import { sortNamespaces, sortObjectStorageBuckets } from "./utils";

const WorkspaceContext = createContext<WorkspaceContextValue | null>(null);

export function WorkspaceProvider({ children }: { children: React.ReactNode }) {
  const [workers, setWorkers] = useState<Worker[]>([]);
  const [namespaces, setNamespaces] = useState<KVNamespace[]>([]);
  const [objectStorageBuckets, setObjectStorageBuckets] = useState<ObjectStorageBucket[]>([]);
  const [workerDialogOpen, setWorkerDialogOpen] = useState(false);
  const [namespaceDialogOpen, setNamespaceDialogOpen] = useState(false);
  const [objectStorageBucketDialogOpen, setObjectStorageBucketDialogOpen] = useState(false);
  const [toast, setToast] = useState("");
  const [apiConnected, setApiConnected] = useState(false);

  useEffect(() => {
    let cancelled = false;

    async function refreshWorkspace() {
      try {
        const [apps, kvNamespaces, buckets] = await Promise.all([
          fetchJSON<Worker[] | null>("/v1/apps"),
          fetchJSON<KVNamespace[] | null>("/v1/kv/namespaces"),
          fetchJSON<ObjectStorageBucket[] | null>("/v1/object-storage-buckets"),
        ]);
        if (cancelled) return;
        setApiConnected(true);
        const nextWorkers = await Promise.all((apps ?? []).map(async (app) => {
          const [detail, traffic] = await Promise.all([
            fetchJSON<WorkerDetailData>(`/v1/apps/${app.id}`).catch(() => undefined),
            fetchJSON<WorkerTraffic>(`/v1/apps/${app.id}/traffic`).catch(() => undefined),
          ]);

          return {
            ...app,
            status: detail?.deployment ? "live" as const : "draft" as const,
            requests: traffic?.available ? `${traffic.requests_per_second.toFixed(2)}/s` : "unavailable",
            deployment: detail?.deployment?.id ?? "awaiting deploy",
            bindings: detail?.deployment?.bindings ?? [],
          };
        }));
        if (cancelled) return;
        setWorkers(nextWorkers);
        setNamespaces(sortNamespaces(kvNamespaces ?? []));
        setObjectStorageBuckets(sortObjectStorageBuckets(buckets ?? []));
      } catch {
        if (cancelled) return;
        setApiConnected(false);
        setWorkers([]);
        setNamespaces([]);
        setObjectStorageBuckets([]);
      }
    }

    void refreshWorkspace();
    const interval = window.setInterval(() => void refreshWorkspace(), 15000);

    return () => {
      cancelled = true;
      window.clearInterval(interval);
    };
  }, []);

  function notify(message: string) {
    setToast(message);
    window.setTimeout(() => setToast(""), 2600);
  }

  return (
    <WorkspaceContext.Provider
      value={{
        workers,
        setWorkers,
        namespaces,
        setNamespaces,
        objectStorageBuckets,
        setObjectStorageBuckets,
        apiConnected,
        workerDialogOpen,
        namespaceDialogOpen,
        objectStorageBucketDialogOpen,
        openWorkerDialog: () => setWorkerDialogOpen(true),
        closeWorkerDialog: () => setWorkerDialogOpen(false),
        openNamespaceDialog: () => setNamespaceDialogOpen(true),
        closeNamespaceDialog: () => setNamespaceDialogOpen(false),
        openObjectStorageBucketDialog: () => setObjectStorageBucketDialogOpen(true),
        closeObjectStorageBucketDialog: () => setObjectStorageBucketDialogOpen(false),
        toast,
        notify,
      }}
    >
      {children}
    </WorkspaceContext.Provider>
  );
}

export function useWorkspace() {
  const context = useContext(WorkspaceContext);
  if (!context) throw new Error("useWorkspace must be used inside WorkspaceProvider");
  return context;
}
