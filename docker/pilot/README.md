# Persistent local trusted pilot

This is a separate local deployment for stage 5 recovery and operational checks.
It is not production qualification or an untrusted execution environment. Only
trusted Workers may be deployed. The API has no configured external OIDC login;
keep every published port on loopback.

From the repository root, build current production images with
`docker/pilot/build.sh`, then initialize once with
`python3 docker/pilot/prepare.py`. Initialization generates credentials in the
ignored, mode-0700 `private/` directory and pins every image by local image ID.
Back up this directory securely: the encryption key is needed to recover secrets.
Never commit or paste the environment file or resolved Compose configuration.

Use `docker/pilot/pilot.sh up -d --pull never`, then
`docker/pilot/pilot.sh ps`. The API health endpoint is
<http://127.0.0.1:18080/healthz>, Worker ingress is port 18088 (use the Worker
hostname under `workers.pilot.localhost`), Grafana is port 13000, and Prometheus
is port 19090. Grafana's admin password is in the private environment file.
Postgres, MinIO, runtime, tracing and log collection have no host ports.
The log collector receives runtime socket logs only, without Docker socket access.

All volumes and the network belong to the `nanoflare-pilot` Compose project.
`docker/pilot/pilot.sh stop` and `start` preserve state. `down` also preserves
volumes; **do not use `down -v`** unless intentionally destroying this pilot.
Do not change the project name: that would select different storage.

Before calling stage 5 qualified, retain results for a real deployment surviving
runner and daemon restarts; backup and restore of PostgreSQL, MinIO, generated
configuration and runner SQLite storage; an isolated OOM recovery drill; alert
notification delivery; sustained load; revision growth and retention. A restart
alone does not prove backup recovery. Snapshot related stores while writes are
stopped to avoid an inconsistent recovery set. Image changes require an explicit
new lock and recovery-compatible migrations; preserve the old lock and backups.

Run `python3 docker/pilot/recovery.py` for the retained trusted dispatch fixture.
It creates a pilot-owned organization, marks that fixture organization paid in
the local database, deploys a platform Worker and dynamic customer Worker with an
encrypted secret, then restarts only the pilot daemon and runner. The private
fixture account state remains under `private/`; the report contains no credentials.
Worker ingress uses HTTPS on **18443**. The generated routes require TLS;
HTTP port 18088 alone does not serve them. The loopback-only recovery client
accepts Traefik's local self-signed certificate and bypasses system proxies.
Do not reuse that certificate policy for remote deployment. The Cloudflare
adapter is explicitly enabled, and login signing uses a generated private key.

For a cold snapshot and isolated restore drill, run
`python3 docker/pilot/restore.py`. This briefly stops all **pilot** services to
capture consistent PostgreSQL, MinIO, generated configuration and runner volumes,
then restarts the original pilot even if snapshotting fails. Private credentials,
image pins, fixture identity, archive sizes and SHA-256 hashes are retained in a
unique `private/backup-*` directory. Treat these archives as sensitive data.
Monitoring history is not backed up by this drill.

The drill refuses existing `nanoflare-pilot-restore` volumes. It restores into
fresh volumes and a separate network, with host ports shifted by 10000: API
28080, HTTPS Worker ingress 28443. It checks the restored dispatch response,
encrypted secret and retained login token, then checks the original pilot too.
The restore stack remains available for inspection. Do not run another drill
until intentionally retiring that recovery project. This cold physical restore
requires the same database/runtime images and original signing/encryption keys;
it does not establish cross-version or off-host recovery, or recovery of a
populated D1 database.

Populated D1 recovery is now covered by `python3 docker/pilot/d1.py`. It creates
a trusted Worker with a D1 binding and writes a known row through the Worker.
Subsequent restores automatically read that row through both source and restored
Workers. SQLite files reside in the daemon's generated-data volume (`db/`), not
just the runner volume. The check uses GET for verification, so it cannot
repopulate a missing row during restore verification.

The recorded second recovery project was created with
`PILOT_RESTORE_PROJECT=nanoflare-pilot-restore-d1 python3 docker/pilot/restore.py`.
That project uses ports shifted by 20000 (API 38080, Worker HTTPS 38443), preserving
the first recovery stack for comparison. Both destination projects are retained.
The earlier populated-D1 limitation is superseded for this one-row fixture;
concurrent writes, point-in-time recovery and cross-version recovery remain open.

