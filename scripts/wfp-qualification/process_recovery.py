#!/usr/bin/env python3
"""Opt-in real process/storage failure drill using only disposable fixtures."""
import argparse
import base64
import concurrent.futures
import datetime
import hashlib
import http.client
import json
import os
import pathlib
import platform
import re
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[2]
LABEL = 'codex.task=wfp-step10-process'


def command(*args, timeout=20, env=None):
    result = subprocess.run(args, capture_output=True, text=True, timeout=timeout, env=env)
    if result.returncode:
        raise RuntimeError(f'{args[:3]} failed: {result.stderr.strip()}')
    return result.stdout.strip()


def free_port():
    with socket.socket() as server:
        server.bind(('127.0.0.1', 0))
        return server.getsockname()[1]


def wait_for(label, check, seconds=30):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if check():
            return
        time.sleep(.1)
    raise AssertionError(f'timed out: {label}')


class Drill:
    label = LABEL
    control_flags = ()

    def __init__(self, args):
        self.args = args
        self.temp = tempfile.TemporaryDirectory(prefix='nf-wfp-process-')
        self.directory = pathlib.Path(self.temp.name)
        self.names = []
        self.process = None
        self.logs = []
        self.token = ''
        self.base = f'http://127.0.0.1:{free_port()}'
        self.runtime_port = free_port()
        self.runner_port = free_port()
        self.pg_port = free_port()
        self.object_port = free_port()
        self.prefix = 'nf-wfp-process-' + uuid.uuid4().hex[:12]
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        self.report = {'at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                       'host_os': platform.system(), 'host_arch': platform.machine(), 'python': platform.python_version(),
                       'passed': False, 'observations': [], 'limitations': [
                           'single-host trusted fixture, not production sizing or an untrusted sandbox',
                           'client deadlines do not prove backend work is cancelled',
                           'brief server pauses exercise transport stalls; no packet loss or multi-host partition',
                           'no monitoring pipeline or per-invocation CPU qualification']}

    def request(self, method, path, body=None, auth=True, timeout=4):
        assert path.startswith('/')
        headers = {'Content-Type': 'application/json'}
        if auth and self.token:
            headers['Authorization'] = 'Bearer ' + self.token
        request = urllib.request.Request(self.base + path, method=method,
                                         headers=headers,
                                         data=None if body is None else json.dumps(body).encode())
        started = time.monotonic()
        try:
            with self.opener.open(request, timeout=timeout) as response:
                data = response.read()
                status = response.status
        except urllib.error.HTTPError as error:
            status, data = error.code, error.read()
        except (OSError, urllib.error.URLError, http.client.HTTPException) as error:
            return {'status': None, 'error': type(error).__name__, 'elapsed_ms': round((time.monotonic()-started)*1000)}
        try:
            value = json.loads(data)
        except (ValueError, UnicodeDecodeError):
            value = data.decode(errors='replace')
        return {'status': status, 'body': value, 'elapsed_ms': round((time.monotonic()-started)*1000)}

    def api(self, method, path, body=None, want=200):
        result = self.request(method, path, body)
        if result['status'] != want:
            # Responses contain only disposable fixture data, never user credentials.
            raise AssertionError(f'{method} {path}: {result}')
        return result.get('body')

    def observe(self, name, **values):
        self.report['observations'].append({'name': name, **values})
        print(name, flush=True)

    def image(self, tag):
        return command('docker', 'image', 'inspect', tag, '--format', '{{.Id}}')

    def container(self, suffix, image, options, arguments=()):
        name = self.prefix + '-' + suffix
        self.names.append(name)
        command('docker', 'run', '--pull=never', '-d', '--name', name, '--label', self.label,
                *options, image, *arguments)
        return name

    def sql(self, statement):
        return command('docker', 'exec', self.postgres, 'psql', '-U', 'postgres', '-d',
                       'postgres', '-A', '-t', '-v', 'ON_ERROR_STOP=1', '-c', statement)

    def start_control(self):
        assert self.process is None or self.process.poll() is not None
        log = open(self.directory / f'control-{len(self.logs)}.log', 'wb')
        self.logs.append(log)
        env = {'PATH': os.environ.get('PATH', '/usr/bin:/bin'), 'TZ': 'UTC',
               'DATABASE_URL': f'postgres://postgres:qualification@127.0.0.1:{self.pg_port}/postgres?sslmode=disable',
               'MINIO_ENDPOINT': f'127.0.0.1:{self.object_port}',
               'MINIO_ACCESS_KEY': 'qualification', 'MINIO_SECRET_KEY': 'qualification-disposable',
               'MINIO_BUCKET': 'qualification', 'MINIO_SECURE': 'false',
               'NANOFLARE_SECRET_KEY': '0123456789abcdef0123456789abcdef',
               'NANOFLARE_AUTH_SECRET': 'qualification-disposable-control',
               'NANOFLARE_TRAEFIK_TOKEN': 'qualification-disposable-traefik',
               'NANOFLARE_RUNNER_TOKEN': 'qualification-disposable-runner'}
        self.process = subprocess.Popen([
            str(self.args.control), '-addr=' + self.base.removeprefix('http://'),
            f'-runtime-addr=0.0.0.0:{self.runtime_port}',
            '-config-dir=' + str(self.directory / 'generated'),
            f'-runner-url=http://127.0.0.1:{self.runner_port}', '-enable-platform-dispatch',
            '-base-hostname=qualification.example.test', *self.control_flags],
            env=env, cwd=self.directory, stdout=log, stderr=log)
        def ready():
            if self.process.poll() is not None:
                log.flush()
                raise RuntimeError('disposable control process exited: ' + pathlib.Path(log.name).read_text()[-3000:])
            return self.request('GET', '/healthz', auth=False, timeout=.4)['status'] == 200
        wait_for('control process ready', ready)

    def kill_control(self):
        assert self.process is not None and self.process.poll() is None
        self.process.kill()
        self.process.wait(timeout=5)

    def verify(self, version):
        result = self.request('GET', self.dispatch)
        expected = {'version': version, 'saved': 'durable', 'asset': 'asset-' + version,
                    'secret': 'fixture-pinned-secret'}
        if result['status'] != 200 or any(result.get('body', {}).get(key) != value for key, value in expected.items()):
            raise AssertionError(f'dispatch snapshot mismatch: {result}')
        return result['body']

    def unavailable(self, name):
        result = self.request('GET', self.dispatch, timeout=2)
        if result['status'] == 200:
            raise AssertionError(f'{name}: unavailable authoritative service returned success')
        self.observe(name, status=result['status'], client_error=result.get('error'), elapsed_ms=result['elapsed_ms'])

    def start_stack(self):
        runner_image = self.image(self.args.image)
        postgres_image = self.image('postgres:17-alpine')
        object_image = self.image('minio/minio:RELEASE.2025-04-22T22-12-26Z')
        self.report.update({'runner_image_id': runner_image, 'postgres_image_id': postgres_image,
                            'object_store_image_id': object_image,
                            'control_sha256': hashlib.sha256(self.args.control.read_bytes()).hexdigest(),
                            'runner_sha256': hashlib.sha256(self.args.runner.read_bytes()).hexdigest()})
        self.postgres = self.container('postgres', postgres_image,
                                       ['--memory=256m', '--memory-swap=256m', '--pids-limit=128',
                                        '-p', f'127.0.0.1:{self.pg_port}:5432', '-e', 'POSTGRES_PASSWORD=qualification'])
        self.object_store = self.container('objects', object_image,
                                           ['--memory=256m', '--memory-swap=256m', '--pids-limit=128',
                                            '-p', f'127.0.0.1:{self.object_port}:9000',
                                            '-e', 'MINIO_ROOT_USER=qualification',
                                            '-e', 'MINIO_ROOT_PASSWORD=qualification-disposable'], ['server', '/data'])
        wait_for('PostgreSQL ready', lambda: self.pg_ready())
        def object_ready():
            try:
                with self.opener.open(f'http://127.0.0.1:{self.object_port}/minio/health/ready', timeout=.3) as response:
                    return response.status == 200
            except (OSError, urllib.error.URLError):
                return False
        self.object_ready = object_ready
        wait_for('object server ready', object_ready)
        self.runner = self.container('runner', runner_image,
                                     ['--init', '--memory=512m', '--memory-swap=512m', '--pids-limit=128',
                                      '--read-only', '--cap-drop=ALL', '--security-opt=no-new-privileges',
                                      '--user=65532:65532', '--tmpfs', '/tmp:rw,nosuid,nodev,size=64m,mode=1777',
                                      '--tmpfs', '/var/lib/nanoflare:rw,nosuid,nodev,size=64m,uid=65532,gid=65532',
                                      '--mount', f'type=bind,src={self.args.runner},dst=/qualification-runner,readonly',
                                      '--entrypoint=/qualification-runner', '--add-host=host.docker.internal:host-gateway',
                                      '-p', f'127.0.0.1:{self.runner_port}:8090',
                                      '-e', 'NANOFLARE_RUNNER_TOKEN=qualification-disposable-runner'],
                                     ['-addr=:8090', '-config-dir=/var/lib/nanoflare/generated',
                                      '-runtime-socket-dir=/tmp/sockets',
                                      f'-nanoflare-runtime-addr=host.docker.internal:{self.runtime_port}',
                                      '-workerd=' + self.args.workerd])
        wait_for('runner ready', lambda: self.runner_ready())
        limits = command('docker', 'exec', self.runner, 'sh', '-c',
                         'cat /sys/fs/cgroup/memory.max /sys/fs/cgroup/memory.swap.max /sys/fs/cgroup/pids.max')
        assert limits == '536870912\n0\n128', limits
        self.report['verified_runner_cgroup_limits'] = limits
        assert command('docker', 'exec', self.runner, 'id', '-u') == '65532'
        self.report['workerd'] = command('docker', 'exec', self.runner, self.args.workerd, '--version')
        self.report['workerd_sha256'] = command('docker', 'exec', self.runner, 'sha256sum', self.args.workerd).split()[0]
        self.start_control()

    def run(self):
        self.start_stack()
        session = self.api('POST', '/v1/setup/signup', {'email': 'fixture@example.test',
                           'password': 'qualification-disposable', 'organization_name': 'Qualification'}, want=201)
        self.token = session['access_token']
        org = session['active_org_id']
        assert re.fullmatch(r'[a-zA-Z0-9_-]+', org)
        self.sql(f"UPDATE organizations SET usage_level='paid' WHERE id='{org}'")
        base = '/v1/organizations/' + org
        ns = self.api('POST', base + '/dispatch-namespaces', {'name': 'qualification', 'customer_mode': 'platform'}, want=201)
        worker = self.api('POST', base + '/dispatch-namespaces/' + ns['id'] + '/scripts',
                          {'script_name': 'stable', 'execution': 'dynamic', 'customer_id': 'stable'}, want=201)
        kv = self.api('POST', base + '/kv-namespaces', {'name': 'durable'}, want=201)
        self.api('PUT', base + '/dispatch-namespaces/' + ns['id'] + '/grants',
                 {'expected_revision': 1, 'grants': [{'kind': 'kv', 'resource_id': kv['id'], 'customer_id': 'stable'}]})
        worker_base = base + '/workers/' + worker['id']
        self.api('PUT', worker_base + '/secrets/TOKEN', {'value': 'fixture-pinned-secret'}, want=204)
        def deployment(version, key=None):
            code = '''export default {async fetch(request,env){let saved=await env.STATE.get("proof");if(!saved){saved="durable";await env.STATE.put("proof",saved)}return Response.json({version:VERSION,saved,asset:await(await env.ASSETS.fetch("/proof.txt")).text(),secret:env.TOKEN})}}'''
            return {'idempotency_key': key or version, 'format': 'modules', 'compatibility_date': '2025-12-10',
                    'kv_namespaces': [{'binding': 'STATE', 'id': kv['id']}],
                    'assets': [{'path': 'proof.txt', 'data': base64.b64encode(('asset-' + version).encode()).decode()}],
                    'files': [{'path': 'worker.js', 'content': code.replace('VERSION', json.dumps(version))}]}
        first = self.api('POST', worker_base + '/deployments', deployment('v1'), want=201)
        platform = self.api('POST', base + '/workers', {'name': 'platform', 'hostname': 'qualification.example.test'}, want=201)
        self.api('POST', base + '/workers/' + platform['id'] + '/deployments',
                 {'format': 'modules', 'compatibility_date': '2025-12-10',
                  'dispatch_namespaces': [{'binding': 'CUSTOMERS', 'namespace_id': ns['id']}],
                  'files': [{'path': 'platform.js', 'content': '''export default {async fetch(request,env){try{return await env.CUSTOMERS.get("stable").fetch(request)}catch(error){return new Response(error.message,{status:error.status||500})}}}'''}]}, want=201)
        self.dispatch = '/internal/http/workers/' + platform['id'] + '/stable'
        self.verify('v1')
        second = self.api('POST', worker_base + '/deployments', deployment('v2'), want=201)
        self.verify('v2')
        self.observe('initial_real_process_pg_kv_minio_asset_secret_snapshot_verified')
        self.kill_control()
        self.start_control()
        self.verify('v2')
        self.observe('sigkill_control_restart_recovers_durable_active_revision')
        # The entire metadata server is killed, not just a client pool closed.
        command('docker', 'kill', '--signal=KILL', self.postgres)
        self.unavailable('postgres_server_outage_fails_closed')
        command('docker', 'start', self.postgres)
        wait_for('PostgreSQL restarted', self.pg_ready)
        self.verify('v2')
        self.observe('postgres_server_restart_recovers_existing_pool')
        command('docker', 'kill', '--signal=KILL', self.object_store)
        self.unavailable('object_server_outage_fails_closed_at_client')
        command('docker', 'start', self.object_store)
        wait_for('object server restarted', self.object_ready)
        self.verify('v2')
        self.observe('object_server_restart_preserves_pinned_assets')
        # A paused server stalls established transport, unlike connection refusal.
        command('docker', 'pause', self.object_store)
        try:
            self.unavailable('paused_object_transport_does_not_return_success')
        finally:
            command('docker', 'unpause', self.object_store)
        self.verify('v2')
        # Establish an exact durable building phase while S3 writes cannot finish.
        command('docker', 'pause', self.object_store)
        pending = deployment('v3', 'interrupted-building')
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as executor:
            future = executor.submit(self.request, 'POST', worker_base + '/deployments', pending, True, 8)
            try:
                wait_for('durable building publication', lambda: self.sql("SELECT payload->>'stage' FROM deployment_publications WHERE request_key='interrupted-building'") == 'building', seconds=5)
                self.kill_control()
            finally:
                command('docker', 'unpause', self.object_store)
            interrupted = future.result(timeout=10)
        assert interrupted['status'] != 201
        self.start_control()
        self.verify('v2')
        stage = self.sql("SELECT payload->>'stage' FROM deployment_publications WHERE request_key='interrupted-building'")
        assert stage == 'failed', stage
        retry = self.request('POST', worker_base + '/deployments', pending)
        assert retry['status'] == 409, retry
        self.verify('v2')
        self.observe('sigkill_during_durable_building_recovers_old_revision_and_conflict_retry', recovered_stage=stage, retry_status=retry['status'])
        # A fresh key can succeed after the failed operation without mixing bytes.
        self.api('POST', worker_base + '/deployments', deployment('v3', 'fresh-after-crash'), want=201)
        self.verify('v3')
        # Stored immutable bytes are durable, but the remote prepare cannot finish.
        # Recovery should complete this intent, unlike the incomplete build above.
        command('docker', 'pause', self.runner)
        stored_input = deployment('v4', 'interrupted-stored')
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as executor:
            future = executor.submit(self.request, 'POST', worker_base + '/deployments', stored_input, True, 8)
            try:
                wait_for('durable stored publication', lambda: self.sql("SELECT payload->>'stage' FROM deployment_publications WHERE request_key='interrupted-stored'") == 'stored', seconds=5)
                self.kill_control()
            finally:
                command('docker', 'unpause', self.runner)
            assert future.result(timeout=10)['status'] != 201
        self.start_control()
        self.verify('v4')
        stage = self.sql("SELECT payload->>'stage' FROM deployment_publications WHERE request_key='interrupted-stored'")
        assert stage == 'route_published', stage
        durable_id = self.sql("SELECT deployment_id FROM deployment_publications WHERE request_key='interrupted-stored'")
        resumed = self.api('POST', worker_base + '/deployments', stored_input, want=201)
        assert resumed['id'] == durable_id
        self.verify('v4')
        self.observe('sigkill_after_durable_artifact_recovers_new_revision_and_idempotent_identity', recovered_stage=stage)
        self.api('PUT', worker_base + '/deployment-traffic', {'deployments': [{'id': first['id'], 'traffic_percent': 100}]})
        self.verify('v1')
        self.kill_control()
        self.start_control()
        self.verify('v1')
        # Replay a previously successful key must not undo the persisted rollback.
        replay = self.api('POST', worker_base + '/deployments', deployment('v2'), want=201)
        assert replay['id'] == second['id']
        self.verify('v1')
        self.observe('rollback_and_successful_idempotent_retry_survive_process_restart')
        self.report['passed'] = True

    def pg_ready(self):
        try:
            return command('docker', 'exec', self.postgres, 'pg_isready', '-U', 'postgres') != ''
        except RuntimeError:
            return False

    def runner_ready(self):
        try:
            with self.opener.open(f'http://127.0.0.1:{self.runner_port}/healthz', timeout=.3) as response:
                return response.status == 200
        except (OSError, urllib.error.URLError):
            return False

    def close(self):
        errors = []
        if self.process is not None and self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=8)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)
        for name in reversed(self.names):
            try:
                command('docker', 'rm', '-f', '-v', name)
            except Exception as error:
                errors.append(str(error))
        for log in self.logs:
            log.close()
        if errors:
            self.report.update(passed=False, cleanup_errors=errors)
        self.report['cleanup_verified'] = not errors
        self.temp.cleanup()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--control', type=pathlib.Path, required=True, help='current host-native nanoflared binary')
    parser.add_argument('--runner', type=pathlib.Path, required=True, help='current Linux runner binary')
    parser.add_argument('--image', default='nanoflare-e2e/nanoflare-runner:local')
    parser.add_argument('--workerd', default='/usr/local/lib/node_modules/workerd/node_modules/@cloudflare/workerd-linux-arm64/bin/workerd')
    parser.add_argument('--report', type=pathlib.Path, required=True)
    args = parser.parse_args()
    args.control, args.runner = args.control.resolve(strict=True), args.runner.resolve(strict=True)
    drill = Drill(args)
    try:
        drill.run()
    except Exception as error:
        drill.report.update(passed=False, failure=str(error))
        raise
    finally:
        drill.close()
        args.report.write_text(json.dumps(drill.report, indent=2) + '\n')
    if not drill.report['passed']:
        raise SystemExit('qualification or cleanup failed')


if __name__ == '__main__':
    main()
