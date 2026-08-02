import { LayerCard, Text } from "@cloudflare/kumo";
import { ArrowDownToLine, Copy, FileText, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { Navigate, useNavigate, useParams } from "react-router-dom";

import type { ObjectStorageObject } from "../app/types";

import { apiFetch, errorText } from "../app/api";
import { useWorkspaceResources } from "../app/use-workspace-resources";
import { formatBytes } from "../app/utils";
import { useWorkspace } from "../app/workspace-context";
import { WorkerDetailEmpty } from "../components/shared/primitives";
import { Button } from "../components/ui/button";

type ObjectPreview = { kind: "empty" } | { kind: "image"; url: string };

export function ObjectStorageObjectDetailPage() {
  const navigate = useNavigate();
  const { bucketId, "*": encodedKey } = useParams();
  const { objectStorageBuckets } = useWorkspace();
  const resourcesReady = useWorkspaceResources(["objectStorageBuckets", "workers"], "details");
  const bucket = objectStorageBuckets.find((item) => item.id === bucketId);
  const objectKey = decodeObjectKey(encodedKey);

  if (!resourcesReady) return null;
  if (!bucket || !objectKey) return <Navigate to="/object-storage" replace />;

  return (
    <ObjectStorageObjectDetailContent
      bucket={bucket}
      objectKey={objectKey}
      onBack={() => navigate(`/object-storage/${bucket.id}?tab=objects`)}
    />
  );
}

function ObjectStorageObjectDetailContent({
  bucket,
  objectKey,
  onBack,
}: {
  bucket: { id: string; name: string };
  objectKey: string;
  onBack: () => void;
}) {
  const { workers, activeOrgID, notify } = useWorkspace();
  const [object, setObject] = useState<ObjectStorageObject>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [deleting, setDeleting] = useState(false);
  const [preview, setPreview] = useState<ObjectPreview>({ kind: "empty" });

  const accessorWorker = workers.find((worker) =>
    worker.bindings?.some(
      (binding) => binding.kind === "object_storage_bucket" && binding.bucket_id === bucket.id,
    ),
  );
  const basePath = accessorWorker
    ? `/v1/organizations/${encodeURIComponent(activeOrgID)}/workers/${encodeURIComponent(accessorWorker.id)}/object-storage-buckets/${encodeURIComponent(bucket.id)}/objects`
    : "";

  useEffect(() => {
    if (!basePath) return;
    let cancelled = false;

    async function loadObject() {
      setLoading(true);
      setError("");
      try {
        const response = await apiFetch(`${basePath}/${encodeURIComponent(objectKey)}`, {
          method: "HEAD",
        });
        if (response.status === 404) throw new Error("Object not found");
        if (!response.ok) throw new Error(await errorText(response, "Object details failed"));
        if (cancelled) return;
        setObject({
          key: response.headers.get("x-nanoflare-object-key") ?? objectKey,
          size: Number(response.headers.get("content-length") ?? "0"),
          etag: response.headers.get("x-nanoflare-object-etag") ?? "",
          httpEtag: response.headers.get("etag") ?? "",
          uploaded: response.headers.get("x-nanoflare-object-uploaded") ?? new Date().toISOString(),
          httpMetadata: { contentType: response.headers.get("content-type") ?? "" },
        });
      } catch (nextError) {
        if (!cancelled)
          setError(nextError instanceof Error ? nextError.message : "Object details failed");
      } finally {
        if (!cancelled) setLoading(false);
      }
    }

    void loadObject();
    return () => {
      cancelled = true;
    };
  }, [basePath, objectKey]);

  useEffect(() => {
    const contentType = object?.httpMetadata?.contentType ?? "";
    if (!basePath || !contentType.startsWith("image/")) {
      setPreview({ kind: "empty" });
      return;
    }
    let cancelled = false;
    let url = "";

    async function loadPreview() {
      try {
        const response = await apiFetch(`${basePath}/${encodeURIComponent(objectKey)}`);
        if (!response.ok) throw new Error("Object preview failed");
        url = URL.createObjectURL(await response.blob());
        if (!cancelled) setPreview({ kind: "image", url });
      } catch {
        if (!cancelled) setPreview({ kind: "empty" });
      }
    }

    void loadPreview();
    return () => {
      cancelled = true;
      if (url) URL.revokeObjectURL(url);
    };
  }, [basePath, object?.httpMetadata?.contentType, objectKey]);

  async function downloadObject() {
    if (!basePath) return;
    try {
      const response = await apiFetch(`${basePath}/${encodeURIComponent(objectKey)}`);
      if (!response.ok) throw new Error(`Object download failed (${response.status})`);
      const blob = await response.blob();
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = objectKey.split("/").pop() || objectKey;
      link.click();
      URL.revokeObjectURL(url);
    } catch (nextError) {
      notify(nextError instanceof Error ? nextError.message : "Object download failed");
    }
  }

  async function deleteObject() {
    if (!basePath || !window.confirm(`Delete object "${objectKey}" from ${bucket.name}?`)) return;
    setDeleting(true);
    try {
      const response = await apiFetch(`${basePath}/${encodeURIComponent(objectKey)}`, {
        method: "DELETE",
      });
      if (!response.ok) throw new Error(await errorText(response, "Object delete failed"));
      notify(`${objectKey} deleted`);
      onBack();
    } catch (nextError) {
      notify(nextError instanceof Error ? nextError.message : "Object delete failed");
    } finally {
      setDeleting(false);
    }
  }

  return (
    <section className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="flex min-w-0 items-center gap-2">
          <FileText className="size-5 shrink-0 text-gray-600" />
          <p className="truncate text-base font-semibold text-gray-950">{objectKey}</p>
          <button
            type="button"
            className="shrink-0 text-gray-500 hover:text-gray-950"
            aria-label="Copy object key"
            onClick={() => void navigator.clipboard?.writeText(objectKey)}
          >
            <Copy className="size-4" />
          </button>
        </div>
        <div className="flex shrink-0 gap-2">
          <Button type="button" variant="outline" onClick={() => void downloadObject()}>
            <ArrowDownToLine className="size-4" />
            Download
          </Button>
          <Button
            type="button"
            variant="danger"
            disabled={deleting}
            onClick={() => void deleteObject()}
          >
            <Trash2 className="size-4" />
            Delete
          </Button>
        </div>
      </div>

      {!accessorWorker ? (
        <WorkerDetailEmpty
          icon={<FileText />}
          title="No worker access path"
          copy="Bind this bucket to a worker to inspect object details."
        />
      ) : loading ? (
        <WorkerDetailEmpty
          icon={<FileText />}
          title="Loading object"
          copy="Reading object metadata."
        />
      ) : error ? (
        <WorkerDetailEmpty icon={<FileText />} title="Object unavailable" copy={error} />
      ) : object ? (
        <LayerCard>
          <LayerCard.Primary className="space-y-8 px-5 py-4">
            <section>
              <Text as="h2" variant="heading3">
                Object details
              </Text>
              <dl className="mt-4 grid gap-5 sm:grid-cols-2 lg:grid-cols-4">
                <Detail label="Date created" value={new Date(object.uploaded).toLocaleString()} />
                <Detail
                  label="Type"
                  value={object.httpMetadata?.contentType || "application/octet-stream"}
                />
                <Detail label="Storage class" value="Standard" />
                <Detail label="Size" value={formatBytes(object.size)} />
              </dl>
            </section>
            <section className="border-t border-kumo-line pt-6">
              <Text as="h2" variant="heading3">
                Custom metadata
              </Text>
              <p className="mt-3 text-sm text-gray-600">No custom metadata set</p>
            </section>
            <section className="border-t border-kumo-line pt-6">
              <Text as="h2" variant="heading3">
                Object preview
              </Text>
              {preview.kind === "image" ? (
                <div className="mt-3 flex min-h-48 items-center justify-center rounded-lg bg-gray-50 p-4">
                  <img
                    src={preview.url}
                    alt={objectKey}
                    className="max-h-[560px] max-w-full object-contain"
                  />
                </div>
              ) : (
                <p className="mt-3 text-sm text-gray-600">No preview available</p>
              )}
            </section>
          </LayerCard.Primary>
        </LayerCard>
      ) : null}
    </section>
  );
}

function Detail({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid gap-1">
      <dt className="text-sm text-gray-600">{label}</dt>
      <dd className="break-all text-sm text-gray-950">{value}</dd>
    </div>
  );
}

function decodeObjectKey(value: string | undefined) {
  if (!value) return "";
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}
