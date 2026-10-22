# Workers for Platforms feasibility PoC

This is an opt-in local experiment, not a production feature. It implements a namespace facade on workerd's experimental Worker Loader and exercises Nanoflare handlers. The initial reports reproduce the baseline blockers; the updated telemetry probe requires the step 2 authorization fix. Production source is now changed for that fix; nothing is deployed, and package versions and lockfiles are unchanged.

See the [overall implementation plan](../../docs/plans/workers-for-platforms-implementation.md) for the ordered work after this PoC.

**Current status, 2026-10-18:** [step 2](../../docs/plans/workers-for-platforms-step-2-status.md) is implemented and verified locally. Native telemetry forgery and unbound KV access are denied on both tested runtime versions. The original reports/table below are historical baseline observations. New evidence is in [results-step2.json](results-step2.json) and [results-step2-current.json](results-step2-current.json); these do not overwrite the original reports. CPU/subrequest enforcement, peer isolation and corporate egress remain blockers.

## Run

From the repository root, with Go and workerd available:

```sh
go test -tags=wfppoc -count=1 -timeout=90s -v ./scripts/wfp-poc -args -report=results-step2.json
```

To test a separately installed binary:

```sh
go test -tags=wfppoc -race -count=1 -timeout=90s -v ./scripts/wfp-poc \
  -args -workerd=/absolute/path/to/workerd -report=results-step2-current.json
```

The report path is relative to this directory. `wfppoc` deliberately excludes the CPU probes from ordinary `go test ./...`. Supported harness hosts are Linux and macOS. An explicit PoC run fails if workerd is unavailable. The fixture uses compatibility date `2025-12-10` for comparable behavior across binaries.

Every server is created locally using temporary configuration, dummy identities, and synthetic data. Outbound policy requests go to a Worker sink, not the requested Internet host. The infinite-loop probes start their own disposable process group, abort the client after 150 ms, test a peer for another 250 ms, then kill that process group. They never target an existing Nanoflare deployment. Processes and listeners are cleaned up on completion.

An exit code of zero means the experiments ran and assertions held. **It does not mean the security checks passed:** reproduced defects are recorded as `blocker`. `verified` applies to the stated fixture behavior; `measured` is a small sample; `not_verified` and `needs_review` are unresolved.

## Architecture

```text
Local HTTP client
  -> platform dispatcher (one exposed workerd socket)
     -> authenticated local artifact registry (namespace + script + revision)
     -> experimental Worker Loader
        -> private alpha / beta customer code
           -> explicitly granted STATE service (production alpha only)
           -> request-scoped Connector RPC capability through native ctx.props
           -> optional platform Outbound Worker
```

- `dispatcher.js`: fixture authentication, target selection, native loopback RPC and outbound entrypoints.
- `namespace.js`: synchronous `get(...).fetch(...)` facade, revision-based loader keys, explicit environment grants and native props transfer.
- `customer.js`: controlled user-code scenarios, including the deliberately nonterminating CPU probe.
- `poc_test.go`: artifact registry, disposable process lifecycle, behavioral assertions, Nanoflare integration probes and evidence generation.

The registry is in-memory and authentication uses dummy bearer identities. It is not the Nanoflare control plane. The loader replaces the initially proposed HTTP-only dispatch fallback: native props and RPC proved feasible. The current Nanoflare shared pool manager is unchanged; independent loader updates are an alternative architecture demonstrated by this fixture, not an improvement already integrated into that manager.

The dispatcher gets the registry and loader capabilities. Customer code gets neither. Alpha and beta share a namespace, while staging alpha has a distinct revision and no production STATE grant. No user Worker receives a public socket. This demonstrates capability attachment, not complete database/object-store tenancy enforcement.

## Results, 2026-10-18

Both suites ran on macOS arm64:

| Experiment                                                             | Installed workerd 2025-12-10               | Candidate workerd 2026-10-18               |
| ---------------------------------------------------------------------- | ------------------------------------------ | ------------------------------------------ |
| Namespace lookup, authentication and private users                     | Verified                                   | Verified                                   |
| 24 concurrent invocations with distinct native props                   | Verified                                   | Verified                                   |
| STATE only available to production alpha                               | Verified                                   | Verified                                   |
| Native RPC stub passed in props                                        | Verified                                   | Verified                                   |
| RPC stub rejected when reused in a later request                       | Verified                                   | Verified                                   |
| Customer code/revision update without dispatcher restart               | Verified                                   | Verified                                   |
| Old response stream completes while new code serves requests           | Verified                                   | Verified                                   |
| Client cancellation invokes customer stream cancellation               | Callback not observed within 1 second      | Callback observed                          |
| `globalOutbound: null` blocks HTTP and TCP to a live loopback listener | Verified                                   | Verified                                   |
| Outbound policy receives separate per-request context                  | Verified, with reuse tradeoff below        | Verified, with reuse tradeoff below        |
| 10 ms CPU limit interrupts an infinite loop                            | Not enforced in the tested loader path     | Not enforced in the tested loader path     |
| Peer Worker responds after infinite-loop client's timeout              | No, requires process kill                  | No, requires process kill                  |
| Subrequest limit of 2 stops 5 outgoing fetch calls                     | No; all 5 completed                        | No; all 5 completed                        |
| Nanoflare rejects forged duration/log producer identity                | No                                         | No                                         |
| Telemetry capability confined to telemetry operations                  | No; synthetic unbound KV data was read     | No; synthetic unbound KV data was read     |
| Native network allowlist still applies behind corporate proxy adapter  | No; explicit local bypass reached loopback | No; explicit local bypass reached loopback |

