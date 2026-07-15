import { type ChangeEvent, useDeferredValue, useEffect, useState } from "react";
import { Title } from "@mantine/core";
import { Archive, ArrowDownToLine, BookOpen, DatabaseZap, FileJson, FileText, Globe2, HardDrive, RefreshCw, Search, Trash2, Upload, Waypoints, Workflow } from "lucide-react";
import { Navigate, useNavigate, useParams } from "react-router-dom";
import { apiFetch, errorText, fetchJSON } from "../app/api";
import type { ObjectStorageBucketMetrics, ObjectStorageObject } from "../app/types";
import { formatBytes, sortObjectStorageBuckets } from "../app/utils";
import { useWorkspace } from "../app/workspace-context";
import { Field, Panel, WorkerDetailEmpty } from "../components/shared/primitives";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";

export function ObjectStorageBucketDetailPage() {
  const navigate = useNavigate();
  const { bucketId } = useParams();
  const { objectStorageBuckets } = useWorkspace();
  const bucket = objectStorageBuckets.find((item) => item.id === bucketId);

  if (!bucket) return <Navigate to="/object-storage" replace />;

  return <ObjectStorageBucketDetailContent bucket={bucket} onBack={() => navigate("/object-storage")} />;
}

function ObjectStorageBucketDetailContent({
  bucket,
  onBack,
}: {
  bucket: { id: string; name: string; created_at: string };
  onBack: () => void;
}) {
  const { workers, setObjectStorageBuckets, notify, apiConnected } = useWorkspace();
  const [name, setName] = useState(bucket.name);
  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [objects, setObjects] = useState<ObjectStorageObject[]>([]);
  const [loadingObjects, setLoadingObjects] = useState(false);
  const [status, setStatus] = useState("");
  const [search, setSearch] = useState("");
  const deferredSearch = useDeferredValue(search);
  const [selectedKey, setSelectedKey] = useState("");
  const [selectedObject, setSelectedObject] = useState<ObjectStorageObject>();
  const [preview, setPreview] = useState("");
  const [previewLoading, setPreviewLoading] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [metrics, setMetrics] = useState<ObjectStorageBucketMetrics>({ available: false, reads: 0, writes: 0, size: 0 });
  const bindings = workers.flatMap((worker) =>
    (worker.bindings ?? [])
      .filter((binding) => binding.kind === "object_storage_bucket" && binding.bucket_id === bucket.id)
      .map((binding) => ({ worker, binding })),
  );
  const accessorWorkers = bindings.map(({ worker }) => worker).filter((worker, index, all) => all.findIndex((candidate) => candidate.id === worker.id) === index);
  const [accessorWorkerID, setAccessorWorkerID] = useState(accessorWorkers[0]?.id ?? "");

  useEffect(() => {
    setName(bucket.name);
  }, [bucket.id, bucket.name]);

  useEffect(() => {
    setAccessorWorkerID((current) => current && accessorWorkers.some((worker) => worker.id === current) ? current : (accessorWorkers[0]?.id ?? ""));
  }, [accessorWorkers]);

  useEffect(() => {
    if (!apiConnected) {
      setMetrics({ available: false, reads: 0, writes: 0, size: 0 });
      return;
    }
    let cancelled = false;
    async function loadMetrics() {
      const nextMetrics = await fetchJSON<ObjectStorageBucketMetrics>(`/v1/object-storage-buckets/${encodeURIComponent(bucket.id)}/metrics`).catch(() => ({ available: false, reads: 0, writes: 0, size: 0 }));
      if (!cancelled) setMetrics(nextMetrics);
    }
    void loadMetrics();
    const interval = window.setInterval(() => void loadMetrics(), 15000);
    return () => {
      cancelled = true;
      window.clearInterval(interval);
    };
  }, [apiConnected, bucket.id]);

  const basePath = accessorWorkerID ? `/v1/apps/${accessorWorkerID}/object-storage-buckets/${encodeURIComponent(bucket.id)}` : "";
  const filteredObjects = objects.filter((item) => item.key.toLowerCase().includes(deferredSearch.trim().toLowerCase()));

  useEffect(() => {
    setObjects([]);
    setSelectedKey("");
    setSelectedObject(undefined);
    setPreview("");
    setStatus("");
  }, [bucket.id, accessorWorkerID]);

  async function refreshObjects() {
    if (!basePath) {
      setObjects([]);
      setStatus(accessorWorkers.length ? "Choose a worker to inspect this bucket." : "Bind this bucket to a worker to inspect its objects.");
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

  useEffect(() => {
    void refreshObjects();
  }, [basePath]);

  async function loadObject(key: string) {
    if (!basePath) return;
    setSelectedKey(key);
    setPreview("");
    setPreviewLoading(true);
    try {
      const response = await apiFetch(`${basePath}/${encodeURIComponent(key)}`);
      if (response.status === 404) {
        setSelectedObject(undefined);
        setStatus("Object not found");
        return;
      }
      if (!response.ok) throw new Error(`Object read failed (${response.status})`);
      const metadata = objects.find((item) => item.key === key) ?? {
        key,
        size: Number(response.headers.get("content-length") ?? "0"),
        etag: response.headers.get("x-nanoflare-object-etag") ?? "",
        httpEtag: response.headers.get("etag") ?? "",
        uploaded: response.headers.get("x-nanoflare-object-uploaded") ?? new Date().toISOString(),
        httpMetadata: { contentType: response.headers.get("content-type") ?? "" },
      };
      setSelectedObject(metadata);
      const contentType = response.headers.get("content-type") ?? "";
      if (contentType.includes("json") || contentType.startsWith("text/") || !contentType) {
        setPreview(await response.text());
      }
    } catch (error) {
      notify(error instanceof Error ? error.message : "Object read failed");
    } finally {
      setPreviewLoading(false);
    }
  }

  async function uploadObject(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file || !basePath) return;
    setUploading(true);
    try {
      const response = await apiFetch(`${basePath}/${encodeURIComponent(file.name)}`, {
        method: "PUT",
        headers: file.type ? { "content-type": file.type } : undefined,
        body: file,
      });
      if (!response.ok) throw new Error(`Object upload failed (${response.status})`);
      await refreshObjects();
      notify(`${file.name} uploaded`);
    } catch (error) {
      notify(error instanceof Error ? error.message : "Object upload failed");
    } finally {
      setUploading(false);
    }
  }

  async function downloadSelectedObject() {
    if (!basePath || !selectedKey) return;
    try {
      const response = await apiFetch(`${basePath}/${encodeURIComponent(selectedKey)}`);
      if (!response.ok) throw new Error(`Object download failed (${response.status})`);
      const blob = await response.blob();
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = selectedKey.split("/").pop() || selectedKey;
      link.click();
      URL.revokeObjectURL(url);
    } catch (error) {
      notify(error instanceof Error ? error.message : "Object download failed");
    }
  }

  async function deleteSelectedObject() {
    if (!basePath || !selectedKey) return;
    if (!window.confirm(`Delete object "${selectedKey}" from ${bucket.name}?`)) return;
    try {
      const response = await apiFetch(`${basePath}/${encodeURIComponent(selectedKey)}`, { method: "DELETE" });
      if (!response.ok) throw new Error(`Object delete failed (${response.status})`);
      await refreshObjects();
      setSelectedKey("");
      setSelectedObject(undefined);
      setPreview("");
      notify(`${selectedKey} deleted`);
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
        const response = await apiFetch(`/v1/object-storage-buckets/${encodeURIComponent(bucket.id)}`, {
          method: "PATCH",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ name: trimmed }),
        });
        if (!response.ok) throw new Error(`Bucket update failed (${response.status})`);
        nextBucket = await response.json();
      }
      setObjectStorageBuckets((current) => sortObjectStorageBuckets(current.map((item) => item.id === bucket.id ? nextBucket : item)));
      notify(`${trimmed} updated`);
    } catch (error) {
      notify(error instanceof Error ? error.message : "Bucket update failed");
    } finally {
      setSaving(false);
    }
  }

  async function deleteBucket() {
    if (!window.confirm(`Delete bucket "${bucket.name}"?`)) return;
    setDeleting(true);
    try {
      if (apiConnected) {
        const response = await apiFetch(`/v1/object-storage-buckets/${encodeURIComponent(bucket.id)}`, { method: "DELETE" });
        if (!response.ok) throw new Error(await errorText(response, `Bucket delete failed (${response.status})`));
      }
      setObjectStorageBuckets((current) => current.filter((item) => item.id !== bucket.id));
      notify(`${bucket.name} deleted`);
      onBack();
    } catch (error) {
      notify(error instanceof Error ? error.message : "Bucket delete failed");
    } finally {
      setDeleting(false);
    }
  }

  const cards = [
    { label: "Reads", value: compactNumber(metrics.reads), note: metrics.available ? "runtime object reads" : "metrics unavailable", icon: BookOpen },
    { label: "Writes", value: compactNumber(metrics.writes), note: metrics.available ? "runtime object writes" : "metrics unavailable", icon: Workflow },
    { label: "Size", value: formatBytes(metrics.size), note: metrics.available ? "stored object bytes" : "metrics unavailable", icon: HardDrive },
    { label: "Bindings", value: String(bindings.length), note: "active bucket references", icon: Waypoints },
    { label: "Workers", value: String(accessorWorkers.length), note: "workers with live access", icon: Globe2 },
    { label: "Objects", value: String(objects.length), note: "objects currently listed", icon: DatabaseZap },
  ];

  return (
    <>
      <div className="mb-10 flex flex-col justify-between gap-4 py-2 md:flex-row md:items-start">
        <div>
          <Title className="flex h-12 items-center" order={1}>{bucket.name}</Title>
          <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2 font-mono text-[10px] text-[#858b84]"><span className="flex items-center gap-1.5"><DatabaseZap className="size-3" />{bucket.id}</span></div>
        </div>
        <div className="flex gap-2">
          <Button variant="ghost" onClick={() => void deleteBucket()} disabled={deleting || saving}><Trash2 className="size-3.5" />Delete</Button>
          <Button onClick={() => void saveBucket()} disabled={saving || deleting || !name.trim()}><Archive className="size-3.5" />Save</Button>
        </div>
      </div>

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
        {cards.map(({ label, value, note, icon: Icon }, index) => <div key={label} style={{ animationDelay: `${index * 60}ms` }} className="rounded-lg border border-gray-200 bg-white p-4"><div className="flex items-center justify-between"><p className="font-mono text-[9px] text-gray-500">{label}</p><Icon className="size-3.5 text-blue-600" /></div><p className="mt-3 text-3xl font-semibold">{value}</p><p className="mt-1 font-mono text-[9px] text-gray-500">{note}</p></div>)}
      </div>

      <div className="mt-6 grid gap-6 xl:grid-cols-[1.55fr_1fr]">
        <section className="overflow-hidden rounded-xl border border-gray-200 bg-white">
          <header className="border-b border-gray-200 px-5 py-4"><h2 className="text-sm font-extrabold">Edit bucket</h2></header>
          <div className="p-5">
            <Field label="Name"><Input value={name} onChange={(event) => setName(event.target.value)} placeholder="customer-files" /></Field>
          </div>
        </section>

        <Panel title="Bound workers">
          {bindings.length ? (
            <div className="space-y-3">
              {bindings.map(({ worker, binding }) => (
                <div key={`${worker.id}-${binding.binding}`} className="rounded-lg border border-gray-200 bg-white px-4 py-3">
                  <div className="flex items-center justify-between gap-3">
                    <div>
                      <p className="text-xs font-extrabold text-[#35413e]">{worker.name}</p>
                      <p className="mt-1 font-mono text-[10px] text-[#7d837d]">{worker.hostname}</p>
                    </div>
                    <Badge tone="green">{binding.binding}</Badge>
                  </div>
                </div>
              ))}
            </div>
          ) : (
            <p className="text-sm leading-6 text-[#7a8079]">This bucket is not bound by any active deployment yet, so there is no worker path available for object inspection.</p>
          )}
        </Panel>
      </div>

      <section className="mt-6 overflow-hidden rounded-xl border border-gray-200 bg-white">
        {!accessorWorkerID ? (
          <WorkerDetailEmpty icon={<DatabaseZap />} title="No worker access path" copy="Bind this bucket to a worker to browse objects through the runtime API." />
        ) : (
          <div className="grid min-h-[540px] md:grid-cols-[280px_1fr]">
            <aside className="border-b border-gray-200 bg-gray-50 py-3 md:border-b-0 md:border-r">
              <div className="flex items-center justify-between px-4 pb-2">
                <p className="font-mono text-[9px]   text-[#a0a39c]">Objects</p>
                <Button type="button" variant="ghost" size="icon" aria-label="Refresh objects" onClick={() => void refreshObjects()} disabled={loadingObjects}><RefreshCw className={loadingObjects ? "size-3.5 animate-spin" : "size-3.5"} /></Button>
              </div>
              <div className="space-y-3 px-4 pb-3">
                <label className="flex cursor-pointer items-center justify-center gap-2 rounded-md border border-dashed border-gray-300 bg-white px-3 py-2 font-mono text-[10px] font-bold text-gray-700 hover:bg-gray-50">
                  <Upload className="size-3.5 text-[#d75a41]" />
                  {uploading ? "Uploading..." : "Upload file"}
                  <input type="file" className="hidden" onChange={uploadObject} />
                </label>
                <div className="flex items-center gap-2 rounded-md border border-gray-200 bg-white px-3">
                  <Search className="size-4 text-[#959a93]" />
                  <Input value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Search objects" variant="unstyled" className="min-w-0 flex-1" inputClassName="h-10 bg-transparent p-0" />
                </div>
              </div>
              {filteredObjects.map((item) => (
                <button key={item.key} onClick={() => void loadObject(item.key)} className={selectedKey === item.key ? "flex w-full items-center gap-2 bg-[#e5e0d6] px-4 py-2 text-left font-mono text-[10px] font-bold text-[#35413e]" : "flex w-full items-center gap-2 px-4 py-2 text-left font-mono text-[10px] text-[#848a83] transition hover:bg-white/60 hover:text-[#4c5853]"}>
                  {item.httpMetadata?.contentType?.includes("json") ? <FileJson className="size-3.5 text-[#bd7e35]" /> : <FileText className="size-3.5 text-[#668e7a]" />}
                  <span className="min-w-0 flex-1 truncate">{item.key}</span>
                  <span className="text-[9px] text-[#a0a39c]">{formatBytes(item.size)}</span>
                </button>
              ))}
              {!filteredObjects.length && <p className="px-4 py-8 text-center font-mono text-[9px]   text-[#a1a49e]">{status || "No objects yet"}</p>}
            </aside>

            <div className="p-5">
              {!selectedKey ? (
                <WorkerDetailEmpty icon={<DatabaseZap />} title="Select an object" copy="Choose an object to inspect its metadata, preview text content, or download it." />
              ) : (
                <>
                  <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
                    <div>
                      <p className="text-sm font-extrabold text-[#26332f]">{selectedKey}</p>
                      <p className="mt-1 font-mono text-[10px] text-[#8a8f88]">{selectedObject?.httpMetadata?.contentType || "application/octet-stream"}</p>
                    </div>
                    <div className="flex gap-2">
                      <Button type="button" variant="outline" onClick={() => void downloadSelectedObject()}><ArrowDownToLine className="size-3.5" />Download</Button>
                      <Button type="button" variant="ghost" onClick={() => void deleteSelectedObject()}><Trash2 className="size-3.5" />Delete</Button>
                    </div>
                  </div>
                  {selectedObject ? (
                    <div className="mb-4 overflow-hidden rounded-lg border border-[#e2ddd2]">
                      {[
                        ["Size", formatBytes(selectedObject.size)],
                        ["Uploaded", new Date(selectedObject.uploaded).toLocaleString()],
                        ["ETag", selectedObject.etag || "-"],
                      ].map(([label, value]) => (
                        <div key={label} className="grid gap-1 border-b border-[#e8e3d9] bg-white/35 px-4 py-3 last:border-0 sm:grid-cols-[170px_1fr]">
                          <span className="font-mono text-[10px]   text-[#93978f]">{label}</span>
                          <span className="font-mono text-[11px] font-bold break-all text-[#4f5a55]">{value}</span>
                        </div>
                      ))}
                    </div>
                  ) : null}
                  <div className="overflow-hidden rounded-lg border border-[#d9d3c7] bg-[#202b29]">
                    <div className="flex items-center justify-between border-b border-white/10 px-4 py-3">
                      <p className="font-mono text-[10px] text-[#b5c1bb]">Preview</p>
                      <span className="font-mono text-[9px]   text-[#778781]">{previewLoading ? "loading" : (preview ? `${preview.length} chars` : "binary-safe mode")}</span>
                    </div>
                    <pre className="min-h-80 overflow-x-auto p-4 font-mono text-[11px] leading-6 text-[#d8dfd8]">{preview || "No inline preview available for this object type."}</pre>
                  </div>
                </>
              )}
            </div>
          </div>
        )}
      </section>
    </>
  );
}

function compactNumber(value: number) {
  return new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 }).format(value || 0);
}
