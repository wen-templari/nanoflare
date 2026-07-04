import type { KVNamespace, ObjectStorageBucket } from "./types";

export function formatBytes(value: number) {
  if (value < 1024) return `${value} B`;
  return `${(value / 1024).toFixed(1)} KB`;
}

export function formatDuration(seconds: number) {
  return seconds < 1 ? `${Math.round(seconds * 1000)}ms` : `${seconds.toFixed(2)}s`;
}

export function sortNamespaces(namespaces: KVNamespace[]) {
  return [...namespaces].sort((a, b) => a.name.localeCompare(b.name));
}

export function sortObjectStorageBuckets(buckets: ObjectStorageBucket[]) {
  return [...buckets].sort((a, b) => a.name.localeCompare(b.name));
}
