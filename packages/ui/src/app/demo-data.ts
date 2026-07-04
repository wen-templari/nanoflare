import type { ConsoleDeployment, KVNamespace, Worker } from "./types";
import { sortNamespaces } from "./utils";

export const demoNamespaces: KVNamespace[] = sortNamespaces([
  { id: "5c8b2f5a80fe4f3699d97240b9d90f44", name: "edge-sessions", created_at: "2026-05-24T10:15:00Z" },
  { id: "89a4bfb9ec7348919d95a9b7c0d31da2", name: "billing-cache", created_at: "2026-05-26T12:40:00Z" },
  { id: "91f2d55c1f2a4a50b2512eca934f68f6", name: "ops-flags", created_at: "2026-05-29T08:05:00Z" },
]);

export const demoWorkers: Worker[] = [
  {
    id: "ec84a0260cb606a15cf3a09ea938ddd1ca3a57089320af23",
    name: "Customer portal",
    hostname: "portal.acme.internal",
    created_at: "2026-05-31T07:30:00Z",
    status: "live",
    requests: "24.8k",
    deployment: "cc01a1bab53a42c865bfe59a2296cd28419d03db91a54ac8",
    kv_bindings: [{ binding: "SESSIONS", id: demoNamespaces[0].id }],
  },
  {
    id: "dc2b346df24d2e247917fcdbdc344e12cb6b642902ed86f8",
    name: "Billing sync",
    hostname: "billing.acme.internal",
    created_at: "2026-05-29T14:20:00Z",
    status: "live",
    requests: "8.2k",
    deployment: "06b83b2017e634e152d296a447660e9567c9f317fd2e47d5",
    kv_bindings: [{ binding: "CACHE", id: demoNamespaces[1].id }],
  },
  {
    id: "ce586cdb38f47b15dc20397f081eef8738f1231f7df55f0a",
    name: "Operations dashboard",
    hostname: "ops.acme.internal",
    created_at: "2026-05-27T09:10:00Z",
    status: "draft",
    requests: "1.1k",
    deployment: "42eac2d9c9b5672eb56237745cbeef42cf8fe09107567c44",
    kv_bindings: [{ binding: "FLAGS", id: demoNamespaces[2].id }],
  },
];

export const demoDeployments: ConsoleDeployment[] = [
  { id: "cc01a1bab53a42c865bfe59a2296cd28419d03db91a54ac8", app_id: demoWorkers[0].id, app_name: demoWorkers[0].name, hostname: demoWorkers[0].hostname, entrypoint: "worker.js", bundle_size: 18432, compatibility_date: "2026-05-31", state: "active", created_at: "2026-05-31T07:30:00Z" },
  { id: "06b83b2017e634e152d296a447660e9567c9f317fd2e47d5", app_id: demoWorkers[1].id, app_name: demoWorkers[1].name, hostname: demoWorkers[1].hostname, entrypoint: "worker.js", bundle_size: 9638, compatibility_date: "2026-05-29", state: "active", created_at: "2026-05-29T14:20:00Z" },
  { id: "42eac2d9c9b5672eb56237745cbeef42cf8fe09107567c44", app_id: demoWorkers[2].id, app_name: demoWorkers[2].name, hostname: demoWorkers[2].hostname, entrypoint: "worker.js", bundle_size: 6144, compatibility_date: "2026-05-27", state: "inactive", created_at: "2026-05-27T09:10:00Z" },
];
