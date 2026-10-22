# Bounded execution/isolation experiment

Opt-in step 3 investigation. Read the [status and decisions](../../docs/plans/workers-for-platforms-step-3-status.md) and [separate CPU workstream](../../docs/plans/workers-for-platforms-cpu-enforcement.md). No production runner behavior is changed here.

From the repository root, with Docker running and local `nanoflare-e2e/nanoflare-runner:local` and `node:24-slim` images already available:

```sh
python3 scripts/wfp-isolation-poc/run.py
```

The harness never pulls or builds images. It resolves local image tags to immutable IDs and records executable and fixture SHA-256 values in [results](results-step3-linux.json). Default native executable is the Linux arm64 npm-installed workerd binary. Use `--image`, `--binary` and `--report` to qualify a different local image/build. The tested image reports workerd 2026-09-11, matching the repository Dockerfile's pinned npm version. No global runtime installation or package versions change.

Each run creates uniquely named containers and an internal Docker network with label `codex.task=wfp-step3`; it removes its own containers/network and temporary files on normal completion or Python exceptions. Forced termination of the controller may require manual cleanup of those labeled resources. It never targets an existing Worker. The relay publishes only an ephemeral localhost port; customer containers publish none, use an internal-only network, and receive only synthetic code/data. No Docker socket or real credentials are mounted.

Resource-heavy probes run only in disposable containers with 192 MiB RAM+swap and a 0.5-core CPU rate quota. Each client/probe and Docker command has a parent timeout. The harness forces process termination rather than leaving infinite loops running. Whole-process Linux `/proc/1/stat` CPU accounting is separate from elapsed HTTP timing; it is not per-invocation CPU attribution. The memory probe fills bounded chunks until the container exits under its cgroup limit. A separate peer must continue serving requests.

External budget probes allow exactly 2 synthetic effects per fixture invocation, including parallel calls, redirects, retries, resource calls, RPC-facade calls, mixed calls and `waitUntil()` effects. They do not access real KV, databases, object stores or the Internet. The counter is in the separate relay process; the local native WorkerEntrypoint facade holds the request lease. It uses per-request loader keys as a fixture constraint, not a scalable warm reuse design.

A successful harness run means its observations and assertions held. It intentionally records **blockers** for native CPU interruption and background execution. Independent Docker processes contain the tested failures, but this does not prove hardened isolation, CPU-limit parity, production budgets, native RPC transfer across processes or an untrusted release. See [capabilities.json](capabilities.json) for the explicit assessment.

## Final bridge and lifecycle qualification

```sh
python3 scripts/wfp-isolation-poc/bridge.py
python3 scripts/wfp-isolation-poc/run.py --report scripts/wfp-isolation-poc/results-step3-final-linux.json
```

The [execution decision](../../docs/architecture/workers-for-platforms-execution.md) selects the restricted typed-facade subset. The [bridge results](results-step3-bridge.json) cover warm revision reuse, per-request native props/facades, stream revision pinning, cancellation, background/wall deadlines, revocation/replay denial, admission and bounded terminal history. It uses the same disposable container controls and inherits cleanup from the isolation harness. Only the synthetic relay uses Node; the workerd process is the pinned native binary.

`bridge/dispatcher.js` caches by revision and passes a new facade via entrypoint props. `bridge/relay.js` owns authoritative invocation state and synthetic effects in the separate relay process. Caller headers cannot select its lease/revision. The single cell has a narrow fixture transport credential; the relay's management credential is never given to the cell. There are no real user credentials or resource effects. The positive load sample retries 503 admission rejections within a bounded client deadline; it is not a throughput benchmark. A separate saturation test requires a 503 for the seventeenth live lease.

The bridge closes global outbound and deliberately supports only typed `identify` and resource `fetch` calls. General native RPC serialization, WebSockets, streaming uploads, runtime eviction, production sandbox/capacity and warm request-specific global outbound remain unqualified. The old request-keyed outbound fixture remains useful evidence of a tradeoff; it is not the selected reuse strategy.
