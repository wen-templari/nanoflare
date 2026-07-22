import http from "k6/http";
import { check, sleep } from "k6";
import exec from "k6/execution";

const baseURL = (__ENV.BASE_URL || "http://127.0.0.1:8080").replace(/\/$/, "");
const workerID = __ENV.WORKER_ID || "";
const workerIDs = splitList(__ENV.WORKER_IDS || workerID);
const hostname = __ENV.HOSTNAME || "";
const hostnames = splitList(__ENV.HOSTNAMES || hostname);
const scenario = __ENV.SCENARIO || "mixed";
const profile = __ENV.PROFILE || "step";
const thinkTime = Number(__ENV.THINK_TIME || "0");
const debugErrors = __ENV.DEBUG_ERRORS === "1";
const apiToken = __ENV.API_TOKEN || "";
const orgID = __ENV.ORG_ID || "";
const kvNamespaceID = __ENV.KV_NAMESPACE_ID || "";
const objectBucketID = __ENV.OBJECT_BUCKET_ID || "";
const assetPaths = splitList(__ENV.ASSET_PATHS || "/,/assets/app.js,/assets/logo.svg,/assets/image.svg");
const objectKeyPrefix = __ENV.OBJECT_KEY_PREFIX || "k6";
const objectPayload = __ENV.OBJECT_PAYLOAD || "nanoflare object load test payload";
const controlPaths = splitList(__ENV.CONTROL_PATHS || defaultControlPaths());
const controlWrites = __ENV.CONTROL_WRITES === "1";
const controlDeployWorkerID = __ENV.CONTROL_DEPLOY_WORKER_ID || workerID;
const controlDeployBody = __ENV.CONTROL_DEPLOY_BODY || defaultDeployBody();
let debugErrorCount = 0;

function splitList(value) {
  return String(value || "")
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
}

function defaultControlPaths() {
  const paths = ["/v1/workers", "/v1/kv/namespaces", "/v1/object-storage-buckets", "/metrics"];
  if (workerID) {
    paths.push(`/v1/workers/${workerID}`, `/v1/workers/${workerID}/deployments`);
  }
  if (kvNamespaceID) {
    paths.push(`/v1/kv/namespaces/${kvNamespaceID}/metrics`);
  }
  if (objectBucketID) {
    paths.push(`/v1/object-storage-buckets/${objectBucketID}/metrics`);
  }
  return paths.join(",");
}

function defaultDeployBody() {
  return JSON.stringify({
    entrypoint: "worker.js",
    format: "modules",
    compatibility_date: "2025-12-10",
    files: [
      {
        path: "worker.js",
        content: 'export default { fetch() { return new Response("k6 deploy"); } };',
      },
    ],
  });
}

function stagesForProfile() {
  if (profile === "smoke") {
    return [{ duration: "30s", target: 1 }];
  }
  if (profile === "sustained") {
    return [{ duration: __ENV.DURATION || "10m", target: Number(__ENV.VUS || "100") }];
  }
  if (profile === "spike") {
    return [
      { duration: "30s", target: 25 },
      { duration: "30s", target: 250 },
      { duration: "2m", target: 250 },
      { duration: "30s", target: 25 },
    ];
  }
  return [
    { duration: "1m", target: 10 },
    { duration: "2m", target: 50 },
    { duration: "2m", target: 100 },
    { duration: "2m", target: 250 },
    { duration: "1m", target: 0 },
  ];
}

export const options = {
  stages: stagesForProfile(),
  thresholds: {
    http_req_failed: ["rate<0.01"],
    http_req_duration: ["p(95)<500", "p(99)<1000"],
  },
};

function currentIndex(items) {
  if (items.length === 0) {
    return 0;
  }
  return exec.scenario.iterationInTest % items.length;
}

function currentWorkerID() {
  if (workerIDs.length === 0) {
    return "";
  }
  return workerIDs[currentIndex(workerIDs)];
}

function currentHostname() {
  if (hostnames.length === 0) {
    return "";
  }
  return hostnames[currentIndex(hostnames)];
}

function targetPath(path, selectedWorkerID = workerID) {
  if (selectedWorkerID) {
    return `${baseURL}/internal/http/workers/${selectedWorkerID}${path}`;
  }
  return `${baseURL}${path}`;
}

