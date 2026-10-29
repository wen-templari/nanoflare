# Private DNS upgrade notes

## Split deployments

With WFP dispatch and its default DNS access enabled, `nanoflared` validates
`NANOFLARE_DNS_CONFIG` at startup, including when using a remote runner. A missing
configuration file or invalid configuration prevents startup. An unset variable
is valid and selects the system resolver.

Before upgrading, make any configured DNS file available to **both nanoflared and
the runner**. Inline JSON must also be configured on both services when they need
the same profiles. Namespace scripts use nanoflared's default profile; ordinary
Workers use the runner's configuration and their selected profile.

If namespace DNS is unnecessary, set `NANOFLARE_WFP_DNS=false`
(`--wfp-dns=false`) on nanoflared. Namespace JSON DoH then fails closed without
public fallback. In split mode, nanoflared skips the unused DNS configuration;
ordinary Worker DNS continues to use the runner.

## Trusted namespace policy

DNS remains enabled by default within the administrator-enabled trusted WFP
pilot. Only enable dispatch for scripts trusted to query the platform default
resolver, including private names. Socket allow lists do not restrict DNS
queries. Use `NANOFLARE_WFP_DNS=false` when that authority is inappropriate.
This default is not an isolation guarantee for untrusted tenants.

## Record compatibility

TXT, MX, CNAME, SRV, NS and PTR query raw records. Explicit profile servers remain
isolated from system/public fallback. System profiles use nameservers from
`/etc/resolv.conf`; record queries use absolute names and do not apply search
suffixes or per-domain OS resolver routing. Configure explicit servers on hosts
without this file or where that routing matters. A/AAAA lookups continue to use
Go's host resolver, including host-file behavior.

TXT preserves separate character strings in each record, including 255-byte
chunks, and avoids an extra escape layer for quotes and backslashes. The pinned
workerd TXT parser cannot represent every quote sequence: multi-string records
with consecutive literal quotes or a string ending in a quote, and single-string
records that both begin and end in a quote, return NOTIMP when ambiguous rather
than silently changing the value. An empty TXT string remains supported.

CNAME returns the immediate target rather than the end of the chain. PTR accepts
arbitrary names such as DNS-SD owners; IP literals are translated into reverse
query names. Raw record TTLs are retained, and truncated UDP responses retry over
TCP within the profile timeout. Cancellation closes outstanding queries.

Successful empty record responses retain DoH status 0 (NODATA), distinct from
status 3 (NXDOMAIN). The pinned workerd Node API nevertheless reports both as
`ENOTFOUND`; `ENODATA` parity requires an upstream runtime change. The existing
A/AAAA adapter retains its empty-answer workaround for that runtime.

Ordinary Workers using `disallow_importable_env` cannot access the private DNS
binding. Their calls now fall back to native fetch under the Worker's outbound
policy instead of throwing on the missing binding. They do not use `dns.profile`
in this mode. Resolver errors with an available binding never trigger public
fallback. Namespace DNS continues to use its invocation broker and WFP DNS policy.
