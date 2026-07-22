# k6 Stress Test Results

Date: 2026-07-22

## Summary

The k6 stress tests were run against the local `nanoflared` control plane and internal worker gateway on `127.0.0.1:8080`. Proxy environment variables were explicitly cleared for the test commands so localhost traffic did not route through the shell proxy:

```sh
env -u http_proxy -u https_proxy -u all_proxy \
  -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY \
  NO_PROXY=127.0.0.1,localhost
```

The single-worker tests now show stable behavior for plain Worker fetches, KV reads, KV writes, static assets, object storage through the Worker binding, mixed app traffic, and control-plane reads/writes. Earlier KV write and mixed plain/KV runs failed at 25 VUs, but later reruns on Wednesday, July 22, 2026 passed cleanly after restarting the service and after tightening the KV write path.

The multi-worker tests found a clear short-run boundary: 20 VUs passed cleanly, 30 VUs stayed under the 1% failure threshold, and 40 VUs collapsed. Longer multi-worker runs degraded over time and eventually produced `dial tcp 127.0.0.1:8080: connect: can't assign requested address`, indicating local connection/address exhaustion in the internal gateway/runtime-manager path or its client/server connection lifecycle.

## Environment

- Service under test: `nanoflared` on `127.0.0.1:8080`
- Current run mode observed during later tests: `go run ./cmd/nanoflared -addr :8080 -config-dir ./var/generated -litestream-enabled`
- Load tool: `k6`
- Test script: `scripts/k6/worker-load.js`
- Result artifacts: `var/k6-results/*.json` and `var/k6-results/*.log`
- Primary worker: `6e3998ebd8bef447d16db9f0d73308aa87e15b47bb9dad15`
- Multi-worker set: 5 deployed workers from `var/k6-results/multi-worker-ids.txt`

## Results

| Scenario | Load | Duration | Requests | RPS | Failed | Avg | p95 | Max | Result |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---|
| Plain Worker fetch | 50 VUs | 2m | 16,809 | 139.8 | 0.00% | 168.4 ms | 290.0 ms | 422.3 ms | Pass |
| KV read (initial run) | 25 VUs | 2m | 8,293 | 69.0 | 0.00% | 170.7 ms | 256.7 ms | 814.2 ms | Pass |
| KV read (rerun after restart) | 25 VUs | 2m | 8,182 | 68.0 | 0.00% | 173.1 ms | 260.8 ms | 342.0 ms | Pass |
| KV write (initial run) | 25 VUs | 2m | 7,592 | 63.2 | 5.01% | 187.2 ms | 308.6 ms | 1030.8 ms | Fail |
| KV write (rerun after restart and KV write-path changes) | 25 VUs | 2m | 8,649 | 71.9 | 0.00% | 163.2 ms | 236.4 ms | 311.8 ms | Pass |
| Mixed plain/KV (initial run) | 25 VUs | 2m | 8,415 | 70.0 | 4.19% | 167.9 ms | 261.6 ms | 468.6 ms | Fail |
| Mixed plain/KV (rerun after restart and KV write-path changes) | 25 VUs | 2m | 6,605 | 54.9 | 0.00% | 217.0 ms | 335.0 ms | 400.3 ms | Pass |
| Static assets | 25 VUs | 2m | 11,789 | 98.2 | 0.75% | 116.9 ms | 149.0 ms | 242.2 ms | Pass |
| Objects through Worker binding | 25 VUs | 2m | 8,220 | 68.3 | 0.00% | 173.3 ms | 242.1 ms | 302.8 ms | Pass |
| Mixed app traffic | 25 VUs | 2m | 8,216 | 68.3 | 0.00% | 172.4 ms | 299.0 ms | 496.4 ms | Pass |
| Control API reads | 25 VUs | 2m | 12,146 | 100.9 | 0.00% | 113.3 ms | 449.4 ms | 834.6 ms | Pass |
| Control API low-rate writes/deploys | 5 VUs | 1m | 1,475 | 24.5 | 0.00% | 51.1 ms | 277.2 ms | 650.9 ms | Pass |
| Multi-worker | 20 VUs | 2m | 9,516 | 79.1 | 0.00% | 115.8 ms | 197.9 ms | 326.8 ms | Pass |
| Multi-worker | 30 VUs | 2m | 8,306 | 69.0 | 0.48% | 206.6 ms | 344.1 ms | 1137.7 ms | Pass under threshold |
| Multi-worker | 40 VUs | 2m | 18,056 | 150.0 | 65.44% | 121.2 ms | 411.2 ms | 1265.2 ms | Fail |
| Multi-worker port-guarded soak | 20 VUs | 5m | 21,014 | 69.9 | 3.28% | 132.1 ms | 257.8 ms | 533.7 ms | Fail |
| Multi-worker debug after soak | 5 VUs | 20s | 951 | 47.4 | 81.91% | 40.0 ms | 103.6 ms | 238.5 ms | Fail |

## Findings

### 1. Plain Worker Fetch Is Stable At 50 VUs

