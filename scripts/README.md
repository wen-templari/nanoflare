# Development scripts

## Workers for Platforms (WFP)

Use the official Cloudflare Publisher and Internal Sites examples as the primary
WFP application acceptance workflow. The client and qualification suites cover
additional contracts that those applications do not exercise. Each directory
documents its prerequisites and limitations.

| Directory                                        | Purpose                                                              | When to use                                       |
| ------------------------------------------------ | -------------------------------------------------------------------- | ------------------------------------------------- |
| [wfp-examples](wfp-examples/README.md)           | Pinned official Publisher and Internal Sites with persistent storage | Primary application acceptance and browser checks |
| [wfp-client-flows](wfp-client-flows/README.md)   | SDK and Wrangler wire compatibility                                  | Client protocol regression checks                 |
| [wfp-qualification](wfp-qualification/README.md) | Storage, recovery, capacity and monitoring qualification             | Explicit release qualification runs               |

Focused Go tests remain the regression checks for authorization, tenant
isolation, asset integrity, publication and routing. The official applications
complement these checks; they do not exercise all negative and failure cases.

Historical loader and isolation experiments live under [archive](archive/README.md).
The redundant disposable local demo has been removed in favor of the official
examples.
