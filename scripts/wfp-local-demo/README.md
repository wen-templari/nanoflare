# Local platform development example

This disposable fixture runs the trusted shared-runtime subset with native
workerd. It requires no Cloudflare account. It does not establish Cloudflare API
compatibility, production capacity, CPU enforcement or untrusted isolation.

## Start

Build from the repository root and supply an absolute path to a native workerd
binary (an npm wrapper is rejected). The verified baseline is workerd 2025-12-10;
the dynamic integration suite also passes on workerd 2026-10-18.

```sh
go build -o /tmp/nf-wfp-local-demo ./scripts/wfp-local-demo
/tmp/nf-wfp-local-demo --workerd /absolute/path/to/workerd --metadata /tmp/nf-wfp-demo.json
```

The control API binds `127.0.0.1:54335`. In another terminal, start the console:

```sh
NANOFLARED_URL=http://127.0.0.1:54335 pnpm --dir packages/ui dev --host 127.0.0.1 --port 54336 --strictPort
```

Open `http://127.0.0.1:54336` and sign in with
`platform-demo@example.com` / `local-demo-password`. These are synthetic local
credentials. The fixture seeds a local paid-tier organization to allow several
scripts; this does not provision billing or a Cloudflare subscription.

Control metadata and KV are in memory. SQLite and workerd are real. Object
storage is not configured. The private runtime transport uses separate loopback
listeners. Stop the fixture with Ctrl-C to terminate its runtime and remove its
temporary state. Every restart creates new resource and Worker IDs; the optional
metadata file contains IDs and endpoints, with no credentials.

## Dispatch, update and roll back

1. In **Dispatch namespaces**, open `local-staging`. `alpha` and `beta` are
   private dynamic Workers belonging to different customers. Both have explicit
   grants to the demonstration KV namespace and SQLite database.
2. Use the printed dispatch endpoint with `Authorization: Bearer local-demo`.
   Replace its final `/alpha` with `/beta` to dispatch the other script. This
   header is the example platform's caller authentication, not a control token.
   The response demonstrates KV, SQLite and the platform's native RPC facade.
3. Create a project directory with the following `nanoflare.json`, substituting
   IDs from `/tmp/nf-wfp-demo.json`. Use the private Worker's immutable ID.

   ```json
   {
     "name": "alpha",
     "worker_id": "WORKER_ID",
     "main": "worker.js",
     "files": ["worker.js"],
     "format": "modules",
     "compatibility_date": "2025-12-10",
     "kv_namespaces": [{ "binding": "STATE", "id": "KV_NAMESPACE_ID" }],
     "db": [{ "binding": "DB", "database_id": "DATABASE_ID" }]
   }
   ```

   A minimal `worker.js` is:

   ```js
   export default {
     async fetch(request, env, ctx) {
       console.log("Customer request", ctx.props.customer);
       await env.STATE.put(ctx.props.customer, "v2");
       return Response.json({
         version: "v2",
         customer: ctx.props.customer,
         answer: await env.DB.prepare("SELECT 42 AS answer").first("answer"),
         platform: await ctx.props.PLATFORM.identify(),
       });
     },
   };
   ```

4. Build the CLI with `go build -o /tmp/nf-cli ./cmd/nanoflare`. From the example
   project, run `/tmp/nf-cli platform bundle revision-v2.json`. Export is local
   and requires explicit binding IDs. It refuses to overwrite an existing file.
5. Open the private Worker, select **Runtime → Deploy a bundle**, choose that
   JSON file and deploy. Bundles replace the complete source, variables, bindings
   and assets; saved Worker secrets are used. Dispatch again to verify `v2`.
6. Export a different file after changing the version to `v3`, deploy and verify
   it. In **Deployments**, open the previous revision's action menu and choose
   **Rollback**. Dispatch again to verify `v2`.
7. Inspect **Output** for customer-attributed logs and chain IDs, and **Runtime**
   for capabilities and publication stages. `route published` does not mean a
   dynamic revision has been loaded or verified ready. CPU accounting/limits
   remain unavailable. Counters are scoped to the current daemon process.

For a real Nanoflare staging environment, use its organization credentials and
explicitly granted resource IDs with the same native CLI operations. The fixture
credentials, in-memory resources and seeded tier are local examples only.

## CLI control operations

After authenticating to the intended Nanoflare API and selecting an organization,
the supported operations include:

```sh
nanoflare platform namespace list
nanoflare platform script list NAMESPACE_ID
nanoflare platform capabilities WORKER_ID
nanoflare platform publications WORKER_ID
nanoflare platform deploy WORKER_ID revision-v2.json
nanoflare platform deployments WORKER_ID
nanoflare platform rollback WORKER_ID RETAINED_REVISION_ID
```

Use `--api-url` and `--org` to select an explicit endpoint and organization.
Grant replacement requires `expected_revision` and the complete `grants` list;
retrieve the current namespace first to preserve other customers' grants.

See the [supported contract](../../docs/architecture/workers-for-platforms-supported-contract.md)
and [operator notes](../../docs/plans/workers-for-platforms-operations.md).

## Management adapter prototype

The optional `--enable-cloudflare-compatibility` fixture flag enables the
incomplete local adapter under `/client/v4/accounts/local-platform-demo/workers/dispatch/namespaces`.
Use Nanoflare control credentials; Cloudflare account credentials do not work.
Mutations require `X-Nanoflare-Execution-Profile: trusted_shared_workerd` to
acknowledge this runtime profile. New scripts in `local-staging` also require
`X-Nanoflare-Customer-ID` because that namespace uses explicit customer IDs.

Namespace create/read/delete, script multipart upload/read/delete,
settings/bindings/tags/secret-name reads, settings PATCH, secret PUT/DELETE, bulk
secret PATCH and
tag PUT/DELETE are implemented. `Idempotency-Key` recovers an original upload or
mutation across retries, including after native traffic rollback. Settings, secret,
tag and content mutations
accept an optional `If-Match` revision; stale revisions return 412. Settings
PATCH preserves omitted fields and replaces supplied lists. Tag edits preserve
serving traffic; settings and secret edits publish the latest uploaded code.
Bulk secret merge patches apply all changes in one revision, preserve omitted
secrets and delete null entries. Only `secret_text` is supported; secret keys and
version tags are rejected.
Module content GET returns multipart source only; content PUT replaces code while
retaining settings, bindings, secrets and assets. Listing accepts a single
`tags=tag:yes` or `tag:no` filter. Tags support single-tag PUT and DELETE, with
eight tags per script. Asset sessions support verified base64 multipart uploads
and completion tokens in script upload metadata. Session initiation requires
control credentials and the trusted-profile header; file uploads use the returned
upload JWT. Uploaded assets require an explicit assets binding. Empty asset
files and content-type omission remain unsupported. Pinned JavaScript SDK flows
require explicit multipart adaptation. Wrangler 4.54.0 plain-module/namespace
flows pass with a local proxy adding the required execution-profile header;
broader CLI behavior remains unqualified. Tag-selected bulk deletion uses an
atomic metadata batch and reports configuration refresh failures explicitly;
retries select the current matching scripts. List pagination and compound
filters remain unsupported;
unsupported operations return an error. The existing native API remains available. This prototype does not claim
Cloudflare trust/cache equivalence, verified startup or drop-in SDK/Wrangler
compatibility. See the [step 9 record](../../docs/plans/workers-for-platforms-step-9-status.md).

Staged asset sessions expire independently of deployed assets. Cleanup runs at
startup and every minute, deleting at most 256 expired sessions per pass and
stopping before the repository closes. Deployed artifacts retain their asset
bytes after staging expiry and cleanup.