function controlPath(path) {
  return `${baseURL}${path}`;
}

function headers(extra = {}, selectedHostname = hostname) {
  const requestHeaders = { ...extra };
  if (selectedHostname) {
    requestHeaders.Host = selectedHostname;
  }
  if (apiToken) {
    requestHeaders.Authorization = `Bearer ${apiToken}`;
  }
  if (orgID) {
    requestHeaders["X-Nanoflare-Org-ID"] = orgID;
  }
  return requestHeaders;
}

function request(method, url, tagName, body = null, params = {}, expectedStatuses = [200]) {
  const requestParams = {
    tags: { endpoint: tagName },
    ...params,
    headers: headers(params.headers || {}, params.hostname || ""),
  };
  const response = http.request(method, url, body, requestParams);
  if (!expectedStatuses.includes(response.status) && debugErrors && debugErrorCount < 3) {
    debugErrorCount += 1;
    console.warn(`${tagName} returned ${response.status}: ${String(response.body).slice(0, 300)}`);
  }
  check(response, {
    "status is expected": (r) => expectedStatuses.includes(r.status),
  });
  return response;
}

function workerRequest(path, tagName, selectedWorkerID = workerID, selectedHostname = hostname) {
  const params = {
    tags: { endpoint: tagName },
    headers: headers({}, selectedHostname),
  };
  const response = http.get(targetPath(path, selectedWorkerID), params);
  if (debugErrors && response.status !== 200 && debugErrorCount < 3) {
    debugErrorCount += 1;
    console.warn(`${tagName} returned ${response.status}: ${String(response.body).slice(0, 300)}`);
  }
  check(response, {
    "status is 200": (r) => r.status === 200,
    "body is not empty": (r) => r.body && r.body.length > 0,
  });
  return response;
}

export function setup() {
  if (["kv_read", "mixed", "mixed_app", "multi_worker"].includes(scenario)) {
    const response = workerRequest("/kv-put", "kv_seed", workerIDs[0] || workerID, hostnames[0] || hostname);
    check(response, {
      "seed status is 200": (r) => r.status === 200,
    });
  }
  if (["object_read", "objects", "mixed_app", "multi_worker"].includes(scenario)) {
    putObject("k6-seed.txt", "object_seed");
  }
}

function assetPath() {
  if (assetPaths.length === 0) {
    return "/";
  }
  return assetPaths[currentIndex(assetPaths)];
}

function objectKey(name) {
  return `${objectKeyPrefix}/${name}`;
}

function iterationObjectKey() {
  return objectKey(`${exec.vu.idInTest}-${exec.scenario.iterationInTest}.txt`);
}

function objectURL(key, selectedWorkerID = workerID) {
  if (selectedWorkerID && objectBucketID) {
    return controlPath(`/v1/workers/${selectedWorkerID}/object-storage-buckets/${objectBucketID}/${encodeURIComponent(key)}`);
  }
  return targetPath(`/object/${encodeURIComponent(key)}`, selectedWorkerID);
}

function objectRequestParams(extraHeaders = {}, selectedWorkerID = workerID, selectedHostname = hostname) {
  if (selectedWorkerID && objectBucketID) {
    return { headers: extraHeaders };
  }
  return { headers: extraHeaders, hostname: selectedHostname };
}

function putObject(name, tagName, selectedWorkerID = workerID, selectedHostname = hostname) {
  const key = name.includes("/") ? name : objectKey(name);
  return request(
    "PUT",
    objectURL(key, selectedWorkerID),
    tagName,
    objectPayload,
    objectRequestParams({ "Content-Type": "text/plain" }, selectedWorkerID, selectedHostname),
    [200, 201, 204],
  );
}

function getObject(name, tagName, selectedWorkerID = workerID, selectedHostname = hostname) {
  const key = name.includes("/") ? name : objectKey(name);
  return request("GET", objectURL(key, selectedWorkerID), tagName, null, objectRequestParams({}, selectedWorkerID, selectedHostname), [200]);
}

function deleteObject(name, tagName, selectedWorkerID = workerID, selectedHostname = hostname) {
  const key = name.includes("/") ? name : objectKey(name);
  return request("DELETE", objectURL(key, selectedWorkerID), tagName, null, objectRequestParams({}, selectedWorkerID, selectedHostname), [200, 204]);
}

