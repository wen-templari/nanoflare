# Cloudflare live comparison prerequisite

> Historical record. Status statements and proposed designs below describe the work at the time.
> See the [documentation index](../../README.md) for the maintained contract and
> [remaining work](../../plans/workers-for-platforms-remaining-work.md) for current gates.

Status: **blocked by account entitlement**, verified 2026-10-19. Local native implementation and compatibility contract tests may continue, but cannot satisfy the live comparison acceptance criterion in step 9.

The user supplied a local staging account profile. Its account identity and credential values are intentionally omitted from this repository.

Read-only validation used the documented [list dispatch namespaces API](https://developers.cloudflare.com/api/resources/workers_for_platforms/subresources/dispatch/subresources/namespaces/). It returned HTTP 403 with Cloudflare error code 10121, indicating that the account does not have dispatch namespace access. No namespace, script, subscription or billing resource was created or changed.

The user confirmed that no available account has Workers for Platforms access. Do not repeatedly request credentials or attempt to purchase/enable a subscription. Resume the live comparison only when an enabled staging account becomes available. Native and compatibility adapter tests must remain labeled local; drop-in Wrangler/SDK compatibility and live portability remain unverified.
