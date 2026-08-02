import {
  ChartPalette,
  DropdownMenu,
  LayerCard,
  Tabs,
  Text,
  TimeseriesChart,
} from "@cloudflare/kumo";
import {
  Archive,
  BookOpen,
  ChevronRight,
  Copy,
  DatabaseZap,
  FileJson,
  FileText,
  Folder,
  FolderPlus,
  Globe2,
  HardDrive,
  MoreHorizontal,
  RefreshCw,
  Search,
  Trash2,
  Upload,
  Waypoints,
  Workflow,
} from "lucide-react";
import {
  type ChangeEvent,
  type KeyboardEvent,
  useDeferredValue,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Navigate, useNavigate, useParams } from "react-router-dom";

import type { ObjectStorageBucketMetricsTimeseries, ObjectStorageObject } from "../app/types";

import { apiClient, apiFetch, errorText, fetchJSON } from "../app/api";
import { useQueryTab } from "../app/use-query-tab";
import { useWorkspaceResources } from "../app/use-workspace-resources";
import { formatBytes, sortObjectStorageBuckets } from "../app/utils";
import { useWorkspace } from "../app/workspace-context";
import { ConfirmDeleteDialog } from "../components/kumo/confirm-delete-dialog";
import { Field, WorkerDetailEmpty } from "../components/shared/primitives";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { echarts } from "../lib/kumo-echarts";

type ObjectBrowserEntry =
  | { kind: "folder"; key: string; name: string }
  | { kind: "object"; object: ObjectStorageObject; name: string };

const objectStorageBucketDetailTabs = ["overview", "settings"] as const;
const folderMarker = ".nanoflare-folder";

function latestObjectMetric(points: { value: number }[] | null | undefined) {
  return points?.[points.length - 1]?.value ?? 0;
}

export function ObjectStorageBucketDetailPage() {
  const navigate = useNavigate();
  const { bucketId } = useParams();
  const { objectStorageBuckets } = useWorkspace();
  const resourcesReady = useWorkspaceResources(["objectStorageBuckets", "workers"], "details");
  const bucket = objectStorageBuckets.find((item) => item.id === bucketId);

  if (!resourcesReady) return null;
  if (!bucket) return <Navigate to="/object-storage" replace />;

  return (
    <ObjectStorageBucketDetailContent bucket={bucket} onBack={() => navigate("/object-storage")} />
  );
}

