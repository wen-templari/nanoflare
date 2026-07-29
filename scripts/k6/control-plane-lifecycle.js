import http from "k6/http";
import { check, sleep } from "k6";
import exec from "k6/execution";

// Keep control-plane lifecycle traffic separate from steady Worker capacity tests.
// Namespace writes are deleted within the same iteration so repeated runs do not
// accumulate resources. Deployment writes are deliberately opt-in because they
// change the active deployment and cannot be safely cleaned up through the API.
const baseURL = (__ENV.CONTROL_BASE_URL || "http://127.0.0.1:8080").replace(/\/$/, "");
const apiToken = __ENV.API_TOKEN || "";
const orgID = __ENV.ORG_ID || "";
const workerID = __ENV.WORKER_ID || "";
const profile = __ENV.PROFILE || "smoke";
const thinkTime = Number(__ENV.THINK_TIME || "0");
const allowDeploys = __ENV.ALLOW_DEPLOYS === "1";

function stages() {
  if (profile === "sustained") return [{ duration: __ENV.DURATION || "10m", target: Number(__ENV.VUS || "10") }];
  return [{ duration: "30s", target: 1 }];
}

export const options = {
  stages: stages(),
  thresholds: { http_req_failed: ["rate<0.01"], http_req_duration: ["p(95)<500", "p(99)<1000"] },
};

function headers(extra = {}) {
  const result = { ...extra };
  if (apiToken) result.Authorization = `Bearer ${apiToken}`;
  if (orgID) result["X-Nanoflare-Org-ID"] = orgID;
  return result;
}

function request(method, path, body, tag, expected = [200]) {
  const response = http.request(method, `${baseURL}${path}`, body, {
    headers: headers(body ? { "Content-Type": "application/json" } : {}),
    tags: { endpoint: tag },
  });
  check(response, { "status is expected": (r) => expected.includes(r.status) });
  return response;
}

function lifecycleNamespace() {
  const name = `k6-lifecycle-${exec.vu.idInTest}-${exec.scenario.iterationInTest}`;
  const created = request("POST", "/v1/kv/namespaces", JSON.stringify({ name }), "namespace_create", [201]);
  if (created.status !== 201) return;
  const namespace = created.json();
  check(namespace, { "namespace id is present": (item) => item && item.id });
  if (namespace && namespace.id) request("DELETE", `/v1/kv/namespaces/${namespace.id}`, null, "namespace_delete", [204]);
}

function deploy() {
  if (!workerID) throw new Error("WORKER_ID is required when ALLOW_DEPLOYS=1");
  request("POST", `/v1/workers/${workerID}/deployments`, JSON.stringify({
    entrypoint: "worker.js", format: "modules", compatibility_date: "2025-12-10",
    files: [{ path: "worker.js", content: 'export default { fetch() { return new Response("k6 lifecycle deploy"); } };' }],
  }), "deployment_create", [201]);
}

export function setup() {
  if (!apiToken || !orgID) throw new Error("API_TOKEN and ORG_ID are required");
}

export default function () {
  const slot = exec.scenario.iterationInTest % (allowDeploys ? 10 : 5);
  if (allowDeploys && slot === 0) deploy();
  else if (slot < 3) request("GET", "/v1/workers", null, "workers_list");
  else if (slot === 3) request("GET", "/v1/kv/namespaces", null, "namespaces_list");
  else lifecycleNamespace();
  if (thinkTime > 0) sleep(thinkTime);
}
