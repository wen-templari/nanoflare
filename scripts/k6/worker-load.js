import http from "k6/http";
import { check, sleep } from "k6";
import exec from "k6/execution";

// Exercise the production ingress path by default: k6 -> Traefik -> workerd.
// Set ROUTE_VIA=internal only when diagnosing the nanoflared worker gateway.
const routeVia = __ENV.ROUTE_VIA || "traefik";
const defaultBaseURL =
  routeVia === "internal" ? "http://127.0.0.1:8080" : "http://127.0.0.1:8088";
const baseURL = (__ENV.BASE_URL || defaultBaseURL).replace(/\/$/, "");
// Console object routes remain available for object-storage-specific tests.
const controlBaseURL = (
  __ENV.CONTROL_BASE_URL || "http://127.0.0.1:8080"
).replace(/\/$/, "");
const workerID = __ENV.WORKER_ID || "";
const workerIDs = splitList(__ENV.WORKER_IDS || workerID);
const hostname = __ENV.HOSTNAME || "";
const hostnames = splitList(__ENV.HOSTNAMES || hostname);
const scenario = __ENV.SCENARIO || "mixed";
const databaseCount = Number(__ENV.DATABASE_COUNT || "1");
const profile = __ENV.PROFILE || "step";
const thinkTime = Number(__ENV.THINK_TIME || "0");
const debugErrors = __ENV.DEBUG_ERRORS === "1";
const apiToken = __ENV.API_TOKEN || "";
const orgID = __ENV.ORG_ID || "";
const kvNamespaceID = __ENV.KV_NAMESPACE_ID || "";
const objectBucketID = __ENV.OBJECT_BUCKET_ID || "";
const assetPaths = splitList(
  __ENV.ASSET_PATHS || "/,/assets/app.js,/assets/logo.svg,/assets/image.svg",
);
const objectKeyPrefix = __ENV.OBJECT_KEY_PREFIX || "k6";
const objectPayload = __ENV.OBJECT_PAYLOAD || "nanoflare object load test payload";
let debugErrorCount = 0;

function isManyWorkersScenario() {
  return scenario === "multi_worker" || scenario === "many_workers";
}

function splitList(value) {
  return String(value || "")
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
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

function workerTargetCount() {
  return routeVia === "internal" ? workerIDs.length : hostnames.length;
}

function targetPath(path, selectedWorkerID = workerID) {
  if (routeVia === "internal") {
    if (!selectedWorkerID) {
      throw new Error("WORKER_ID or WORKER_IDS is required when ROUTE_VIA=internal");
    }
    return `${baseURL}/internal/http/workers/${selectedWorkerID}${path}`;
  }
  return `${baseURL}${path}`;
}

function controlPath(path) {
  return `${controlBaseURL}${path}`;
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
  if (!["traefik", "internal"].includes(routeVia)) {
    throw new Error(`ROUTE_VIA must be "traefik" or "internal", got ${routeVia}`);
  }
  if (routeVia === "traefik" && hostnames.length === 0) {
    throw new Error("HOSTNAME or HOSTNAMES is required when ROUTE_VIA=traefik");
  }
  if (isManyWorkersScenario()) {
    const targetCount = workerTargetCount();
    if (targetCount < 2) {
      throw new Error("many_workers requires at least two WORKER_IDS or HOSTNAMES targets");
    }
    if (objectBucketID && workerIDs.length > 0 && workerIDs.length !== hostnames.length) {
      throw new Error("WORKER_IDS and HOSTNAMES must contain the same number of targets");
    }
  }
  if (["kv_read", "mixed", "mixed_app"].includes(scenario) || isManyWorkersScenario()) {
    const seedCount = isManyWorkersScenario() ? workerTargetCount() : 1;
    for (let index = 0; index < seedCount; index += 1) {
      const response = workerRequest(
        "/kv-put",
        "kv_seed",
        workerIDs[index] || workerID,
        hostnames[index] || hostname,
      );
      check(response, {
        [`worker ${index + 1} seed status is 200`]: (r) => r.status === 200,
      });
    }
  }
  if (["object_read", "objects", "mixed_app"].includes(scenario) || isManyWorkersScenario()) {
    const seedCount = isManyWorkersScenario() ? workerTargetCount() : 1;
    for (let index = 0; index < seedCount; index += 1) {
      putObject(
        "k6-seed.txt",
        "object_seed",
        workerIDs[index] || workerID,
        hostnames[index] || hostname,
      );
    }
  }
  if (!["db_read", "db_write", "db_mixed", "db_multi"].includes(scenario) && scenario.startsWith("db_")) {
    throw new Error(`unknown database scenario ${scenario}`);
  }
  if (!Number.isInteger(databaseCount) || databaseCount < 1) {
    throw new Error(`DATABASE_COUNT must be a positive integer, got ${databaseCount}`);
  }
  if (["db_read", "db_write", "db_mixed", "db_multi"].includes(scenario)) {
    const count = scenario === "db_multi" ? databaseCount : 1;
    for (let index = 1; index <= count; index += 1) {
      const suffix = count === 1 ? "" : `/${index}`;
      const response = workerRequest(`/db-init${suffix}`, `db_init_${index}`);
      check(response, { [`database ${index} initialized`]: (r) => r.status === 200 });
    }
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
    return controlPath(
      `/v1/workers/${selectedWorkerID}/object-storage-buckets/${objectBucketID}/${encodeURIComponent(key)}`,
    );
  }
  return targetPath(`/object/${encodeURIComponent(key)}`, selectedWorkerID);
}

function objectRequestParams(
  extraHeaders = {},
  selectedWorkerID = workerID,
  selectedHostname = hostname,
) {
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
  return request(
    "GET",
    objectURL(key, selectedWorkerID),
    tagName,
    null,
    objectRequestParams({}, selectedWorkerID, selectedHostname),
    [200],
  );
}

function deleteObject(name, tagName, selectedWorkerID = workerID, selectedHostname = hostname) {
  const key = name.includes("/") ? name : objectKey(name);
  return request(
    "DELETE",
    objectURL(key, selectedWorkerID),
    tagName,
    null,
    objectRequestParams({}, selectedWorkerID, selectedHostname),
    [200, 204],
  );
}

function listObjects() {
  if (workerID && objectBucketID) {
    return request(
      "GET",
      controlPath(`/v1/workers/${workerID}/object-storage-buckets/${objectBucketID}`),
      "object_list",
    );
  }
  return workerRequest("/objects", "object_list");
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
  if (scenario === "db_read") {
    workerRequest("/db-read", "db_read");
    return;
  }
  if (scenario === "db_write") {
    workerRequest("/db-write", "db_write");
    return;
  }
  if (scenario === "db_mixed") {
    const slot = exec.scenario.iterationInTest % 10;
    workerRequest(slot < 7 ? "/db-read" : "/db-write", slot < 7 ? "db_read" : "db_write");
    return;
  }
  if (scenario === "db_multi") {
    const database = (exec.scenario.iterationInTest % databaseCount) + 1;
    const slot = exec.scenario.iterationInTest % 10;
    const operation = slot < 7 ? "read" : "write";
    workerRequest(`/db-${operation}/${database}`, `db_${operation}_${database}`);
    return;
  }
  if (isManyWorkersScenario()) {
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
