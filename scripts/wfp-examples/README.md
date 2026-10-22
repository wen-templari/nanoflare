# Unchanged upstream Workers for Platforms examples

Runs Cloudflare's Worker Publisher (Platform Starter Kit) and Internal Sites on
Nanoflare. VibeSDK is excluded. Application source and dependency lockfiles are
pinned to `cloudflare/templates@dfdf14099a81a08cfe1ffc78f8463605670ae513`.

This is the primary WFP application acceptance workflow. Focused Go regression
tests, [client compatibility checks](../wfp-client-flows/README.md) and
[qualification checks](../wfp-qualification/README.md) cover authorization,
protocol and failure cases beyond these applications.

This is a real local trusted environment: native workerd, PostgreSQL metadata,
SQLite for the D1 binding and MinIO for uploaded assets/artifacts. No Cloudflare
account or external Cloudflare API service is used. It is not a public untrusted
hosting deployment: existing CPU/failure-containment limitations still apply.

## Prepare and run

From the Nanoflare repository, with Go, Node 22+, npm, Git and Docker available:

```sh
node scripts/wfp-examples/prepare.mjs "$PWD/work/wfp-examples"
docker compose -f docker/compose.yml up -d postgres minio
docker exec docker-postgres-1 psql -U nanoflare -d nanoflare \
  -c 'CREATE SCHEMA IF NOT EXISTS upstream_examples;'
go build -o work/wfp-examples/nf-examples ./scripts/wfp-examples
export NANOFLARE_EXAMPLES_DATABASE_URL='postgres://nanoflare:nanoflare-development@127.0.0.1:5432/nanoflare?sslmode=disable&search_path=upstream_examples'
work/wfp-examples/nf-examples \
  --state "$PWD/work/wfp-examples/state" \
  --workerd "$PWD/work/wfp-examples/upstream/worker-publisher-template/node_modules/@cloudflare/workerd-darwin-arm64/bin/workerd" \
  --publisher "$PWD/work/wfp-examples/worker-publisher-template/index.js" \
  --sites "$PWD/work/wfp-examples/internal-sites-template/index.js"
```

The binary path above is for macOS Apple Silicon. Select the native workerd
package matching your machine; do not use the npm workerd wrapper. Source and
bundles remain in `work/`, outside Git. The preparation script invokes the
locked Wrangler directly for bundling, checks upstream source/lockfile diffs,
and writes its revision and output paths into `upstream.json`.

If the Compose project has a different name, use its PostgreSQL container name
for the schema-creation command. The fixture's development-only MinIO settings
match `docker/compose.yml`. It creates a separate `nanoflare-upstream-examples`
bucket and uses the selected PostgreSQL schema. It does not delete existing data.

Open:

- Publisher: <http://127.0.0.1:54337/>
- Internal Sites: <http://127.0.0.1:54338/>

Internal Sites signs in through the Nanoflare fixture with
`examples@nanoflare.local` / `local-examples-password`. These credentials and
fixed development encryption/session keys are deliberately local-only. All
listeners bind loopback. Do not expose this fixture through a public proxy.

Publisher accepts a name and Worker code, deploys via its original Cloudflare
SDK 5.2.0, then redirects to the deployed Worker. Sites accepts a folder or ZIP,
stores site/deployment records in SQLite, uploads assets through the original
three-step protocol, and serves them through namespace dispatch after login.

## Configuration-only differences

The runner supplies account/resource IDs, secrets, `READONLY=false`, namespace
bindings and a D1 binding at deployment time. `SITE_DOMAIN` uses the upstream
placeholder, enabling its path routing mode. Application bundles are produced
unchanged by upstream Wrangler; deployment uses Nanoflare's native complete
publication pipeline. This example does not claim that the full upstream
`npm run deploy` provisioning sequence is already supported by Nanoflare.

An explicitly installed private egress gateway routes only the Cloudflare API
origin and selected Nanoflare account to the local adapter. The runner enables
shared trusted execution as administrator configuration; no request consent
header is added. Normal Nanoflare authentication/scopes remain in force. It never connects to the Internet and rejects CONNECT tunneling.
The separate loopback CLI endpoint is `http://127.0.0.1:54339/client/v4`.

For Internal Sites, the local ingress verifies a signed Nanoflare session and
organization membership, strips caller identity/root headers, and forwards a
verified assertion. The runtime resolves that assertion through a private,
revision-capability-protected identity service to implement
`ctx.access.getIdentity()`. Unsigned identity values and foreign users fail.
This demonstrates the app's identity contract with Nanoflare authentication;
it does not implement Cloudflare's dashboard, Access policy API or identity
provider management. The upstream UI still uses its original Cloudflare wording.

## Verify

In another terminal:

