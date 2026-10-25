#!/usr/bin/env python3
"""Retain a trusted pilot Wasm deployment and verify update/rollback/restart."""
import base64
import hashlib
import json
import os
import pathlib
import ssl
import subprocess
import time
import urllib.request

root = pathlib.Path(__file__).resolve().parent
account = json.loads((root / 'private/recovery.json').read_text())
state_path = root / 'private/wasm.json'
state = json.loads(state_path.read_text()) if state_path.exists() else {}
new_uploads = int('first' not in state) + int('second' not in state)
prefix = '/v1/organizations/' + account['org']
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=ssl._create_unverified_context()))

def save():
    fd = os.open(state_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, 'w') as stream:
        json.dump(state, stream)

def api(method, path, body=None):
    request = urllib.request.Request('http://127.0.0.1:18080' + path, data=None if body is None else json.dumps(body).encode(), headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + account['token']}, method=method)
    with opener.open(request, timeout=30) as response:
        data = response.read()
        return json.loads(data) if data else None

def wasm(answer):
    return bytes.fromhex('0061736d010000000105016000017f030201000707010372756e00000a0601040041' + format(answer, '02x') + '0b00040178fffe')

code = 'import module from "./answer.wasm";export default {fetch(r,env){return new Response("pilot-wasm:"+env.PROBE+":"+new WebAssembly.Instance(module).exports.run())}}'

def deploy(answer, key):
    result = api('POST', prefix + '/workers/' + state['customer'] + '/deployments', {'idempotency_key': key, 'format': 'modules', 'entrypoint': 'worker.js', 'compatibility_date': '2026-09-11', 'files': [{'path': 'worker.js', 'content': code}, {'path': 'answer.wasm', 'wasm': base64.b64encode(wasm(answer)).decode()}]})
    return result['id']

def verify(answer):
    request = urllib.request.Request('https://127.0.0.1:18443/', headers={'Host': 'wasm.workers.pilot.localhost'})
    with opener.open(request, timeout=5) as response:
        assert response.read() == ('pilot-wasm:retained-wasm-value:' + str(answer)).encode()
    files = api('GET', prefix + '/workers/' + state['customer'] + '/files')
    binary = next(file for file in files if file['path'] == 'answer.wasm')
    assert base64.b64decode(binary['wasm']) == wasm(answer)
    assert binary['size'] == len(wasm(answer)) and not binary['content']
    assert next(file['content'] for file in files if file['path'] == 'worker.js') == code

def wait(check):
    end = time.monotonic() + 90
    while time.monotonic() < end:
        try:
            check()
            return
        except Exception:
            time.sleep(1)
    raise RuntimeError('Wasm pilot recovery deadline exceeded')

def traffic(deployment):
    api('PUT', prefix + '/workers/' + state['customer'] + '/deployment-traffic', {'deployments': [{'id': deployment, 'traffic_percent': 100}]})

if 'namespace' not in state:
    state['namespace'] = api('POST', prefix + '/dispatch-namespaces', {'name': 'pilot-wasm', 'customer_mode': 'platform'})['id']
    save()
if 'customer' not in state:
    state['customer'] = api('POST', prefix + '/dispatch-namespaces/' + state['namespace'] + '/scripts', {'script_name': 'answer', 'execution': 'dynamic', 'customer_id': 'pilot-owned'})['id']
    save()
if not state.get('secret_prepared'):
    api('PUT', prefix + '/workers/' + state['customer'] + '/secrets/PROBE', {'value': 'retained-wasm-value'})
    state['secret_prepared'] = True
    save()
if 'first' not in state:
    state['first'] = deploy(42, 'pilot-wasm-first-v1')
    save()
if 'platform' not in state:
    state['platform'] = api('POST', prefix + '/workers', {'name': 'pilot-wasm', 'hostname': 'wasm.workers.pilot.localhost'})['id']
    save()
if not state.get('platform_deployed'):
    api('POST', prefix + '/workers/' + state['platform'] + '/deployments', {'idempotency_key': 'pilot-wasm-dispatcher-v1', 'format': 'modules', 'compatibility_date': '2026-09-11', 'dispatch_namespaces': [{'binding': 'CUSTOMERS', 'namespace_id': state['namespace']}], 'files': [{'path': 'worker.js', 'content': 'export default {fetch(r,env){return env.CUSTOMERS.get("answer").fetch(r)}}'}]})
    state['platform_deployed'] = True
    save()
traffic(state['first'])
wait(lambda: verify(42))
if 'second' not in state:
    state['second'] = deploy(7, 'pilot-wasm-second-v1')
    save()
else:
    traffic(state['second'])
wait(lambda: verify(7))
traffic(state['first'])
wait(lambda: verify(42))
started = time.monotonic()
try:
    subprocess.run([str(root / 'pilot.sh'), 'stop', 'nanoflared', 'nanoflare-runner'], check=True, stdout=subprocess.DEVNULL)
    subprocess.run([str(root / 'pilot.sh'), 'restart', 'postgres', 'minio'], check=True, stdout=subprocess.DEVNULL)
    end = time.monotonic() + 60
    while time.monotonic() < end:
        health = subprocess.check_output(['docker', 'inspect', 'nanoflare-pilot-postgres-1', '--format', '{{.State.Health.Status}}'], text=True).strip()
        if health == 'healthy':
            break
        time.sleep(1)
    else:
        raise RuntimeError('Pilot PostgreSQL did not recover')
finally:
    subprocess.run([str(root / 'pilot.sh'), 'start', 'nanoflare-runner', 'nanoflared'], check=True, stdout=subprocess.DEVNULL)
wait(lambda: verify(42))
report = {'passed': True, 'scope': 'trusted persistent local Docker pilot; native deployment API and real dynamic dispatch', 'binary_variants_42_and_7_served': True, 'new_binary_revision_uploads': new_uploads, 'traffic_rollback_to_42': True, 'javascript_unchanged': True, 'exact_non_utf8_binary_bytes_preserved': True, 'encrypted_secret_survived': True, 'postgres_minio_daemon_runner_restart_passed': True, 'recovery_seconds': round(time.monotonic() - started, 2), 'wasm_sha256': {str(answer): hashlib.sha256(wasm(answer)).hexdigest() for answer in [42, 7]}, 'production_qualified': False, 'untrusted_execution_qualified': False, 'limits': ['Not a cold restore of this Wasm fixture, crash recovery or cross-version database migration test.', 'Native deployment API; pinned SDK/Wrangler workflows are qualified separately.', 'Retained fixture remains available for subsequent recovery checks.']}
(root.parents[1] / 'scripts/wfp-qualification/results-cloudflare-stages-local-pilot-wasm.json').write_text(json.dumps(report, indent=2) + '\n')
print(json.dumps(report, indent=2))