function ObjectStorageBucketDetailContent({
  bucket,
  onBack,
}: {
  bucket: { id: string; name: string; created_at: string };
  onBack: () => void;
}) {
  const navigate = useNavigate();
  const { workers, setObjectStorageBuckets, notify, apiConnected, activeOrgID } = useWorkspace();
  const [tab, setTab] = useQueryTab<(typeof objectStorageBucketDetailTabs)[number]>(
    objectStorageBucketDetailTabs,
    "overview",
  );
  const [name, setName] = useState(bucket.name);
  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deleteError, setDeleteError] = useState("");
  const [objects, setObjects] = useState<ObjectStorageObject[]>([]);
  const [loadingObjects, setLoadingObjects] = useState(false);
  const [status, setStatus] = useState("");
  const [searchInput, setSearchInput] = useState("");
  const [searchPrefix, setSearchPrefix] = useState("");
  const deferredSearch = useDeferredValue(searchPrefix);
  const uploadInputRef = useRef<HTMLInputElement>(null);
  const [uploading, setUploading] = useState(false);
  const [currentPrefix, setCurrentPrefix] = useState("");
  const [newFolderOpen, setNewFolderOpen] = useState(false);
  const [newFolderName, setNewFolderName] = useState("");
  const [creatingFolder, setCreatingFolder] = useState(false);
  const [metrics, setMetrics] = useState<ObjectStorageBucketMetricsTimeseries>({
    available: false,
    reads: [],
    writes: [],
    size: [],
  });
  const bindings = workers.flatMap((worker) =>
    (worker.bindings ?? [])
      .filter(
        (binding) => binding.kind === "object_storage_bucket" && binding.bucket_id === bucket.id,
      )
      .map((binding) => ({ worker, binding })),
  );
  const accessorWorkers = bindings
    .map(({ worker }) => worker)
    .filter(
      (worker, index, all) => all.findIndex((candidate) => candidate.id === worker.id) === index,
    );
  const [accessorWorkerID, setAccessorWorkerID] = useState(accessorWorkers[0]?.id ?? "");

  useEffect(() => {
    setName(bucket.name);
  }, [bucket.id, bucket.name]);

  useEffect(() => {
    setAccessorWorkerID((current) =>
      current && accessorWorkers.some((worker) => worker.id === current)
        ? current
        : (accessorWorkers[0]?.id ?? ""),
    );
  }, [accessorWorkers]);

  useEffect(() => {
    if (!apiConnected) {
      setMetrics({ available: false, reads: [], writes: [], size: [] });
      return;
    }
    let cancelled = false;
    async function loadMetrics() {
      const nextMetrics = await apiClient
        .GET("/v1/organizations/{orgID}/object-storage-buckets/{bucketID}/analytics", {
          params: { path: { orgID: activeOrgID, bucketID: bucket.id } },
        })
        .then(({ data }) => data ?? { available: false, reads: [], writes: [], size: [] });
      if (!cancelled) setMetrics(nextMetrics);
    }
    void loadMetrics();
    return () => {
      cancelled = true;
    };
  }, [activeOrgID, apiConnected, bucket.id]);

  const basePath = accessorWorkerID
    ? `/v1/organizations/${encodeURIComponent(activeOrgID)}/workers/${encodeURIComponent(accessorWorkerID)}/object-storage-buckets/${encodeURIComponent(bucket.id)}/objects`
    : "";
  const filteredObjects = objects.filter((item) =>
    item.key.toLowerCase().startsWith(deferredSearch.trim().toLowerCase()),
  );
  const browserEntries = useMemo(() => {
    const folders = new Map<string, ObjectBrowserEntry>();
    const files: ObjectBrowserEntry[] = [];

    for (const item of filteredObjects) {
      if (!item.key.startsWith(currentPrefix)) continue;
      const remainder = item.key.slice(currentPrefix.length);
      if (!remainder) continue;
      const slash = remainder.indexOf("/");
      if (slash >= 0) {
        const name = remainder.slice(0, slash);
        const key = `${currentPrefix}${name}/`;
        folders.set(key, { kind: "folder", key, name });
      } else if (remainder !== folderMarker) {
        files.push({ kind: "object", object: item, name: remainder });
      }
    }

    return [
      ...[...folders.values()].sort((a, b) => a.name.localeCompare(b.name)),
      ...files.sort((a, b) => a.name.localeCompare(b.name)),
    ];
  }, [currentPrefix, filteredObjects]);

  useEffect(() => {
    setObjects([]);
    setStatus("");
    setSearchInput("");
    setSearchPrefix("");
    setCurrentPrefix("");
    setNewFolderOpen(false);
    setNewFolderName("");
  }, [bucket.id, accessorWorkerID]);

  async function refreshObjects() {
    if (!basePath) {
      setObjects([]);
      setStatus(
        accessorWorkers.length
          ? "Choose a worker to inspect this bucket."
          : "Bind this bucket to a worker to inspect its objects.",
      );
      return;
    }
    setLoadingObjects(true);
    setStatus("");
    try {
      const nextObjects = await fetchJSON<ObjectStorageObject[]>(basePath);
      setObjects(nextObjects);
      setStatus(nextObjects.length ? "Objects refreshed" : "No objects in this bucket yet.");
    } catch (error) {
      setObjects([]);
      setStatus(error instanceof Error ? error.message : "Object list failed");
    } finally {
      setLoadingObjects(false);
    }
  }

  function applySearch() {
    setCurrentPrefix("");
    setSearchPrefix(searchInput.trim());
  }

  useEffect(() => {
    void refreshObjects();
  }, [basePath]);

  async function uploadObject(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file || !basePath) return;
    setUploading(true);
    try {
      const objectKey = `${currentPrefix}${file.name}`;
      const response = await apiFetch(`${basePath}/${encodeURIComponent(objectKey)}`, {
        method: "PUT",
        headers: file.type ? { "content-type": file.type } : undefined,
        body: file,
      });
      if (!response.ok) throw new Error(`Object upload failed (${response.status})`);
      await refreshObjects();
      notify(`${objectKey} uploaded`);
    } catch (error) {
      notify(error instanceof Error ? error.message : "Object upload failed");
    } finally {
      setUploading(false);
    }
  }

  async function createFolder() {
    const folderName = newFolderName.trim().replace(/^\/+|\/+$/g, "");
    if (!folderName || !basePath) return;
    const folderKey = `${currentPrefix}${folderName}/`;
    setCreatingFolder(true);
    try {
      const response = await apiFetch(
        `${basePath}/${encodeURIComponent(`${folderKey}${folderMarker}`)}`,
        {
          method: "PUT",
          body: "",
        },
      );
      if (!response.ok) throw new Error(`Folder creation failed (${response.status})`);
      setNewFolderName("");
      setNewFolderOpen(false);
      await refreshObjects();
      notify(`${folderKey} created`);
    } catch (error) {
      notify(error instanceof Error ? error.message : "Folder creation failed");
    } finally {
      setCreatingFolder(false);
    }
  }

  async function downloadObject(key: string) {
    if (!basePath) return;
    try {
      const response = await apiFetch(`${basePath}/${encodeURIComponent(key)}`);
      if (!response.ok) throw new Error(`Object download failed (${response.status})`);
      const blob = await response.blob();
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = key.split("/").pop() || key;
      link.click();
      URL.revokeObjectURL(url);
    } catch (error) {
      notify(error instanceof Error ? error.message : "Object download failed");
    }
  }

  async function deleteObject(key: string) {
    if (!basePath || !window.confirm(`Delete object "${key}" from ${bucket.name}?`)) return;
    try {
      const response = await apiFetch(`${basePath}/${encodeURIComponent(key)}`, {
        method: "DELETE",
      });
      if (!response.ok) throw new Error(`Object delete failed (${response.status})`);
      await refreshObjects();
      notify(`${key} deleted`);
    } catch (error) {
      notify(error instanceof Error ? error.message : "Object delete failed");
    }
  }

  async function saveBucket() {
    const trimmed = name.trim();
    if (!trimmed) return notify("Bucket name is required");
    setSaving(true);
    try {
      let nextBucket = { ...bucket, name: trimmed };
      if (apiConnected) {
        const response = await apiFetch(
          `/v1/organizations/${encodeURIComponent(activeOrgID)}/object-storage-buckets/${encodeURIComponent(bucket.id)}`,
          {
            method: "PATCH",
            headers: { "content-type": "application/json" },
            body: JSON.stringify({ name: trimmed }),
          },
        );
        if (!response.ok) throw new Error(`Bucket update failed (${response.status})`);
        nextBucket = await response.json();
      }
      setObjectStorageBuckets((current) =>
        sortObjectStorageBuckets(
          current.map((item) => (item.id === bucket.id ? nextBucket : item)),
        ),
      );
      notify(`${trimmed} updated`);
    } catch (error) {
      notify(error instanceof Error ? error.message : "Bucket update failed");
    } finally {
      setSaving(false);
    }
  }

  async function deleteBucket() {
    setDeleting(true);
    setDeleteError("");
    try {
      if (apiConnected) {
        const response = await apiFetch(
          `/v1/organizations/${encodeURIComponent(activeOrgID)}/object-storage-buckets/${encodeURIComponent(bucket.id)}`,
          { method: "DELETE" },
        );
        if (!response.ok)
          throw new Error(await errorText(response, `Bucket delete failed (${response.status})`));
      }
      setObjectStorageBuckets((current) => current.filter((item) => item.id !== bucket.id));
      notify(`${bucket.name} deleted`);
      onBack();
    } catch (error) {
      setDeleteError(error instanceof Error ? error.message : "Bucket delete failed");
    } finally {
      setDeleting(false);
    }
  }

  const cards = [
    {
      label: "Reads",
      value: compactNumber(latestObjectMetric(metrics.reads)),
      note: metrics.available ? "runtime object reads" : "metrics unavailable",
      icon: BookOpen,
    },
    {
      label: "Writes",
      value: compactNumber(latestObjectMetric(metrics.writes)),
      note: metrics.available ? "runtime object writes" : "metrics unavailable",
      icon: Workflow,
    },
    {
      label: "Size",
      value: formatBytes(latestObjectMetric(metrics.size)),
      note: metrics.available ? "stored object bytes" : "metrics unavailable",
      icon: HardDrive,
    },
    {
      label: "Bindings",
      value: String(bindings.length),
      note: "active bucket references",
      icon: Waypoints,
    },
    {
      label: "Workers",
      value: String(accessorWorkers.length),
      note: "workers with live access",
      icon: Globe2,
    },
    {
      label: "Objects",
      value: String(objects.length),
      note: "objects currently listed",
      icon: DatabaseZap,
    },
  ];

  return (
    <>
      <div className="mb-6">
        <Tabs
          className="inline-flex max-w-full"
          listClassName="max-w-full"
          tabs={[
            { label: "Overview", value: "overview" },
            { label: "Settings", value: "settings" },
          ]}
          onValueChange={(value) => setTab(value as "overview" | "settings")}
          value={tab}
        />
      </div>

      {tab === "overview" && (
        <div className="space-y-6">
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
            {cards.map(({ label, value, note, icon: Icon }, index) => (
              <div
                key={label}
                style={{ animationDelay: `${index * 60}ms` }}
                className="rounded-lg border border-gray-200 bg-white p-4"
              >
                <div className="flex items-center justify-between">
                  <p className="font-mono text-[9px] text-gray-500">{label}</p>
                  <Icon className="size-3.5 text-blue-600" />
                </div>
                <p className="mt-3 text-3xl font-semibold">{value}</p>
                <p className="mt-1 font-mono text-[9px] text-gray-500">{note}</p>
              </div>
            ))}
          </div>
          <LayerCard>
            <LayerCard.Secondary>
              <Text as="h2" variant="secondary">
                Bucket activity
              </Text>
            </LayerCard.Secondary>
            <LayerCard.Primary className="px-5 py-4">
              <ObjectMetricChart
                reads={metrics.reads}
                writes={metrics.writes}
                size={metrics.size}
              />
            </LayerCard.Primary>
          </LayerCard>
          <section className="space-y-6">
            <div className="">
              <div className="border-b border-kumo-line py-3">
                <div className="flex flex-col gap-3 lg:flex-row lg:items-end lg:justify-between">
                  <label className="block min-w-0 lg:w-[420px]">
                    <span className="mb-1.5 block text-sm text-gray-700">
                      Search objects by prefix
                    </span>
                    <div className="flex items-center gap-2">
                      <div className="flex min-w-0 flex-1 items-center gap-2 rounded-lg border border-gray-300 bg-white px-3">
                        <Search className="size-4 shrink-0 text-gray-500" />
                        <Input
                          value={searchInput}
                          onChange={(event) => setSearchInput(event.target.value)}
                          onKeyDown={(event: KeyboardEvent<HTMLInputElement>) => {
                            if (event.key === "Enter") {
                              event.preventDefault();
                              applySearch();
                            }
                          }}
                          placeholder="e.g. uploads/2026/"
                          variant="unstyled"
                          className="min-w-0 flex-1"
                          inputClassName="h-10 bg-transparent p-0"
                        />
                      </div>
                      <Button type="button" variant="outline" onClick={applySearch}>
                        <Search className="size-4" />
                        Search
                      </Button>
                    </div>
                  </label>
                </div>
              </div>

              {!accessorWorkerID ? (
                <WorkerDetailEmpty
                  icon={<DatabaseZap />}
                  title="No worker access path"
                  copy="Bind this bucket to a worker to browse objects through the runtime API."
                />
              ) : (
                <>
                  <div className="flex flex-wrap items-center justify-between gap-3 py-4">
                    <div className="flex min-w-0 items-center gap-1 overflow-x-auto text-sm">
                      <button
                        type="button"
                        className="shrink-0 text-blue-700 hover:underline"
                        onClick={() => setCurrentPrefix("")}
                      >
                        {bucket.name}
                      </button>
                      {currentPrefix
                        .split("/")
                        .filter(Boolean)
                        .map((part, index, parts) => {
                          const prefix = `${parts.slice(0, index + 1).join("/")}/`;
                          const isCurrentFolder = index === parts.length - 1;
                          return (
                            <span key={prefix} className="flex shrink-0 items-center gap-1">
                              <ChevronRight className="size-4 text-gray-400" />
                              {isCurrentFolder ? (
                                <span className="text-gray-700">{part}</span>
                              ) : (
                                <button
                                  type="button"
                                  className="text-blue-700 hover:underline"
                                  onClick={() => setCurrentPrefix(prefix)}
                                >
                                  {part}
                                </button>
                              )}
                            </span>
                          );
                        })}
                      <button
                        type="button"
                        className="ml-1 shrink-0 text-gray-500 hover:text-gray-950"
                        aria-label="Copy current path"
                        onClick={() =>
                          void navigator.clipboard?.writeText(`${bucket.name}/${currentPrefix}`)
                        }
                      >
                        <Copy className="size-4" />
                      </button>
                    </div>
                    <div className="flex items-center gap-2">
                      <Button
                        type="button"
                        variant="outline"
                        disabled={uploading}
                        onClick={() => uploadInputRef.current?.click()}
                      >
                        <Upload className="size-4" />
                        {uploading ? "Uploading..." : "Upload"}
                      </Button>
                      <input
                        ref={uploadInputRef}
                        type="file"
                        className="hidden"
                        onChange={uploadObject}
                      />
                      <Button type="button" onClick={() => setNewFolderOpen((open) => !open)}>
                        <FolderPlus className="size-4" />
                        Add folder
                      </Button>
                      <Button
                        type="button"
                        variant="outline"
                        size="icon"
                        aria-label="Refresh objects"
                        onClick={() => void refreshObjects()}
                        disabled={loadingObjects}
                      >
                        <RefreshCw className={loadingObjects ? "size-4 animate-spin" : "size-4"} />
                      </Button>
                    </div>
                  </div>

                  {newFolderOpen && (
                    <form
                      className="flex flex-wrap items-end gap-3 border-b border-gray-200 bg-gray-50 px-5 py-3"
                      onSubmit={(event) => {
                        event.preventDefault();
                        void createFolder();
                      }}
                    >
                      <label className="min-w-[240px] flex-1">
                        <span className="mb-1.5 block text-sm text-gray-700">Folder name</span>
                        <Input
                          value={newFolderName}
                          onChange={(event) => setNewFolderName(event.target.value)}
                          placeholder="e.g. incoming"
                          autoFocus
                        />
                      </label>
                      <Button type="submit" disabled={creatingFolder || !newFolderName.trim()}>
                        {creatingFolder ? "Creating..." : "Create folder"}
                      </Button>
                    </form>
                  )}

                  <LayerCard>
                    <div className="overflow-x-auto">
                      <table className="w-full min-w-[760px] text-left">
                        <thead className="border-b border-gray-200 bg-gray-50 text-sm text-gray-600">
                          <tr>
                            <th className="px-5 py-3 font-medium">Object</th>
                            <th className="py-3 font-medium">Type</th>
                            <th className="py-3 font-medium">Size</th>
                            <th className="py-3 font-medium">Modified</th>
                            <th className="w-16 px-5 py-3">
                              <span className="sr-only">Actions</span>
                            </th>
                          </tr>
                        </thead>
                        <tbody>
                          {currentPrefix && (
                            <tr className="border-b border-gray-200">
                              <td colSpan={5}>
                                <button
                                  type="button"
                                  className="flex w-full items-center gap-2 px-5 py-3 text-sm text-blue-700 hover:bg-blue-50"
                                  onClick={() =>
                                    setCurrentPrefix(
                                      currentPrefix
                                        .split("/")
                                        .filter(Boolean)
                                        .slice(0, -1)
                                        .join("/")
                                        ? `${currentPrefix.split("/").filter(Boolean).slice(0, -1).join("/")}/`
                                        : "",
                                    )
                                  }
                                >
                                  <ChevronRight className="size-4 rotate-180" />
                                  Back to parent folder
                                </button>
                              </td>
                            </tr>
                          )}
                          {browserEntries.map((entry) =>
                            entry.kind === "folder" ? (
                              <tr
                                key={entry.key}
                                className="cursor-pointer border-b border-gray-200 last:border-0 hover:bg-blue-50"
                                onClick={() => {
                                  setCurrentPrefix(entry.key);
                                }}
                              >
                                <td className="px-5 py-3">
                                  <div className="flex items-center gap-2 text-sm font-medium text-blue-700">
                                    <Folder className="size-4" />
                                    {entry.name}/
                                  </div>
                                </td>
                                <td className="py-3 text-sm text-gray-600">Folder</td>
                                <td className="py-3 text-sm text-gray-500">—</td>
                                <td className="py-3 text-sm text-gray-500">—</td>
                                <td className="px-5 py-3">
                                  <DropdownMenu>
                                    <DropdownMenu.Trigger>
                                      <button
                                        type="button"
                                        aria-label={`Actions for ${entry.name}`}
                                        className="inline-grid size-8 place-items-center rounded-md text-gray-500 hover:bg-gray-100 hover:text-gray-950"
                                        onClick={(event) => event.stopPropagation()}
                                      >
                                        <MoreHorizontal className="size-4" />
                                      </button>
                                    </DropdownMenu.Trigger>
                                    <DropdownMenu.Content align="end">
                                      <DropdownMenu.Item
                                        onClick={() => setCurrentPrefix(entry.key)}
                                      >
                                        Open folder
                                      </DropdownMenu.Item>
                                      <DropdownMenu.Item
                                        onClick={() =>
                                          void navigator.clipboard?.writeText(entry.key)
                                        }
                                      >
                                        Copy prefix
                                      </DropdownMenu.Item>
                                    </DropdownMenu.Content>
                                  </DropdownMenu>
                                </td>
                              </tr>
                            ) : (
                              <tr
                                key={entry.object.key}
                                className="cursor-pointer border-b border-gray-200 last:border-0 hover:bg-gray-50"
                                onClick={() =>
                                  navigate(
                                    `/object-storage/${bucket.id}/objects/${encodeURIComponent(entry.object.key)}`,
                                  )
                                }
                              >
                                <td className="px-5 py-3">
                                  <div className="flex items-center gap-2 text-sm font-medium text-gray-950">
                                    {entry.object.httpMetadata?.contentType?.includes("json") ? (
                                      <FileJson className="size-4 text-amber-600" />
                                    ) : (
                                      <FileText className="size-4 text-gray-600" />
                                    )}
                                    {entry.name}
                                  </div>
                                </td>
                                <td className="py-3 text-sm text-gray-600">
                                  {objectMimeType(entry.object)}
                                </td>
                                <td className="py-3 text-sm text-gray-600">
                                  {formatBytes(entry.object.size)}
                                </td>
                                <td className="py-3 text-sm text-gray-600">
                                  {new Date(entry.object.uploaded).toLocaleString()}
                                </td>
                                <td className="px-5 py-3">
                                  <DropdownMenu>
                                    <DropdownMenu.Trigger>
                                      <button
                                        type="button"
                                        aria-label={`Actions for ${entry.name}`}
                                        className="inline-grid size-8 place-items-center rounded-md text-gray-500 hover:bg-gray-100 hover:text-gray-950"
                                        onClick={(event) => event.stopPropagation()}
                                      >
                                        <MoreHorizontal className="size-4" />
                                      </button>
                                    </DropdownMenu.Trigger>
                                    <DropdownMenu.Content align="end">
                                      <DropdownMenu.Item
                                        onClick={() =>
                                          navigate(
                                            `/object-storage/${bucket.id}/objects/${encodeURIComponent(entry.object.key)}`,
                                          )
                                        }
                                      >
                                        View details
                                      </DropdownMenu.Item>
                                      <DropdownMenu.Item
                                        onClick={() => void downloadObject(entry.object.key)}
                                      >
                                        Download
                                      </DropdownMenu.Item>
                                      <DropdownMenu.Item
                                        onClick={() => void deleteObject(entry.object.key)}
                                      >
                                        Delete
                                      </DropdownMenu.Item>
                                    </DropdownMenu.Content>
                                  </DropdownMenu>
                                </td>
                              </tr>
                            ),
                          )}
                        </tbody>
                      </table>
                    </div>
                  </LayerCard>

                  {!browserEntries.length && (
                    <div className="grid min-h-48 place-items-center px-5 text-center">
                      <div>
                        <DatabaseZap className="mx-auto size-5 text-gray-400" />
                        <p className="mt-3 text-sm font-medium text-gray-950">
                          {filteredObjects.length
                            ? "No objects in this folder"
                            : "No matching objects"}
                        </p>
                        <p className="mt-1 text-sm text-gray-600">
                          {status || "Upload a file or add a folder to get started."}
                        </p>
                      </div>
                    </div>
                  )}
                </>
              )}
            </div>
          </section>
        </div>
      )}

      {tab === "settings" && (
        <div className="space-y-6">
          <section className="overflow-hidden rounded-xl border border-gray-200 bg-white">
            <header className="border-b border-gray-200 px-5 py-4">
              <h2 className="text-sm font-extrabold">Settings</h2>
            </header>
            <div className="p-5">
              <Field label="Name">
                <Input
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  placeholder="customer-files"
                />
              </Field>
              <div className="mt-4 overflow-hidden rounded-lg border border-[#e2ddd2]">
                {[
                  ["Bucket ID", bucket.id],
                  ["Created", new Date(bucket.created_at).toLocaleString()],
                  ["Bindings", String(bindings.length)],
                  ["Workers", String(accessorWorkers.length)],
                  ["Objects", String(objects.length)],
                ].map(([label, value]) => (
                  <div
                    key={label}
                    className="grid gap-1 border-b border-gray-200 bg-white px-4 py-3 last:border-0 sm:grid-cols-[170px_1fr]"
                  >
                    <span className="font-mono text-[10px] text-gray-500">{label}</span>
                    <span className="break-all font-mono text-[11px] font-bold text-gray-700">
                      {value}
                    </span>
                  </div>
                ))}
              </div>
              <div className="mt-4 flex gap-2">
                <Button
                  onClick={() => void saveBucket()}
                  disabled={saving || deleting || !name.trim()}
                >
                  <Archive className="size-3.5" />
                  Save
                </Button>
              </div>
            </div>
          </section>

          <LayerCard>
            <LayerCard.Secondary>
              <Text as="h2" variant="heading3">
                Danger zone
              </Text>
            </LayerCard.Secondary>
            <LayerCard.Primary className="p-4">
              <div className="grid gap-4 md:grid-cols-[1fr_auto] md:items-center">
                <div>
                  <Text bold size="sm">
                    Delete bucket
                  </Text>
                  <Text DANGEROUS_className="mt-1" size="sm" variant="secondary">
                    Permanently remove this bucket and its objects. Worker bindings should be
                    updated before deleting it.
                  </Text>
                </div>
                <Button
                  variant="danger"
                  onClick={() => {
                    setDeleteError("");
                    setDeleteOpen(true);
                  }}
                  disabled={deleting || saving}
                >
                  <Trash2 className="size-3.5" />
                  Delete bucket
                </Button>
              </div>
            </LayerCard.Primary>
          </LayerCard>
          <ConfirmDeleteDialog
            confirmLabel="Delete bucket"
            description="This action cannot be undone. All objects stored in this bucket will be permanently deleted."
            errorMessage={deleteError}
            loading={deleting}
            onConfirm={deleteBucket}
            onOpenChange={setDeleteOpen}
            open={deleteOpen}
            title="Delete bucket"
          />

          <LayerCard>
            <LayerCard.Secondary>
              <Text as="h2" variant="heading3">
                Bound workers
              </Text>
            </LayerCard.Secondary>
            <LayerCard.Primary className="p-4">
              {bindings.length ? (
                <div className="space-y-3">
                  {bindings.map(({ worker, binding }) => (
                    <div
                      key={`${worker.id}-${binding.binding}`}
                      className="rounded-lg border border-gray-200 bg-white px-4 py-3"
                    >
                      <div className="flex items-center justify-between gap-3">
                        <div>
                          <p className="text-xs font-extrabold text-[#35413e]">{worker.name}</p>
                          <p className="mt-1 font-mono text-[10px] text-[#7d837d]">
                            {worker.hostname}
                          </p>
                        </div>
                        <Badge tone="green">{binding.binding}</Badge>
                      </div>
                    </div>
                  ))}
                </div>
              ) : (
                <p className="text-sm leading-6 text-[#7a8079]">
                  This bucket is not bound by any active deployment yet, so there is no worker path
                  available for object inspection.
                </p>
              )}
            </LayerCard.Primary>
          </LayerCard>
        </div>
      )}
    </>
  );
}

