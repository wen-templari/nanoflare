# k6 Stress Test Results

Last updated: 2026-08-23

## Summary

The plain Worker path improved substantially after the August 23 deployment-resolution, cold-start, and duration-telemetry optimizations. A true two-minute hold at 50 concurrent VUs completed 315,377 requests at 2,627.9 requests per second with no failures. Average latency was 19.0 ms, p95 was 27.2 ms, and p99 was 35.1 ms. Fixed holds at 100, 150, and 250 VUs also passed with no failures.

A subsequent 250-VU, 10-minute soak completed 1,518,246 requests at 2,530.2 requests per second with no failures or interrupted iterations. Its 194.1 ms p95 and 257.5 ms p99 remained comfortably below threshold, with no time-dependent throughput decay.

For an apples-to-apples comparison with the earlier test profile, which ramped from zero to 50 VUs over two minutes, the optimized Worker completed 239,635 requests at 1,996.3 requests per second. The July run completed 16,809 requests at 139.8 requests per second. That is a 14.3x throughput increase, while p95 latency fell from 290.0 ms to 20.7 ms, a 92.9% reduction.

The k6 `sustained` profile previously used one ramping stage, despite its name. It now uses constant VUs for the full requested duration. Future sustained results should therefore be compared with the new 50-VU hold, not the older ramp results.

KV and object-storage results below remain the latest recorded storage runs. Database scenarios are implemented but still need result artifacts. The earlier multi-worker connection-exhaustion boundary also needs to be retested against the optimized build.

## August 23 Optimized Worker Retest

### Environment

- Git revision: `27a41ed` (`improve cold start performance`), including the preceding gateway lookup and telemetry optimization in `f5481a1`.
- Service under test: host-run optimized `nanoflared` on `127.0.0.1:8080`.
- Load generator: k6 v2.1.0 on Darwin/arm64.
- Route: internal Worker gateway, `/internal/http/workers/{workerID}/plain`.
- Worker: `6e3998ebd8bef447d16db9f0d73308aa87e15b47bb9dad15` (`load-test-worker`).
- Scenario: `plain`, no think time.
- Thresholds: failures below 1%, p95 below 500 ms, and p99 below 1 second.
- Local proxy variables were cleared and localhost was placed in `NO_PROXY`.

The fixed-concurrency result can be reproduced with:

```sh
ROUTE_VIA=internal \
BASE_URL=http://127.0.0.1:8080 \
WORKER_ID=6e3998ebd8bef447d16db9f0d73308aa87e15b47bb9dad15 \
SCENARIO=plain PROFILE=sustained VUS=50 DURATION=2m THINK_TIME=0 \
k6 run --summary-export var/k6-results/plain_50vus_optimized_hold_20260823.json \
  scripts/k6/worker-load.js
```

### Results

| Run                              |       Load shape |  Requests |     RPS | Failed |      Avg |      p95 |          p99 |      Max | Result |
| -------------------------------- | ---------------: | --------: | ------: | -----: | -------: | -------: | -----------: | -------: | ------ |
| Optimized fixed-concurrency hold |    50 VUs for 2m |   315,377 | 2,627.9 |  0.00% |  19.0 ms |  27.2 ms |      35.1 ms | 125.6 ms | Pass   |
| Optimized fixed-concurrency hold |   100 VUs for 2m |   305,423 | 2,544.5 |  0.00% |  39.2 ms |  68.5 ms |      88.4 ms | 212.3 ms | Pass   |
| Optimized fixed-concurrency hold |   150 VUs for 2m |   315,471 | 2,628.1 |  0.00% |  57.0 ms | 104.7 ms |     135.6 ms | 258.5 ms | Pass   |
| Optimized fixed-concurrency hold |   250 VUs for 2m |   296,512 | 2,470.2 |  0.00% | 101.1 ms | 196.7 ms |     257.7 ms | 616.4 ms | Pass   |
| Optimized saturation soak        |  250 VUs for 10m | 1,518,246 | 2,530.2 |  0.00% |  98.7 ms | 194.1 ms |     257.5 ms | 679.0 ms | Pass   |
| Optimized legacy-compatible ramp | 0→50 VUs over 2m |   239,635 | 1,996.3 |  0.00% |  12.5 ms |  20.7 ms |      26.9 ms |  54.9 ms | Pass   |
| July baseline ramp               | 0→50 VUs over 2m |    16,809 |   139.8 |  0.00% | 168.4 ms | 290.0 ms | Not retained | 422.3 ms | Pass   |