function listObjects() {
  if (workerID && objectBucketID) {
    return request("GET", controlPath(`/v1/workers/${workerID}/object-storage-buckets/${objectBucketID}`), "object_list");
  }
  return workerRequest("/objects", "object_list");
}

function controlRead() {
  const path = controlPaths[currentIndex(controlPaths)];
  return request("GET", controlPath(path), `control:${path}`, null, {}, [200, 204]);
}

function controlWrite() {
  const slot = exec.scenario.iterationInTest % 10;
  if (slot < 7) {
    return controlRead();
  }
  if (slot < 9) {
    return request(
      "POST",
      controlPath("/v1/kv/namespaces"),
      "control:namespace_create",
      JSON.stringify({ name: `k6-${exec.vu.idInTest}-${exec.scenario.iterationInTest}` }),
      { headers: { "Content-Type": "application/json" } },
      [200, 201],
    );
  }
  if (!controlDeployWorkerID) {
    return controlRead();
  }
  return request(
    "POST",
    controlPath(`/v1/workers/${controlDeployWorkerID}/deployments`),
    "control:deploy",
    controlDeployBody,
    { headers: { "Content-Type": "application/json" } },
    [200, 201],
  );
}

function runScenario() {
  if (scenario === "plain") {
    workerRequest("/plain", "plain");
    return;
  }
  if (scenario === "kv_read") {
    workerRequest("/kv-get", "kv_get");
    return;
  }
  if (scenario === "kv_write") {
    workerRequest("/kv-put", "kv_put");
    return;
  }
  if (scenario === "assets") {
    workerRequest(assetPath(), "asset");
    return;
  }
  if (scenario === "object_read") {
    getObject("k6-seed.txt", "object_get");
    return;
  }
  if (scenario === "object_write") {
    putObject(iterationObjectKey(), "object_put");
    return;
  }
  if (scenario === "objects") {
    const slot = exec.scenario.iterationInTest % 10;
    if (slot < 4) {
      getObject("k6-seed.txt", "object_get");
    } else if (slot < 7) {
      putObject(iterationObjectKey(), "object_put");
    } else if (slot < 9) {
      listObjects();
    } else {
      const key = iterationObjectKey();
      putObject(key, "object_put_delete_seed");
      deleteObject(key, "object_delete");
    }
    return;
  }
  if (scenario === "control_api") {
    if (controlWrites) {
      controlWrite();
    } else {
      controlRead();
    }
    return;
  }
  if (scenario === "multi_worker") {
    const selectedWorkerID = currentWorkerID();
    const selectedHostname = currentHostname();
    const slot = exec.scenario.iterationInTest % 10;
    if (slot < 5) {
      workerRequest("/plain", "plain", selectedWorkerID, selectedHostname);
    } else if (slot < 7) {
      workerRequest("/kv-get", "kv_get", selectedWorkerID, selectedHostname);
    } else if (slot < 8) {
      workerRequest("/kv-put", "kv_put", selectedWorkerID, selectedHostname);
    } else if (slot < 9) {
      workerRequest(assetPath(), "asset", selectedWorkerID, selectedHostname);
    } else {
      getObject("k6-seed.txt", "object_get", selectedWorkerID, selectedHostname);
    }
    return;
  }
  if (scenario === "mixed_app") {
    const slot = exec.scenario.iterationInTest % 20;
    if (slot < 8) {
      workerRequest("/plain", "plain");
    } else if (slot < 12) {
      workerRequest("/kv-get", "kv_get");
    } else if (slot < 14) {
      workerRequest("/kv-put", "kv_put");
    } else if (slot < 17) {
      workerRequest(assetPath(), "asset");
    } else if (slot < 19) {
      getObject("k6-seed.txt", "object_get");
    } else {
      putObject(iterationObjectKey(), "object_put");
    }
    return;
  }

  const slot = exec.scenario.iterationInTest % 10;
  if (slot < 7) {
    workerRequest("/plain", "plain");
  } else if (slot < 9) {
    workerRequest("/kv-get", "kv_get");
  } else {
    workerRequest("/kv-put", "kv_put");
  }
}

export default function () {
  runScenario();
  if (thinkTime > 0) {
    sleep(thinkTime);
  }
}
