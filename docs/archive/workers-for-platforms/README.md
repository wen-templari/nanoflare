# Workers for Platforms historical records

These documents preserve dated proposals, implementation milestones, audits and
review evidence. Statements such as “not implemented”, “not committed” or
“next step” refer to the recorded stage and may be superseded. Link/path repairs
keep the records navigable; their original findings are retained.

Use the [documentation index](../../README.md),
[current architecture](../../architecture/workers-for-platforms.md),
[API reference](../../reference/cloudflare-management-api.md) and
[remaining work](../../plans/workers-for-platforms-remaining-work.md) for current behavior.

| Record                                                                                            | Purpose                                                      |
| ------------------------------------------------------------------------------------------------- | ------------------------------------------------------------ |
| [Alignment research](workers-for-platforms-alignment.md)                                          | Original Cloudflare comparison and proposed model            |
| [Execution decision](workers-for-platforms-execution.md)                                          | Initial feasibility, future customer cells and typed bridge  |
| [Implementation plan](workers-for-platforms-implementation.md)                                    | Original ordered work and acceptance criteria                |
| [Step 2](workers-for-platforms-step-2-status.md)                                                  | Runtime authorization                                        |
| [Step 3](workers-for-platforms-step-3-status.md)                                                  | Isolation/bridge experiments                                 |
| [Step 4](workers-for-platforms-step-4-status.md)                                                  | Namespace/resource model                                     |
| [Step 5](workers-for-platforms-step-5-status.md)                                                  | Artifacts and publication                                    |
| [Step 6](workers-for-platforms-step-6-status.md)                                                  | Private dynamic dispatch                                     |
| [Step 7](workers-for-platforms-step-7-status.md)                                                  | Resource and outbound policy                                 |
| [Step 8](workers-for-platforms-step-8-status.md)                                                  | Tooling and telemetry                                        |
| [Step 9](workers-for-platforms-step-9-status.md) / [audit](workers-for-platforms-step-9-audit.md) | Management adapter, client qualification and later additions |
| [Step 10](workers-for-platforms-step-10-status.md)                                                | Capacity, storage, monitoring and recovery qualification     |
| [Independent review](workers-for-platforms-independent-review.md)                                 | Findings, fixes and bounded re-verification                  |
| [Live comparison prerequisite](workers-for-platforms-live-comparison.md)                          | Account entitlement blocker                                  |

Machine-readable qualification reports stay with their fixtures under
[scripts/wfp-qualification](../../../scripts/wfp-qualification/README.md).
Archived PoCs stay under [scripts/archive](../../../scripts/archive/README.md).

Active development plans: [remaining work](../../plans/workers-for-platforms-remaining-work.md)
and [CPU enforcement](../../plans/workers-for-platforms-cpu-enforcement.md).
