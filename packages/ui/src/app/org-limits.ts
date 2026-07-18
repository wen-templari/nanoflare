export const usageLevelDefault = "default";
export const usageLevelPaid = "paid";

const mib = 1024 * 1024;

export type OrgLimits = {
  workers: number | null;
  kvNamespaces: number | null;
  objectStorageBuckets: number | null;
  oauthClients: number | null;
  objectStorageBytes: number | null;
  kvStorageBytes: number | null;
};

export function normalizeUsageLevel(level?: string) {
  return level === usageLevelPaid ? usageLevelPaid : usageLevelDefault;
}

export function orgLimitsForLevel(level?: string): OrgLimits {
  if (normalizeUsageLevel(level) === usageLevelPaid) {
    return {
      workers: null,
      kvNamespaces: null,
      objectStorageBuckets: null,
      oauthClients: null,
      objectStorageBytes: null,
      kvStorageBytes: null,
    };
  }

  return {
    workers: 3,
    kvNamespaces: 3,
    objectStorageBuckets: 3,
    oauthClients: 0,
    objectStorageBytes: 500 * mib,
    kvStorageBytes: 100 * mib,
  };
}

export function formatBytes(value = 0) {
  if (value >= mib) {
    return `${new Intl.NumberFormat(undefined, { maximumFractionDigits: value % mib === 0 ? 0 : 1 }).format(value / mib)} MiB`;
  }
  if (value >= 1024) {
    return `${new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 }).format(value / 1024)} KiB`;
  }
  return `${new Intl.NumberFormat().format(value)} B`;
}
