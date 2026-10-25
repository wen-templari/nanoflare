#!/usr/bin/env python3
"""Initialize local pilot secrets once; pin already-built images by ID."""
import json, os, pathlib, secrets, subprocess
root = pathlib.Path(__file__).resolve().parent
private = root / 'private'
private.mkdir(mode=0o700, exist_ok=True)
os.chmod(private, 0o700)
env = private / 'pilot.env'
if env.exists():
    raise SystemExit('Pilot already initialized; preserve its credentials and image pins.')
values = {key: secrets.token_hex(32) for key in ('PILOT_AUTH_SECRET', 'PILOT_PASSWORD', 'PILOT_SECRET_KEY', 'PILOT_METRICS_TOKEN', 'PILOT_TRAEFIK_TOKEN', 'PILOT_RUNNER_TOKEN', 'PILOT_GRAFANA_PASSWORD')}
for role in ('DAEMON', 'RUNNER'):
    values['PILOT_' + role + '_IMAGE'] = subprocess.check_output(['docker', 'image', 'inspect', 'nanoflare-pilot/' + role.lower() + ':local', '--format', '{{.Id}}'], text=True).strip()
# Pin every dependency too, so restart never silently changes a service image.
config = json.loads((root / 'compose.json').read_text())
for service in config['services'].values():
    if not service['image'].startswith('${'):
        service['image'] = subprocess.check_output(['docker', 'image', 'inspect', service['image'], '--format', '{{.Id}}'], text=True).strip()
(private / 'compose.lock.json').write_text(json.dumps(config, indent=2) + '\n')
os.chmod(private / 'compose.lock.json', 0o600)
# Compose resolves bind mounts relative to the first configuration file.
# Keep the lock in private, but explicitly use --project-directory docker/pilot.
fd = os.open(env, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, 'w') as f:
    f.write(''.join(k + '=' + v + '\n' for k, v in values.items()))
fd = os.open(private / 'metrics-token', os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o644)
with os.fdopen(fd, 'w') as f:
    f.write(values['PILOT_METRICS_TOKEN'] + '\n')
print('Initialized private pilot configuration; credentials were not printed.')
