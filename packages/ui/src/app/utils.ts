import type { Database, KVNamespace, ObjectStorageBucket } from "./types";

export function formatBytes(value: number) {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let nextValue = Math.max(value, 0);
  let unitIndex = 0;
  while (nextValue >= 1024 && unitIndex < units.length - 1) {
    nextValue /= 1024;
    unitIndex++;
  }
  if (unitIndex === 0) return `${nextValue} B`;
  return `${nextValue.toFixed(1)} ${units[unitIndex]}`;
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

export function sortDatabases(databases: Database[]) {
  return [...databases].sort((a, b) => a.name.localeCompare(b.name));
}