function ObjectMetricChart({
  reads,
  writes,
  size,
}: {
  reads: { timestamp: string; value: number }[] | null;
  writes: { timestamp: string; value: number }[] | null;
  size: { timestamp: string; value: number }[] | null;
}) {
  const data = (
    name: string,
    points: { timestamp: string; value: number }[] | null,
    color: string,
  ) => ({
    name,
    color,
    data: (points ?? []).map(
      (point) => [Date.parse(point.timestamp), point.value] as [number, number],
    ),
  });
  return (
    <TimeseriesChart
      ariaDescription="Object storage activity over the last 24 hours."
      data={[
        data("Reads", reads, ChartPalette.categorical(0)),
        data("Writes", writes, ChartPalette.categorical(1)),
        data("Stored bytes", size, ChartPalette.categorical(2)),
      ]}
      echarts={echarts}
      height={240}
    />
  );
}

function compactNumber(value: number) {
  return new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 }).format(
    value || 0,
  );
}

function objectMimeType(object: ObjectStorageObject) {
  if (object.httpMetadata?.contentType) return object.httpMetadata.contentType;
  const extension = object.key.split(".").pop()?.toLowerCase();
  return (
    {
      avif: "image/avif",
      gif: "image/gif",
      jpeg: "image/jpeg",
      jpg: "image/jpeg",
      json: "application/json",
      pdf: "application/pdf",
      png: "image/png",
      svg: "image/svg+xml",
      txt: "text/plain",
      webp: "image/webp",
    }[extension ?? ""] ?? "application/octet-stream"
  );
}