Artifacts:

- `var/k6-results/plain_50vus_optimized_hold_20260823.json`
- `var/k6-results/plain_50vus_optimized_hold_20260823.log`
- `var/k6-results/plain_50vus_optimized_20260823.json`
- `var/k6-results/plain_50vus_optimized_20260823.log`
- `var/k6-results/plain_100vus_optimized_hold_20260823.json`
- `var/k6-results/plain_150vus_optimized_hold_20260823.json`
- `var/k6-results/plain_250vus_optimized_hold_20260823.json`
- `var/k6-results/plain_250vus_optimized_soak_10m_20260823.json`

### Interpretation

The optimization moved the plain Worker path well beyond the old local 50-VU boundary. The 50-VU fixed hold processed about 52.6 requests per second per active VU while remaining far inside every latency and failure threshold. There was no visible degradation during any two-minute hold or during the 10-minute saturation soak.

Throughput plateaus around 2,500–2,630 requests per second from 50 through 250 VUs. Increasing concurrency beyond 50 therefore adds queueing latency rather than capacity: p95 rises from 27.2 ms at 50 VUs to 194.1 ms during the 250-VU soak while throughput changes by less than 4%. For this machine and request path, 50–150 VUs is the efficient operating range; 250 VUs is a validated stable saturation load.

The post-test cumulative server metrics showed zero Worker gateway errors and approximately 99.9% upstream connection reuse. The repository pool had all 25 connections open and a substantial cumulative wait count. Because these counters were not captured immediately before each run, they do not prove the pool is the limiting resource, but deployment-resolution database waits are the leading saturation hypothesis and should be measured as per-run deltas.

The hold is the new regression baseline for a warm single Worker through the internal gateway. It does not include Traefik, KV, database, object storage, cold-start latency, or multiple Workers. Those paths should be measured separately rather than extrapolated from this result.

## Historical July 22 Runs

The earlier k6 stress tests were run against the local `nanoflared` control plane and internal worker gateway on `127.0.0.1:8080`. Proxy environment variables were explicitly cleared for the test commands so localhost traffic did not route through the shell proxy:

```sh
env -u http_proxy -u https_proxy -u all_proxy \
  -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY \
  NO_PROXY=127.0.0.1,localhost
```

The single-worker tests now show stable behavior for plain Worker fetches, KV reads, KV writes, static assets, object storage through the Worker binding, mixed app traffic, and control-plane reads/writes. Earlier KV write and mixed plain/KV runs failed at 25 VUs, but later reruns on Wednesday, July 22, 2026 passed cleanly after restarting the service and after tightening the KV write path.

The multi-worker tests found a clear short-run boundary: 20 VUs passed cleanly, 30 VUs stayed under the 1% failure threshold, and 40 VUs collapsed. Longer multi-worker runs degraded over time and eventually produced `dial tcp 127.0.0.1:8080: connect: can't assign requested address`, indicating local connection/address exhaustion in the internal gateway/runtime-manager path or its client/server connection lifecycle.

### Environment

- Service under test: `nanoflared` on `127.0.0.1:8080`
- Current run mode observed during later tests: `go run ./cmd/nanoflared -addr :8080 -config-dir ./var/generated -litestream-enabled`
- Load tool: `k6`
- Test script: `scripts/k6/worker-load.js`
- Result artifacts: `var/k6-results/*.json` and `var/k6-results/*.log`
- Primary worker: `6e3998ebd8bef447d16db9f0d73308aa87e15b47bb9dad15`
- Multi-worker set: 5 deployed workers from `var/k6-results/multi-worker-ids.txt`

### Results

