import { check, sleep } from "k6";
import exec from "k6/execution";
import http from "k6/http";
import { Counter } from "k6/metrics";

const controlBaseURL = (__ENV.CONTROL_BASE_URL || "http://127.0.0.1:8080").replace(/\/$/, "");
const gatewayBaseURL = (__ENV.BASE_URL || controlBaseURL).replace(/\/$/, "");
const apiToken = __ENV.API_TOKEN || "";
const orgID = __ENV.ORG_ID || "";
const workerCount = Number(__ENV.WORKER_COUNT || "100");
const provisionBatchSize = Number(__ENV.PROVISION_BATCH_SIZE || "10");
const vus = Number(__ENV.VUS || "50");
const duration = __ENV.DURATION || "5m";
const keepWorkers = __ENV.KEEP_WORKERS === "1";
const workerNamePrefix = __ENV.WORKER_NAME_PREFIX || "k6-high-count";

const workerRequests = new Counter("worker_requests");

export const options = {
  scenarios: {
    high_worker_count: {
      executor: "constant-vus",
      vus,
      duration,
      gracefulStop: "30s",
    },
  },
  setupTimeout: __ENV.SETUP_TIMEOUT || "20m",
  teardownTimeout: __ENV.TEARDOWN_TIMEOUT || "10m",
  summaryTrendStats: ["avg", "min", "med", "max", "p(90)", "p(95)", "p(99)"],
  thresholds: {
    "checks{phase:load}": ["rate>0.99"],
    "http_req_failed{phase:load}": ["rate<0.01"],
    "http_req_duration{phase:load}": ["p(95)<500", "p(99)<1000"],
    worker_requests: [`count>=${workerCount}`],
  },
};

function controlHeaders() {
  return {
    Authorization: `Bearer ${apiToken}`,
    "Content-Type": "application/json",
    "X-Nanoflare-Org-ID": orgID,
  };
}

function controlURL(path) {
  return `${controlBaseURL}/v1/organizations/${encodeURIComponent(orgID)}${path}`;
}

function batches(items, size, callback) {
  for (let offset = 0; offset < items.length; offset += size) {
    callback(items.slice(offset, offset + size));
  }
}

function cleanupWorkers(workers) {
  let pending = workers;
  for (let attempt = 1; attempt <= 3 && pending.length > 0; attempt += 1) {
    const failed = [];
    batches(pending, provisionBatchSize, (batch) => {
      const responses = http.batch(
        batch.map((worker) => ({
          method: "DELETE",
          url: controlURL(`/workers/${encodeURIComponent(worker.id)}`),
          params: { headers: controlHeaders(), tags: { phase: "cleanup" } },
        })),
      );
      responses.forEach((response, index) => {
        if (![204, 404].includes(response.status)) {
          failed.push(batch[index]);
        }
      });
    });
    pending = failed;
    if (pending.length > 0 && attempt < 3) {
      sleep(1);
    }
  }
  return pending;
}

function failAfterCleanup(message, workers) {
  cleanupWorkers(workers);
  throw new Error(message);
}

export function setup() {
  if (!apiToken || !orgID) {
    throw new Error("API_TOKEN and ORG_ID are required");
  }
  if (!Number.isInteger(workerCount) || workerCount < 2) {
    throw new Error(`WORKER_COUNT must be an integer of at least 2, got ${workerCount}`);
  }
  if (!Number.isInteger(provisionBatchSize) || provisionBatchSize < 1) {
    throw new Error(`PROVISION_BATCH_SIZE must be a positive integer, got ${provisionBatchSize}`);
  }

  const runID = `${Date.now()}-${Math.floor(Math.random() * 100000)}`;
  const workerSpecs = Array.from({ length: workerCount }, (_, index) => ({
    index,
    name: `${workerNamePrefix}-${runID}-${String(index + 1).padStart(4, "0")}`,
  }));
  const workers = [];

  batches(workerSpecs, provisionBatchSize, (batch) => {
    const responses = http.batch(
      batch.map((worker) => ({
        method: "POST",
        url: controlURL("/workers"),
        body: JSON.stringify({ name: worker.name }),
        params: { headers: controlHeaders(), tags: { phase: "provision" } },
      })),
    );

    let failure = "";
    responses.forEach((response, index) => {
      if (response.status !== 201) {
        failure ||= `worker creation failed for ${batch[index].name}: HTTP ${response.status} ${String(response.body).slice(0, 300)}`;
        return;
      }
      const created = response.json();
      if (!created || !created.id) {
        failure ||= `worker creation returned no ID for ${batch[index].name}`;
        return;
      }
      workers.push({ ...batch[index], id: created.id, hostname: created.hostname });
    });
    if (failure) {
      failAfterCleanup(failure, workers);
    }
  });

  batches(workers, provisionBatchSize, (batch) => {
    const responses = http.batch(
      batch.map((worker) => ({
        method: "POST",
        url: controlURL(`/workers/${encodeURIComponent(worker.id)}/deployments`),
        body: JSON.stringify({
          entrypoint: "worker.js",
          format: "modules",
          compatibility_date: "2025-12-10",
          files: [
            {
              path: "worker.js",
              content: `export default { fetch() { return new Response("high-worker-${worker.index}"); } };`,
            },
          ],
        }),
        params: { headers: controlHeaders(), tags: { phase: "provision" } },
      })),
    );

    responses.forEach((response, index) => {
      if (response.status !== 201) {
        failAfterCleanup(
          `deployment failed for ${batch[index].name}: HTTP ${response.status} ${String(response.body).slice(0, 300)}`,
          workers,
        );
      }
    });
  });

  console.log(`provisioned ${workers.length} workers for run ${runID}`);
  return { runID, workers };
}

export default function (data) {
  // iterationInTest is unique across VUs. The first WORKER_COUNT iterations
  // therefore touch every worker exactly once before the sequence repeats.
  const worker = data.workers[exec.scenario.iterationInTest % data.workers.length];
  const response = http.get(
    `${gatewayBaseURL}/internal/http/workers/${encodeURIComponent(worker.id)}/`,
    { tags: { endpoint: "worker_fetch", phase: "load" } },
  );
  workerRequests.add(1);
  check(
    response,
    {
      "worker status is 200": (result) => result.status === 200,
      "correct worker responded": (result) => result.body === `high-worker-${worker.index}`,
    },
    { phase: "load" },
  );
}

export function teardown(data) {
  if (keepWorkers) {
    console.log(
      `KEEP_WORKERS=1; retained ${data.workers.length} workers from run ${data.runID}: ${data.workers.map((worker) => worker.id).join(",")}`,
    );
    return;
  }
  const remaining = cleanupWorkers(data.workers);
  if (remaining.length > 0) {
    console.warn(
      `cleanup incomplete for run ${data.runID}; ${remaining.length} workers may remain: ${remaining.map((worker) => worker.id).join(",")}`,
    );
    return;
  }
  console.log(`deleted ${data.workers.length} workers from run ${data.runID}`);
}
