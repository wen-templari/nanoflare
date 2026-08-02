import { createContext, useContext, useState } from "react";

import type {
  Database,
  KVNamespace,
  ObjectStorageBucket,
  Worker,
  WorkspaceContextValue,
} from "./types";

import { useAuth } from "./auth-context";
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
  const [apiConnected, setApiConnected] = useState(true);

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
        apiConnected,
        setAPIConnected: setApiConnected,
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

export function useWorkspace() {
  const context = useContext(WorkspaceContext);
  if (!context) throw new Error("useWorkspace must be used inside WorkspaceProvider");
  return context;
}
