import { createContext, useContext, useEffect, useState } from "react";
import { fetchJSON } from "./api";
import { demoNamespaces, demoWorkers } from "./demo-data";
import type { KVNamespace, Worker, WorkerDetailData, WorkerTraffic, WorkspaceContextValue } from "./types";
import { sortNamespaces } from "./utils";

const WorkspaceContext = createContext<WorkspaceContextValue | null>(null);

export function WorkspaceProvider({ children }: { children: React.ReactNode }) {
  const [workers, setWorkers] = useState<Worker[]>(demoWorkers);
  const [namespaces, setNamespaces] = useState<KVNamespace[]>(demoNamespaces);
  const [workerDialogOpen, setWorkerDialogOpen] = useState(false);
  const [namespaceDialogOpen, setNamespaceDialogOpen] = useState(false);
  const [toast, setToast] = useState("");
  const [apiConnected, setApiConnected] = useState(false);

  useEffect(() => {
    let cancelled = false;

    async function refreshWorkspace() {
      try {
        const [apps, kvNamespaces] = await Promise.all([
          fetchJSON<Worker[]>("/v1/apps"),
          fetchJSON<KVNamespace[]>("/v1/kv/namespaces"),
        ]);
        if (cancelled) return;
        setApiConnected(true);
        const nextWorkers = await Promise.all(apps.map(async (app) => {
          const [detail, traffic] = await Promise.all([
            fetchJSON<WorkerDetailData>(`/v1/apps/${app.id}`).catch(() => undefined),
            fetchJSON<WorkerTraffic>(`/v1/apps/${app.id}/traffic`).catch(() => undefined),
          ]);

          return {
            ...app,
            status: detail?.deployment ? "live" as const : "draft" as const,
            requests: traffic?.available ? `${traffic.requests_per_second.toFixed(2)}/s` : "unavailable",
            deployment: detail?.deployment?.id ?? "awaiting deploy",
            kv_bindings: detail?.deployment?.kv_namespaces ?? [],
          };
        }));
        if (cancelled) return;
        setWorkers(nextWorkers);
        setNamespaces(sortNamespaces(kvNamespaces));
      } catch {
        if (cancelled) return;
        setApiConnected(false);
        setWorkers(demoWorkers);
        setNamespaces(demoNamespaces);
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
        apiConnected,
        workerDialogOpen,
        namespaceDialogOpen,
        openWorkerDialog: () => setWorkerDialogOpen(true),
        closeWorkerDialog: () => setWorkerDialogOpen(false),
        openNamespaceDialog: () => setNamespaceDialogOpen(true),
        closeNamespaceDialog: () => setNamespaceDialogOpen(false),
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
