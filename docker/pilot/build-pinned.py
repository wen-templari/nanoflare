#!/usr/bin/env python3
"""Offline Go rebuild over the pilot's existing pinned runtime image layers."""
import argparse
import hashlib
import json
import pathlib
import subprocess
import tempfile

root = pathlib.Path(__file__).resolve().parents[2]
pilot = root / 'docker/pilot'

def output(*args):
    return subprocess.check_output(args, text=True).strip()

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--go-image', default='golang:1.25-bookworm', help='already-present local Go builder image')
    args = parser.parse_args()
    values = dict(line.split('=', 1) for line in (pilot / 'private/pilot.env').read_text().splitlines() if '=' in line)
    bases = {'nanoflared': values['PILOT_DAEMON_IMAGE'], 'nanoflare-runner': values['PILOT_RUNNER_IMAGE']}
    assert all(value.startswith('sha256:') for value in bases.values()), 'Existing immutable image pins required'
    architectures = {output('docker', 'image', 'inspect', value, '--format', '{{.Architecture}}') for value in bases.values()}
    assert len(architectures) == 1
    builder = output('docker', 'image', 'inspect', args.go_image, '--format', '{{.Id}}')
    modules = output('go', 'env', 'GOMODCACHE')
    revision = output('git', '-C', str(root), 'rev-parse', 'HEAD')
    report = {'scope': 'offline current Go binaries over existing immutable pilot runtime layers', 'source_commit': revision, 'builder_image': builder, 'base_images': bases, 'architecture': next(iter(architectures)), 'new_images': {}, 'binary_sha256': {}}
    with tempfile.TemporaryDirectory(prefix='nf-pilot-build-') as work:
        work = pathlib.Path(work)
        (work / 'out').mkdir()
        (work / 'cache').mkdir()
        report['go_version'] = output('docker', 'run', '--rm', '--pull', 'never', '--network', 'none', builder, 'go', 'version')
        subprocess.run(['docker', 'run', '--rm', '--pull', 'never', '--network', 'none', '--memory', '2g', '--pids-limit', '256', '-e', 'CGO_ENABLED=0', '-e', 'GOOS=linux', '-e', 'GOARCH=' + report['architecture'], '-e', 'GOMAXPROCS=2', '-e', 'GOTOOLCHAIN=local', '-e', 'GOPROXY=off', '-e', 'GOCACHE=/cache', '-v', str(root) + ':/src:ro', '-v', modules + ':/go/pkg/mod:ro', '-v', str(work / 'out') + ':/out', '-v', str(work / 'cache') + ':/cache', '-w', '/src', builder, 'go', 'build', '-p', '1', '-trimpath', '-o', '/out/', './cmd/nanoflared', './cmd/nanoflare-runner'], check=True)
        for binary, base in bases.items():
            role = 'daemon' if binary == 'nanoflared' else 'runner'
            container = output('docker', 'create', '--network', 'none', base)
            try:
                subprocess.run(['docker', 'cp', str(work / 'out' / binary), container + ':/usr/local/bin/' + binary], check=True)
                report['new_images'][role] = output('docker', 'commit', '--change', 'LABEL org.opencontainers.image.revision=' + revision, '--change', 'LABEL dev.nanoflare.pilot.base=' + base, container, 'nanoflare-pilot/' + role + ':local')
                report['binary_sha256'][binary] = hashlib.sha256((work / 'out' / binary).read_bytes()).hexdigest()
            finally:
                subprocess.run(['docker', 'rm', '-v', container], check=True, stdout=subprocess.DEVNULL)
    (root / 'scripts/wfp-qualification/results-cloudflare-stages-local-pilot-pinned-build.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report, indent=2))

if __name__ == '__main__':
    main()