`python3 docker/pilot/load.py` runs a two-minute read soak at a target 10 requests
per second across four lanes, split evenly between trusted dispatch and populated
D1 reads. It validates exact content, records errors without retries, and retains
latency summaries under `scripts/wfp-qualification/`. Seed the retained fixtures
with `recovery.py` and `d1.py` first. This is a bounded local reliability check,
not a production throughput limit or a long-running/write-heavy soak.

Container logs use Docker's `local` driver with rotation (`max-size=10m`,
`max-file=3`) for each service. Prometheus has explicit one-week and 512 MiB
block retention; its WAL and head storage remain additional usage. Loki keeps
its configured seven-day retention and Tempo one-day retention. Run
`python3 docker/pilot/retention.py` to inspect active Docker settings and
Prometheus flags and check both retained Worker responses. Active configuration
has been verified; time-based deletion has not been accelerated or qualified.
Deployment revision artifacts, private backup directories and database/object
storage still need separate policies and disk-pressure alerts. These settings
are not a total disk quota. Existing recovery projects retain the configurations
from their snapshots and are not changed by pilot configuration updates.

The pilot now includes Alertmanager and a durable local webhook receiver (13
services total). Prometheus routes its existing resource rules and a real
control-scrape outage rule through Alertmanager. Neither new service publishes a
host port. The receiver stores only alert names, status and receive time, with
one 1 MiB previous file; it forwards nothing externally. Run
`python3 docker/pilot/alert-check.py` to stop the control daemon, observe firing,
restart it and observe resolution. The drill always attempts daemon restart.
If daemon-only recovery fails, it records that failure and restarts the runner
and daemon to recover service. This is explicitly separate from alert delivery.

The initial run delivered firing in 30.19 seconds. Daemon-only restart failed
with `runner session expired`; manual runner and daemon restart restored service,
and resolved notification arrived at 150.5 seconds total. This recovery defect
remains open. Local webhook delivery does not qualify operator/on-call routing.

The daemon-only recovery defect is fixed: preparation now retries opening a
session if an earlier failed reconnect retained the broker with no live session.
The updated runner passed the same outage drill without runner restart; firing
arrived at 32.25 seconds and resolved at 47.28 seconds total. Both retained Worker
responses, including the populated D1 row, passed afterward. The original failure
report is preserved separately as `*-alert-delivery-before-fix.json`.

For a current Go rebuild when registry metadata is unavailable, run
`python3 docker/pilot/build-pinned.py`. This uses an already-present local Go
image with networking disabled and keeps the existing pinned runtime layers,
including workerd and Litestream. It changes only the two Go binaries and records
base/builder/new image IDs and binary hashes. It does not refresh dependencies or
replace the standard production Docker build. The private pilot directory is
excluded from ordinary Docker build contexts.

After building, `python3 docker/pilot/upgrade.py` shows only proposed image pins.
Use `python3 docker/pilot/upgrade.py --apply` to cold-snapshot the four authoritative
stores, retain private credentials and previous pins, then replace the daemon and
runner images. The drill checks the retained login, dispatch secret and populated
D1 row. If the fixture login has expired, it renews that fixture session before
snapshotting; signing/encryption/database credentials remain intact. A failed
image startup attempts to restore the previous pins and existing service. The
private backup remains available; this is not a general cross-version database
migration or automatic physical-data rollback tool.

`python3 docker/pilot/wasm.py` retains a separate trusted dynamic-dispatch fixture.
Its first run uploads non-UTF-8 Wasm returning 42, updates only the binary to 7,
then rolls traffic back to 42. It checks exact stored bytes and an encrypted
secret through serving and restarts of PostgreSQL, MinIO, daemon and runner.
Later runs replay the same retained revisions; reports distinguish revision
uploads from replay. Temporary service stops always attempt control/runner
restart. This does not cold-restore the Wasm fixture or qualify production or
untrusted execution.

The current Wasm fixture is included in cold snapshots and restore verification.
Run `PILOT_RESTORE_PROJECT=nanoflare-pilot-restore-wasm python3 docker/pilot/restore.py`
to restore into fresh volumes with ports shifted by 30000 (API 48080, Worker
HTTPS 48443). `PILOT_RESTORE_PORT_OFFSET` can select another valid offset.
Each project now gets its own private Compose lock, so a new drill does not
replace the configuration file for an earlier recovery project.

The Wasm restore checks exact binary bytes, the retained secret and both revision
responses (42 and 7), then leaves restored traffic at 42. The recorded drill also
verifies separate source/restored data volumes and networks, and source response
42 while the restored revision serves 7. Dispatch, login and populated D1 recovery
checks remain included. This closes the previous cold-restore gap for this local
Wasm fixture; point-in-time, cross-version and off-host recovery remain unqualified.
