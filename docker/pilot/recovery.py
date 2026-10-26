#!/usr/bin/env python3
"""Retained trusted dispatch fixture and bounded restart recovery check."""
import json, pathlib, secrets, subprocess, time, urllib.request, urllib.error, ssl
root = pathlib.Path(__file__).resolve().parent
state_path = root / 'private/recovery.json'
base = 'http://127.0.0.1:18080'
token = ''
# Explicit loopback client: never send pilot credentials through a system proxy.
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=ssl._create_unverified_context()))
def api(method, path, body=None):
    headers = {'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token}
    req = urllib.request.Request(base + path, data=None if body is None else json.dumps(body).encode(), headers=headers, method=method)
    with opener.open(req, timeout=20) as response:
        data = response.read()
        return json.loads(data) if data else None

def wait(check):
    end = time.monotonic() + 60
    last = None
    while time.monotonic() < end:
        try:
            result = check()
            if result: return result
        except Exception as error: last = error
        time.sleep(1)
    raise RuntimeError('Recovery deadline exceeded') from last

wait(lambda: api('GET', '/healthz'))
if state_path.exists():
    state = json.loads(state_path.read_text())
else:
    password = secrets.token_hex(24)
    session = api('POST', '/v1/setup/signup', {'email': 'pilot-recovery@example.test', 'password': password, 'organization_name': 'Local trusted pilot'})
    state = {'token': session['access_token'], 'org': session['active_org_id'], 'password': password}
    state_path.touch(mode=0o600)
    state_path.write_text(json.dumps(state))
token = state['token']; org = state['org']
assert org.replace('-', '').replace('_', '').isalnum()
subprocess.run([str(root / 'pilot.sh'), 'exec', '-T', 'postgres', 'psql', '-U', 'nanoflare', '-d', 'nanoflare', '-c', "UPDATE organizations SET usage_level='paid' WHERE id='" + org + "'"], check=True, stdout=subprocess.DEVNULL)
prefix = '/v1/organizations/' + org
if 'platform' not in state:
    ns = api('POST', prefix + '/dispatch-namespaces', {'name': 'pilot-recovery', 'customer_mode': 'platform'})
    worker = api('POST', prefix + '/dispatch-namespaces/' + ns['id'] + '/scripts', {'script_name': 'echo', 'execution': 'dynamic', 'customer_id': 'pilot-owned'})
    api('PUT', prefix + '/workers/' + worker['id'] + '/secrets/PROBE', {'value': 'retained-pilot-value'})
    api('POST', prefix + '/workers/' + worker['id'] + '/deployments', {'format': 'modules', 'compatibility_date': '2026-09-11', 'files': [{'path': 'worker.js', 'content': 'export default {fetch(r,env){return new Response("pilot:"+env.PROBE)}}'}]})
    platform = api('POST', prefix + '/workers', {'name': 'pilot-recovery', 'hostname': 'recovery.workers.pilot.localhost'})
    api('POST', prefix + '/workers/' + platform['id'] + '/deployments', {'format': 'modules', 'compatibility_date': '2026-09-11', 'dispatch_namespaces': [{'binding': 'CUSTOMERS', 'namespace_id': ns['id']}], 'files': [{'path': 'worker.js', 'content': 'export default {fetch(r,env){return env.CUSTOMERS.get("echo").fetch(r)}}'}]})
    state['platform'] = platform['id']; state_path.write_text(json.dumps(state))
def check():
    req = urllib.request.Request('https://127.0.0.1:18443/', headers={'Host': 'recovery.workers.pilot.localhost'})
    with opener.open(req, timeout=5) as response:
        assert response.read() == b'pilot:retained-pilot-value'
    return True
wait(check)
started = time.monotonic()
subprocess.run([str(root / 'pilot.sh'), 'restart', 'nanoflared', 'nanoflare-runner'], check=True)
wait(check)
# Verify the same signed login token remains valid after daemon recreation/restart.
api('GET', prefix + '/workers')
report = {'passed': True, 'dispatch_and_encrypted_secret_survived_restart': True, 'existing_login_token_survived_restart': True, 'recovery_seconds': round(time.monotonic() - started, 2), 'scope': 'persistent local trusted pilot; daemon and runner restart; not backup restore or production qualification'}
(root.parents[1] / 'scripts/wfp-qualification/results-cloudflare-stages-local-pilot-worker-recovery.json').write_text(json.dumps(report, indent=2) + '\n')
print(json.dumps(report))