Machine-readable observations, version and fixture hashes are saved in [installed-runtime results](results.json) and [candidate-runtime results](results-current.json). Measurements vary by host and run. The candidate was downloaded as a separate npm platform binary, without replacing the installed runtime; its immutable package provenance is in [runtime artifacts](runtime-artifacts.json).

### Native props and RPC are feasible

`LOADER.get(revisionKey, getCode).getEntrypoint(undefined, { props })` worked on both binaries with `--experimental`. The facade can expose a WFP-like call shape without serializing capabilities into HTTP headers. User code can invoke a passed `WorkerEntrypoint` capability, and reusing the saved capability in another request throws a cross-request I/O error.

Updating the registry changes alpha's source and revision. The next request receives both `v2` and a marker introduced by the new source, while an already-open `v1` stream finishes normally. The dispatcher source hash and process identity remain unchanged. This proves code replacement, not merely changing an environment variable.

### CPU and subrequest controls remain blockers

The limit probes supply `limits` at both WorkerCode creation and `getEntrypoint` options. With `cpuMs: 10`, the loop starts, the client reaches its 150 ms timeout, and a peer request still cannot finish within 250 ms. The parent kills the runtime afterward. With `subRequests: 2`, five outgoing synthetic fetches complete.

These are bounded observations of the tested open-source loader/server path. They do not claim that Cloudflare's hosted WFP limits fail or that no other execution architecture can enforce limits. An HTTP timeout cannot be advertised as CPU interruption. Merely upgrading to the tested newer binary did not solve this.

Upstream source exposes the relevant [Worker Loader limit fields](https://github.com/cloudflare/workerd/blob/main/src/workerd/api/worker-loader.h) and [resource limit types](https://github.com/cloudflare/workerd/blob/main/src/workerd/io/io-channels.h). Accepting a field in an API does not establish that the self-hosted server enforces it.

### Runtime capability boundaries need correction

The telemetry probe uses the production config generator, Worker wrapper, duration handler and output parser. Customer code submits a duration event naming `victim` and emits a `victim` log marker. Both are attributed to that identity.

The same injected `__NANOFLARE_DURATION_COLLECTOR` external service can make requests to other paths on the private runtime listener. The fixture recreates the relevant listener routes with the real handlers. Given a known synthetic victim namespace ID, customer code reads that namespace's KV value without an Authorization header or a KV binding. The in-memory repository accepts the empty capability. PostgreSQL and a deployed environment were not tested; this result is specific to the reproduced local path and is sufficient to reject it as an untrusted execution boundary.

Required fixes before untrusted customer code: restrict the telemetry service to its intended operation; assign producer identity outside user control; require and validate runtime authorization at each handler; verify resource grants against the exact invoked revision. Fixing log formatting alone does not address this broader capability issue.

### Corporate proxy routing is not customer egress authorization

The production config generator with `NetworkAllow: ["public"]` rejects a request to the live loopback fixture. With the existing corporate egress adapter and a loopback `NoProxy` entry, the same request succeeds. This is consistent with that adapter's documented routing purpose, but it means the adapter needs its own explicit destination authorization before it can enforce customer-code policy.

The new loader fixture separately proves that a null outbound capability blocks global HTTP and TCP. The outbound Worker example returns synthetic responses and rejects destinations outside `allowed.example`; full DNS/redirect/proxy enforcement remains untested.

### Request-bound outbound context has a reuse tradeoff

The PoC creates a unique loader key for an invocation carrying a request-bound outbound capability. That avoids capturing another request's capability/context in an isolate cached only by revision. It verifies correct context delivery, but does not prove efficient warm-isolate reuse with dynamic outbound context. A production design needs a native invocation-scoped outbound mechanism or another measured approach; do not copy the unique-key strategy into a large deployment without capacity testing.

### The scale sample is not a capacity benchmark

The fixture loads batches of 1, 10 and 50 new customer revisions sequentially. It records elapsed time and total RSS of the runtime process tree (including the Node launcher when used). Reports show the samples; no production throughput or maximum-Worker claim is derived from them. Old revisions are kept warm by the loader during the short run, so lifecycle/eviction policy still needs investigation. Comparing process RSS across the two binary launch methods also includes different launcher overhead.

## Remaining gates and recommended next work

1. **Runtime authorization and capability confinement — completed locally in step 2.** Reproduce the KV case against PostgreSQL, then add enforcement tests that require it to fail. Move telemetry identity out of customer-written payloads/text.
2. **Execution-budget investigation — step 3 evidence available.** See the [Linux experiment](../wfp-isolation-poc/README.md); [real CPU enforcement](../../docs/plans/workers-for-platforms-cpu-enforcement.md) is now a separate dependency. Prove CPU interruption and peer isolation using a runner/runtime change or separate failure domains. Do not claim WFP CPU/subrequest parity from the loader facade.
3. **Choose and validate the sandbox boundary.** This PoC does not implement a VM/container boundary or test memory-exhaustion isolation. [workerd's security guidance](https://github.com/cloudflare/workerd/blob/main/README.md) still requires an additional secure sandbox for potentially malicious code.
4. **Integrate the proven loader path as a trusted-only experiment.** Add real namespace ownership, immutable artifact lookup, grants and publication semantics after the security fixes. Validate background tasks, RPC entrypoints, cache behavior and weighted revisions.
5. **Resolve outbound reuse and capacity.** Measure cold/warm behavior, eviction, backpressure, concurrent deployments and fault recovery. Repeat cancellation checks on the selected runtime version.
6. **Then add management compatibility.** Multipart uploads, settings, secrets/tags, asset sessions and CLI/local development should share the same internal authorization and deployment model.

No hardened sandbox, memory pressure, cache isolation, database/object-store grant matrix, DNS rebinding, production load, or live Cloudflare parity result is claimed by these tests.