```sh
node scripts/wfp-examples/verify.mjs \
  work/wfp-examples/state/credentials.json \
  work/wfp-examples/results.json
```

The fixture uses an HttpOnly, Secure, SameSite=None cookie so the upstream
sandboxed path previews can load authenticated CSS/JS. Chromium permits this on
loopback; browser mutation requests with a foreign or opaque Origin are rejected.
The sandbox remains unchanged.

The regression runs real publisher deployment/dispatch/update/delete and sites
login, deploy/update, HTML/CSS/empty-file serving, missing paths and deletion of
both Worker and database row. It also checks unauthenticated/spoofed requests and foreign/opaque mutation origins.
Regression script names are reserved for this fixture and cleaned up after a
successful run. `credentials.json` contains a short-lived local control token,
is written with mode 0600, and must not be committed or shared.

Manually verify browser forms with a folder and a ZIP. Stop and restart the same
command/state/schema, then reopen previously deployed URLs and `/admin` to
verify persistence. Every start refreshes the platform control credentials and
redeploys the platform bundles; customer artifacts and the SQLite site database
remain persisted. Ctrl-C stops listeners/runtime; data remains for another run.

Set `NANOFLARE_EXAMPLES_DEBUG=1` only when local shared-runtime diagnostics are
needed. The debug output is trusted-fixture output and may include app logs.

## Compatibility additions exercised

- SDK 5 filename-keyed multipart modules with bounded strict validation.
- Internal dispatch exports enabled for pre-2025-11-17 app dates.
- Missing dispatch targets produce the error prefix expected by upstream apps.
- Implicit asset serving and asset-first routing for dynamic Workers, while
  explicit worker-first routes preserve application handlers.
- Script/path-salted asset wire hashes plus independent full content digests.
- Empty files; up to 1,000 manifest files, 25 MiB/file and 50 MiB total.
- Verified runtime Access identity backed by Nanoflare sessions.

Namespace sessions remain bounded to 16 live staging records per organization;
completed records expire after one hour. This fixture is for local acceptance,
not a claim of complete Cloudflare API, quota or deployment-tool equivalence.

## Linux container qualification

The same workflow is qualified in a non-root Linux arm64 container with a
read-only root, dropped capabilities, no-new-privileges, 256 PIDs and a 2 GiB
memory bound. These are test bounds, not production sizing or per-customer
isolation guarantees. `node:24-slim` supplies the verification client and private
port forwards; workerd is `@cloudflare/workerd-linux-arm64@1.20260921.1`.

On Docker Desktop, prepare from the repository root (the schema/state are
separate from the macOS fixture):

```sh
node scripts/wfp-examples/prepare.mjs "$PWD/work/reproducible-examples"
mkdir -p work/linux-runtime work/example-state-linux
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o work/nf-wfp-examples-linux ./scripts/wfp-examples
npm pack @cloudflare/workerd-linux-arm64@1.20260921.1 --pack-destination work/linux-runtime
tar -xzf work/linux-runtime/cloudflare-workerd-linux-arm64-1.20260921.1.tgz -C work/linux-runtime
docker exec docker-postgres-1 psql -U nanoflare -d nanoflare \
  -c 'CREATE SCHEMA IF NOT EXISTS upstream_examples_linux;'
docker run --name nanoflare-upstream-examples-linux-browser \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  --pids-limit 256 --memory 2g --tmpfs /tmp:rw,size=128m \
  --user "$(id -u):$(id -g)" \
  -p 127.0.0.1:54357:54357 -p 127.0.0.1:54358:54358 \
  -e NANOFLARE_EXAMPLES_DATABASE_URL='postgres://nanoflare:nanoflare-development@host.docker.internal:5432/nanoflare?sslmode=disable&search_path=upstream_examples_linux' \
  -v "$PWD/work:/fixture:ro" -v "$PWD/work/example-state-linux:/state:rw" \
  -v "$PWD/scripts/wfp-examples:/scripts:ro" \
  node:24-slim node /scripts/linux-launch.cjs
```

Use `http://127.0.0.1:54357/` and `http://127.0.0.1:54358/` for browser checks.
The forwards are ordinary byte streams, with host ports bound to loopback.
No identity, API response, storage operation or application code is mocked.
From another terminal run:

```sh
docker exec -i -e NANOFLARE_EXAMPLES_PORT_BASE=54347 \
  nanoflare-upstream-examples-linux-browser \
  node --input-type=module - /state/credentials.json /tmp/results.json \
  < scripts/wfp-examples/verify.mjs
```

Uploaded browser examples can be reopened after stopping/starting the same
container/state. The qualification also verifies abrupt container termination
and recovery of already deployed content. Keep this distinct from a database
server crash, machine reboot, production soak, or untrusted-code qualification.
