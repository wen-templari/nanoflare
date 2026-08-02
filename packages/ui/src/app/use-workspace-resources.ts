import { useEffect, useState } from "react";

import type { Worker } from "./types";

import { apiClient } from "./api";
import { sortDatabases, sortNamespaces, sortObjectStorageBuckets } from "./utils";
import { useWorkspace } from "./workspace-context";

export type WorkspaceResource = "workers" | "namespaces" | "databases" | "objectStorageBuckets";
export type WorkerLoadLevel = "list" | "details" | "detailsWithAnalytics";

const inFlightLoads = new Map<string, Promise<void>>();

/** Loads only the resources required by the mounted route. */
export function useWorkspaceResources(
  resources: readonly WorkspaceResource[],
  workerLoadLevel: WorkerLoadLevel = "list",
) {
  const {
    activeOrgID,
    setWorkers,
    setNamespaces,
    setDatabases,
    setObjectStorageBuckets,
    setAPIConnected,
  } = useWorkspace();
  const [ready, setReady] = useState(false);
  const resourceKey = [...resources].sort().join(",");
  const selectedResources = resourceKey ? (resourceKey.split(",") as WorkspaceResource[]) : [];

  useEffect(() => {
    let cancelled = false;
    setReady(false);
    if (!activeOrgID) {
      setReady(true);
      return;
    }

    const key = `${activeOrgID}:${resourceKey}:${workerLoadLevel}`;
    let load = inFlightLoads.get(key);
    if (!load) {
      load = loadResources(activeOrgID, selectedResources, workerLoadLevel, {
        setWorkers,
        setNamespaces,
        setDatabases,
        setObjectStorageBuckets,
      });
      inFlightLoads.set(key, load);
      void load.finally(() => inFlightLoads.delete(key));
    }
    void load
      .then(() => setAPIConnected(true))
      .catch(() => setAPIConnected(false))
      .finally(() => {
        if (!cancelled) setReady(true);
      });

    return () => {
      cancelled = true;
    };
  }, [
    activeOrgID,
    resourceKey,
    setAPIConnected,
    setDatabases,
    setNamespaces,
    setObjectStorageBuckets,
    setWorkers,
    workerLoadLevel,
  ]);

  return ready;
}

type ResourceSetters = Pick<
  ReturnType<typeof useWorkspace>,
  "setWorkers" | "setNamespaces" | "setDatabases" | "setObjectStorageBuckets"
>;

async function loadResources(
  orgID: string,
  resources: readonly WorkspaceResource[],
  workerLoadLevel: WorkerLoadLevel,
  setters: ResourceSetters,
) {
  const loads = resources.map(async (resource) => {
    if (resource === "workers") {
      const { data, error } = await apiClient.GET("/v1/organizations/{orgID}/workers", {
        params: { path: { orgID } },
      });
      if (error) throw new Error("Could not load workers");
      const workers = await hydrateWorkers(orgID, data ?? [], workerLoadLevel);
      setters.setWorkers(workers);
      return;
    }
    if (resource === "namespaces") {
      const { data, error } = await apiClient.GET("/v1/organizations/{orgID}/kv-namespaces", {
        params: { path: { orgID } },
      });
      if (error) throw new Error("Could not load KV namespaces");
      setters.setNamespaces(sortNamespaces(data ?? []));
      return;
    }
    if (resource === "databases") {
      const { data, error } = await apiClient.GET("/v1/organizations/{orgID}/databases", {
        params: { path: { orgID } },
      });
      if (error) throw new Error("Could not load databases");
      setters.setDatabases(sortDatabases(data ?? []));
      return;
    }
    const { data, error } = await apiClient.GET(
      "/v1/organizations/{orgID}/object-storage-buckets",
      { params: { path: { orgID } }, parseAs: "json" },
    );
    if (error) throw new Error("Could not load object storage buckets");
    setters.setObjectStorageBuckets(sortObjectStorageBuckets(data ?? []));
  });
  await Promise.all(loads);
}

async function hydrateWorkers(orgID: string, workers: Worker[], level: WorkerLoadLevel) {
  if (level === "list") return workers;
  return Promise.all(
    workers.map(async (worker) => {
      const detailRequest = apiClient.GET("/v1/organizations/{orgID}/workers/{workerID}", {
        params: { path: { orgID, workerID: worker.id } },
      });
      const trafficRequest =
        level === "detailsWithAnalytics"
          ? apiClient.GET("/v1/organizations/{orgID}/workers/{workerID}/analytics", {
              params: { path: { orgID, workerID: worker.id } },
            })
          : undefined;
      const [detail, traffic] = await Promise.all([detailRequest, trafficRequest]);
      return {
        ...worker,
        status: detail.data?.deployment ? ("live" as const) : ("draft" as const),
        requests: traffic?.data?.available
          ? formatCount(traffic.data.requests?.[traffic.data.requests.length - 1]?.value ?? 0)
          : undefined,
        deployment: detail.data?.deployment?.id ?? "awaiting deploy",
        bindings: detail.data?.deployment?.bindings ?? [],
      };
    }),
  );
}

function formatCount(value = 0) {
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: value < 10 ? 1 : 0 }).format(
    value,
  );
}