| Scenario                                                       |   Load | Duration | Requests |   RPS | Failed |      Avg |      p95 |       Max | Result               |
| -------------------------------------------------------------- | -----: | -------: | -------: | ----: | -----: | -------: | -------: | --------: | -------------------- |
| Plain Worker fetch                                             | 50 VUs |       2m |   16,809 | 139.8 |  0.00% | 168.4 ms | 290.0 ms |  422.3 ms | Pass                 |
| KV read (initial run)                                          | 25 VUs |       2m |    8,293 |  69.0 |  0.00% | 170.7 ms | 256.7 ms |  814.2 ms | Pass                 |
| KV read (rerun after restart)                                  | 25 VUs |       2m |    8,182 |  68.0 |  0.00% | 173.1 ms | 260.8 ms |  342.0 ms | Pass                 |
| KV write (initial run)                                         | 25 VUs |       2m |    7,592 |  63.2 |  5.01% | 187.2 ms | 308.6 ms | 1030.8 ms | Fail                 |
| KV write (rerun after restart and KV write-path changes)       | 25 VUs |       2m |    8,649 |  71.9 |  0.00% | 163.2 ms | 236.4 ms |  311.8 ms | Pass                 |
| Mixed plain/KV (initial run)                                   | 25 VUs |       2m |    8,415 |  70.0 |  4.19% | 167.9 ms | 261.6 ms |  468.6 ms | Fail                 |
| Mixed plain/KV (rerun after restart and KV write-path changes) | 25 VUs |       2m |    6,605 |  54.9 |  0.00% | 217.0 ms | 335.0 ms |  400.3 ms | Pass                 |
| Static assets                                                  | 25 VUs |       2m |   11,789 |  98.2 |  0.75% | 116.9 ms | 149.0 ms |  242.2 ms | Pass                 |
| Objects through Worker binding                                 | 25 VUs |       2m |    8,220 |  68.3 |  0.00% | 173.3 ms | 242.1 ms |  302.8 ms | Pass                 |
| Mixed app traffic                                              | 25 VUs |       2m |    8,216 |  68.3 |  0.00% | 172.4 ms | 299.0 ms |  496.4 ms | Pass                 |
| Control API reads                                              | 25 VUs |       2m |   12,146 | 100.9 |  0.00% | 113.3 ms | 449.4 ms |  834.6 ms | Pass                 |
| Control API low-rate writes/deploys                            |  5 VUs |       1m |    1,475 |  24.5 |  0.00% |  51.1 ms | 277.2 ms |  650.9 ms | Pass                 |
| Multi-worker                                                   | 20 VUs |       2m |    9,516 |  79.1 |  0.00% | 115.8 ms | 197.9 ms |  326.8 ms | Pass                 |
| Multi-worker                                                   | 30 VUs |       2m |    8,306 |  69.0 |  0.48% | 206.6 ms | 344.1 ms | 1137.7 ms | Pass under threshold |
| Multi-worker                                                   | 40 VUs |       2m |   18,056 | 150.0 | 65.44% | 121.2 ms | 411.2 ms | 1265.2 ms | Fail                 |
| Multi-worker port-guarded soak                                 | 20 VUs |       5m |   21,014 |  69.9 |  3.28% | 132.1 ms | 257.8 ms |  533.7 ms | Fail                 |
| Multi-worker debug after soak                                  |  5 VUs |      20s |      951 |  47.4 | 81.91% |  40.0 ms | 103.6 ms |  238.5 ms | Fail                 |

### Historical Findings

#### 1. Plain Worker Fetch Was Stable At 50 VUs

The plain Worker scenario completed 16,809 requests with 0 failures at about 140 requests per second. This gives a clean baseline for gateway plus `workerd` overhead without stateful storage.

The p95 latency was 290 ms. This is acceptable for a local stress baseline but leaves limited headroom before the configured 500 ms p95 threshold.

#### 2. KV Reads, Latest KV Writes, And Latest Mixed Plain/KV Traffic Were Stable At 25 VUs

KV reads completed with 0 failures in both the initial run and the later rerun at 25 VUs. The first KV write run at the same concurrency failed 5.01% of requests, but the latest rerun on Wednesday, July 22, 2026 passed with 0 failures, 8,649 requests, 71.9 requests per second, and p95 latency of 236.4 ms. The mixed plain/KV scenario also changed materially: the first run failed at 4.19%, while the rerun passed with 0 failures.

