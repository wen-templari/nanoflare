#!/usr/bin/env python3
"""Back up the owned pilot before replacing its daemon/runner image pins."""
import argparse
import hashlib
import json
import os
import pathlib
import ssl
import subprocess
import time
import urllib.request
import urllib.error

root = pathlib.Path(__file__).resolve().parent
private = root / 'private'
pins = {'PILOT_DAEMON_IMAGE': 'nanoflare-pilot/daemon:local',
        'PILOT_RUNNER_IMAGE': 'nanoflare-pilot/runner:local'}
services = ['nanoflared', 'nanoflare-runner']
report_path = root.parents[1] / 'scripts/wfp-qualification/results-cloudflare-stages-local-pilot-upgrade.json'
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=ssl._create_unverified_context()))

def run(*args):
    subprocess.run(args, check=True, stdout=subprocess.DEVNULL)

def pilot(*args):
    run(str(root / 'pilot.sh'), *args)

def write_private(path, data):
    temporary = path.with_name(path.name + '.upgrade-tmp')
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)

def check():
    with opener.open('http://127.0.0.1:18080/healthz', timeout=3) as response:
        assert response.status == 200
    state_path = private / 'recovery.json'
    if not state_path.exists():
        return
    state = json.loads(state_path.read_text())
    request = urllib.request.Request('http://127.0.0.1:18080/v1/organizations/' + state['org'] + '/workers', headers={'Authorization': 'Bearer ' + state['token']})
    with opener.open(request, timeout=5) as response:
        assert response.status == 200
    for hostname, expected in [('recovery.workers.pilot.localhost', b'pilot:retained-pilot-value')]:
        request = urllib.request.Request('https://127.0.0.1:18443/', headers={'Host': hostname})
        with opener.open(request, timeout=5) as response:
            assert response.read() == expected
    if 'd1_database' in state:
        request = urllib.request.Request('https://127.0.0.1:18443/', headers={'Host': 'd1.workers.pilot.localhost'})
        with opener.open(request, timeout=5) as response:
            assert json.load(response)['results'] == [{'id': 1, 'value': 'retained-sqlite-row'}]

def wait():
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        try:
            check()
            return
        except Exception:
            time.sleep(1)
    raise RuntimeError('Pilot health/retained fixture recovery deadline exceeded')

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    original = (private / 'pilot.env').read_bytes()
    lines = original.decode().splitlines(keepends=True)
    replacements = {key: subprocess.check_output(['docker', 'image', 'inspect', tag, '--format', '{{.Id}}'], text=True).strip() for key, tag in pins.items()}
    previous = {}
    updated = []
    for line in lines:
        key, separator, value = line.rstrip('\r\n').partition('=')
        if key in replacements:
            assert separator and key not in previous, 'Missing or duplicate image pin'
            previous[key] = value
            line = key + '=' + replacements[key] + '\n'
        updated.append(line)
    assert set(previous) == set(pins)
    config = json.loads((private / 'compose.lock.json').read_text())
    for service, key in zip(services, pins):
        assert config['services'][service]['image'] in ('${' + key + '}', '${' + key + ':?required}'), 'Unexpected pilot image binding'
    report = {'passed': False, 'scope': 'owned local trusted pilot image upgrade; not production or cross-version database migration qualification', 'old_images': previous, 'new_images': replacements}
    if not args.apply:
        print(json.dumps(report, indent=2))
        return
    try:
        check()
    except urllib.error.HTTPError as error:
        if error.code != 401:
            raise
        state = json.loads((private / 'recovery.json').read_text())
        body = json.dumps({'email': 'pilot-recovery@example.test', 'password': state['password']}).encode()
        request = urllib.request.Request('http://127.0.0.1:18080/v1/auth/login', data=body, headers={'Content-Type': 'application/json'}, method='POST')
        with opener.open(request, timeout=10) as response:
            session = json.load(response)
        assert session['active_org_id'] == state['org']
        state['token'] = session['access_token']
        write_private(private / 'recovery.json', (json.dumps(state) + '\n').encode())
        report['fixture_session_refreshed_before_upgrade'] = True
    wait()
    backup = private / ('upgrade-backup-' + str(time.time_ns()))
    backup.mkdir(mode=0o700)
    report['private_backup_directory'] = str(backup)
    stores = ['postgres-data', 'minio-data', 'nanoflared-data', 'nanoflare-runner-data']
    for name in ['pilot.env', 'compose.lock.json', 'recovery.json', 'metrics-token']:
        source = private / name
        if source.exists():
            write_private(backup / name, source.read_bytes())
    archives = {}
    try:
        try:
            pilot('stop')
            for store in stores:
                run('docker', 'run', '--rm', '--network', 'none', '--entrypoint', 'tar', '--mount', 'type=volume,src=nanoflare-pilot_' + store + ',dst=/source,readonly', '--mount', 'type=bind,src=' + str(backup) + ',dst=/backup', config['services']['postgres']['image'], '-C', '/source', '-cpf', '/backup/' + store + '.tar', '.')
                archive = backup / (store + '.tar')
                os.chmod(archive, 0o600)
                with archive.open('rb') as stream:
                    archives[store] = {'sha256': hashlib.file_digest(stream, 'sha256').hexdigest(), 'bytes': archive.stat().st_size}
        finally:
            pilot('start')
        assert len(archives) == len(stores)
        write_private(backup / 'manifest.json', (json.dumps(archives, indent=2) + '\n').encode())
        report['cold_backup_stores'] = archives
        write_private(private / 'pilot.env', ''.join(updated).encode())
        try:
            pilot('up', '-d', '--no-deps', '--pull', 'never', *services)
            wait()
            for service, key in zip(services, pins):
                actual = subprocess.check_output(['docker', 'inspect', 'nanoflare-pilot-' + service + '-1', '--format', '{{.Image}}'], text=True).strip()
                assert actual == replacements[key], 'Pilot did not start pinned image'
            report.update(passed=True, retained_login_dispatch_secret_and_d1_verified=True, credentials_preserved=True)
        except Exception:
            write_private(private / 'pilot.env', original)
            pilot('up', '-d', '--no-deps', '--pull', 'never', *services)
            wait()
            report['old_images_restored'] = True
            raise
    finally:
        report_path.write_text(json.dumps(report, indent=2) + '\n')
        print(json.dumps(report, indent=2))

if __name__ == '__main__':
    main()