The plain Worker scenario completed 16,809 requests with 0 failures at about 140 requests per second. This gives a clean baseline for gateway plus `workerd` overhead without stateful storage.

The p95 latency was 290 ms. This is acceptable for a local stress baseline but leaves limited headroom before the configured 500 ms p95 threshold.

### 2. KV Reads, Latest KV Writes, And Latest Mixed Plain/KV Traffic Are Stable At 25 VUs

KV reads completed with 0 failures in both the initial run and the later rerun at 25 VUs. The first KV write run at the same concurrency failed 5.01% of requests, but the latest rerun on Wednesday, July 22, 2026 passed with 0 failures, 8,649 requests, 71.9 requests per second, and p95 latency of 236.4 ms. The mixed plain/KV scenario also changed materially: the first run failed at 4.19%, while the rerun passed with 0 failures.

This points to the earlier KV write and mixed-traffic failures being sensitive to process state or the older write path, not an unavoidable 25-VU concurrency limit. After restarting `nanoflared` onto the newer KV write implementation, the live metrics also showed 0 worker-gateway errors, very high connection reuse, and 0 Postgres connection-pool waits during the reruns.

### 3. Static Assets Are Fastest Of The Main User-Traffic Paths

Static assets ran at 98.2 requests per second with p95 latency of 149 ms and a 0.75% failure rate, which stayed below the 1% threshold. Asset serving is not the first bottleneck in this local test setup.

### 4. Object Storage Through Worker Binding Is Stable At 25 VUs

The object storage test through the Worker binding completed with 0 failures at 25 VUs. Average latency was 173.3 ms and p95 was 242.1 ms.

An earlier object test through console object routes failed because the control API requires authentication and org headers. The Worker-binding path is the better user-traffic test for R2-style object behavior.

### 5. Control Plane Is Stable With Correct Auth Headers

The read-only control API scenario initially failed because the bearer token alone was not enough; the API also requires `X-Nanoflare-Org-ID`. After adding the `ORG_ID` header support to the k6 script, the control API read test passed at 25 VUs with 0 failures.

The low-rate control write/deploy test also passed with 0 failures. That test was intentionally run at lower load to avoid creating thousands of namespaces/deployments in the local database.

### 6. Multi-Worker Traffic Has A Sharp Boundary

The multi-worker scenario was clean at 20 VUs and still under threshold at 30 VUs. At 40 VUs it collapsed to a 65.44% failure rate.

The failure mode was dominated by k6 connection errors:

```text
dial tcp 127.0.0.1:8080: connect: can't assign requested address
```

This points to local address/socket exhaustion rather than ordinary HTTP-level failures from the application.

### 7. Longer Multi-Worker Runs Degrade Over Time

A 20-VU, 5-minute multi-worker run failed with a 3.28% request failure rate. After that run, even a short 5-VU debug run failed heavily with the same `can't assign requested address` error.

The corrected port-specific guard showed that `127.0.0.1:8080` did not accumulate thousands of established connections during the guarded 5-minute run, but the client still later hit address exhaustion. This suggests the issue may involve broader local ephemeral-port churn, connection reuse, or another loopback path used by the runtime stack, not just live connections directly attached to `:8080`.

## Recommended Next Steps

1. Push the KV write boundary higher.
   KV writes no longer fail at 25 VUs in the latest rerun, so the next useful step is to run `kv_write` at 30 VUs and 40 VUs and identify the new failure point, if any.

2. Investigate connection reuse in the internal gateway path.
   The repeated `can't assign requested address` errors suggest k6 or the gateway path is burning through local ephemeral ports. Confirm whether connections are being reused and whether response bodies are always closed in the Go proxy/client path.

3. Add k6 options for connection behavior.
   Run comparison tests with explicit connection reuse settings, for example default reuse versus `--no-connection-reuse`, to separate k6 client behavior from server-side connection lifecycle.

4. Keep server-side metrics on for comparison runs.
   Record open file descriptors, Go goroutines, active `net/http` connections, Postgres connection pool stats, and workerd process counts over time. In the latest 25-VU KV write rerun, the Postgres pool reached 23 open idle connections with 0 wait events, which is a useful new baseline.

5. Keep long soaks below the observed cliff.
   Based on these runs, 20 VUs for multi-worker traffic is not safe for long local soaks yet. Use shorter runs or lower VUs until the connection exhaustion issue is understood.

6. Re-run the matrix after fixes.
   The useful regression matrix is: `plain`, `kv_read`, `kv_write`, `assets`, `objects_worker_binding`, `mixed_app_worker_binding`, `control_api_with_org`, and `multi_worker` at 20/30/40 VUs.

## Bottom Line

Single-worker app traffic is currently healthy, including the latest `kv_read`, `kv_write`, and mixed plain/KV reruns at 25 VUs. Control-plane traffic is healthy when auth and org headers are included. Multi-worker traffic still exposes the most serious issue: a sharp failure cliff and longer-run connection/address exhaustion on the local machine. The next engineering focus should shift from "KV writes are broken at 25 VUs" to "find the new single-worker KV/mixed limit and continue investigating connection lifecycle behavior in the multi-worker internal gateway/runtime path."