This points to the earlier KV write and mixed-traffic failures being sensitive to process state or the older write path, not an unavoidable 25-VU concurrency limit. After restarting `nanoflared` onto the newer KV write implementation, the live metrics also showed 0 worker-gateway errors, very high connection reuse, and 0 Postgres connection-pool waits during the reruns.

#### 3. Static Assets Were Fastest Of The Main User-Traffic Paths

Static assets ran at 98.2 requests per second with p95 latency of 149 ms and a 0.75% failure rate, which stayed below the 1% threshold. Asset serving is not the first bottleneck in this local test setup.

#### 4. Object Storage Through Worker Binding Was Stable At 25 VUs

The object storage test through the Worker binding completed with 0 failures at 25 VUs. Average latency was 173.3 ms and p95 was 242.1 ms.

An earlier object test through console object routes failed because the control API requires authentication and org headers. The Worker-binding path is the better user-traffic test for R2-style object behavior.

#### 5. Control Plane Was Stable With Correct Auth Headers

The read-only control API scenario initially failed because the bearer token alone was not enough; the API also requires `X-Nanoflare-Org-ID`. After adding the `ORG_ID` header support to the k6 script, the control API read test passed at 25 VUs with 0 failures.

The low-rate control write/deploy test also passed with 0 failures. That test was intentionally run at lower load to avoid creating thousands of namespaces/deployments in the local database.

#### 6. Multi-Worker Traffic Had A Sharp Boundary

The multi-worker scenario was clean at 20 VUs and still under threshold at 30 VUs. At 40 VUs it collapsed to a 65.44% failure rate.

The failure mode was dominated by k6 connection errors:

```text
dial tcp 127.0.0.1:8080: connect: can't assign requested address
```

This points to local address/socket exhaustion rather than ordinary HTTP-level failures from the application.

#### 7. Longer Multi-Worker Runs Degraded Over Time

A 20-VU, 5-minute multi-worker run failed with a 3.28% request failure rate. After that run, even a short 5-VU debug run failed heavily with the same `can't assign requested address` error.

The corrected port-specific guard showed that `127.0.0.1:8080` did not accumulate thousands of established connections during the guarded 5-minute run, but the client still later hit address exhaustion. This suggests the issue may involve broader local ephemeral-port churn, connection reuse, or another loopback path used by the runtime stack, not just live connections directly attached to `:8080`.

## Recommended Next Steps

1. Re-run every stateful path with the corrected constant-VU profile.
   Run `kv_read`, `kv_write`, `objects`, `db_read`, `db_write`, `db_mixed`, and `db_multi` against the optimized build. Database scenarios currently have no recorded result artifacts.

2. Investigate the 2.5k–2.6k RPS plateau before adding more VUs.
   Capture repository-pool wait deltas, database query time, CPU, and runtime latency during a repeat 50/150/250 curve. The current cumulative metrics suggest the 25-connection repository pool or deployment-resolution queries may be the next bottleneck.

3. Re-run multi-worker traffic before carrying forward the July diagnosis.
   The deployment-resolution optimization directly changes the gateway path exercised by that scenario. Repeat fixed 20, 30, 40, and 50 VU runs, then run a soak below the newly observed boundary.

4. Confirm the production ingress path separately.
   The August 23 retest intentionally isolates the internal Worker gateway. Run the same fixed hold through Traefik to quantify ingress overhead and validate router-level telemetry.

5. Capture system telemetry with each boundary run.
   Record CPU, memory, open file descriptors, Go goroutines, socket state, Postgres pool stats, and workerd process count. k6 latency alone cannot identify the next limiting resource.

## Bottom Line

The optimized warm single-Worker gateway is healthy through a fixed 250 VUs and a 10-minute saturation soak. The soak sustained 2,530.2 requests per second across 1.52 million requests with no failures and 194.1 ms p95 latency. The directly comparable ramp result is 14.3x faster than the July baseline. Practical throughput plateaus near 2.6k RPS, so the next useful work is bottleneck attribution rather than blindly increasing concurrency. Stateful storage, production ingress, multi-worker traffic, and cold starts still need fresh runs on the optimized build.
