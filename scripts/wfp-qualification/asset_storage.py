#!/usr/bin/env python3
"""Bounded adapter asset upload/publication/load against disposable PG, MinIO and native runner."""
import argparse
import base64
import concurrent.futures
import hashlib
import json
import pathlib
import re
import time
import urllib.error
import urllib.request
import uuid

from process_recovery import Drill, command, wait_for


def multipart(parts):
    boundary = 'nf-asset-' + uuid.uuid4().hex
    body = bytearray()
    for name, data, content_type in parts:
        body.extend(('--' + boundary + '\r\nContent-Disposition: form-data; name="' + name + '"; filename="' + name + '"\r\n').encode())
        if content_type:
            body.extend(('Content-Type: ' + content_type + '\r\n').encode())
        body.extend(b'\r\n' + data + b'\r\n')
    body.extend(('--' + boundary + '--\r\n').encode())
    return 'multipart/form-data; boundary=' + boundary, bytes(body)


class AssetDrill(Drill):
    label = 'codex.task=wfp-assets-storage'
    control_flags = ('-enable-cloudflare-compatibility',)

    def cf(self, method, path, body=None, token=None, parts=None, key=None, want=200, customer=None):
        headers = {'Authorization': 'Bearer ' + (token or self.token)}
        if parts is not None:
            headers['Content-Type'], data = multipart(parts)
        else:
            headers['Content-Type'] = 'application/json'
            data = None if body is None else json.dumps(body).encode()
        if key:
            headers['Idempotency-Key'] = key
        if customer:
            headers['X-Nanoflare-Customer-ID'] = customer
        request = urllib.request.Request(self.base + path, data=data, headers=headers, method=method)
        try:
            with self.opener.open(request, timeout=20) as response:
                status, result = response.status, json.load(response)
        except urllib.error.HTTPError as error:
            status, result = error.code, json.load(error)
        assert status == want, (method, path, status, result)
        return result['result']

    def run(self):
        self.report['limitations'] = [
            'four concurrent customer sessions, 1 MiB distinct content each and two paths per manifest; bounded correctness/load fixture, not production soak',
            '512 MiB native runner with 128 task limit; host control process has no cgroup memory bound',
            'real PostgreSQL, MinIO and native workerd; no Cloudflare account, hosted portability or untrusted/CPU claims',
            'same-session duplicate hash grouping only; cross-session deduplication remains unsupported']
        self.report['fixture_sources'] = {
            name: hashlib.sha256(pathlib.Path(__file__).with_name(name).read_bytes()).hexdigest()
            for name in ['asset_storage.py', 'process_recovery.py']}
        self.start_stack()
        session = self.api('POST', '/v1/setup/signup', {'email': 'assets@example.test', 'password': 'qualification-disposable', 'organization_name': 'Assets'}, want=201)
        self.token, org = session['access_token'], session['active_org_id']
        assert re.fullmatch(r'[a-zA-Z0-9_-]+', org)
        self.sql(f"UPDATE organizations SET usage_level='paid' WHERE id='{org}'")
        base = '/v1/organizations/' + org
        ns = self.api('POST', base + '/dispatch-namespaces', {'name': 'assets', 'customer_mode': 'platform'}, want=201)
        cfbase = '/client/v4/accounts/' + org + '/workers'
        script_base = cfbase + '/dispatch/namespaces/assets/scripts/'
        self.cfbase, self.script_base = cfbase, script_base
        started = time.monotonic()
        def upload(index):
            script = 'customer-' + str(index)
            content = bytes([65 + index]) * (1 << 20)
            digest = hashlib.sha256(content).hexdigest()
            manifest = {name: {'hash': digest[:32], 'size': len(content)} for name in ['/body.bin', '/copy.bin']}
            created = self.cf('POST', script_base + script + '/assets-upload-session', {'manifest': manifest}, customer=script)
            assert created['buckets'] == [[digest[:32]]], created['buckets']
            kind = None if index % 2 else 'application/x-nanoflare-fixture'
            parts = [(digest[:32], base64.b64encode(content), kind)]
            complete = self.cf('POST', cfbase + '/assets/upload?base64=true', token=created['jwt'], parts=parts, want=201)
            replay = self.cf('POST', cfbase + '/assets/upload?base64=true', token=created['jwt'], parts=parts, want=201)
            assert complete['jwt'] == replay['jwt']
            metadata = {'main_module': 'worker.js', 'compatibility_date': '2025-12-10', 'assets': {'jwt': complete['jwt']}, 'bindings': [{'name': 'ASSETS', 'type': 'assets'}]}
            code = b'''export default {async fetch(request,env){const name=new URL(request.url).searchParams.has("copy")?"/copy.bin":"/body.bin";const r=await env.ASSETS.fetch(name);const bytes=await r.arrayBuffer();const hash=Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",bytes)),v=>v.toString(16).padStart(2,"0")).join("");return Response.json({status:r.status,size:bytes.byteLength,hash,type:r.headers.get("content-type")});}};'''
            self.cf('PUT', script_base + script, parts=[('metadata', json.dumps(metadata).encode(), 'application/json'), ('worker.js', code, 'application/javascript+module')], key='assets-' + script, customer=script)
            return {'script': script, 'hash': digest, 'size': len(content), 'type': kind or 'application/octet-stream'}
        with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
            fixtures = list(pool.map(upload, range(4)))
        self.observe('four_concurrent_adapter_sessions_group_duplicate_hashes_and_publish_verified_assets', elapsed_ms=round((time.monotonic()-started)*1000))
        platform = self.api('POST', base + '/workers', {'name': 'platform', 'hostname': 'assets.example.test'}, want=201)
        self.api('POST', base + '/workers/' + platform['id'] + '/deployments', {'format': 'modules', 'compatibility_date': '2025-12-10', 'dispatch_namespaces': [{'binding': 'CUSTOMERS', 'namespace_id': ns['id']}], 'files': [{'path': 'platform.js', 'content': 'export default {fetch(request,env){return env.CUSTOMERS.get(new URL(request.url).searchParams.get("script")).fetch(request)}};'}]}, want=201)
        def verify(item):
            fixture, copy = item
            result = self.api('GET', '/internal/http/workers/' + platform['id'] + '/assets?script=' + fixture['script'] + ('&copy=1' if copy else ''))
            assert result == {'status': 200, 'size': fixture['size'], 'hash': fixture['hash'], 'type': fixture['type']}, result
        reads = [(fixture, copy) for fixture in fixtures for copy in [False, True]]
        with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
            list(pool.map(verify, reads))
        self.observe('native_dispatch_reads_exact_bytes_and_content_types_from_both_manifest_paths', reads=8, distinct_bytes=4 << 20, manifest_bytes=8 << 20)
        self.kill_control()
        self.start_control()
        command('docker', 'restart', self.object_store)
        wait_for('MinIO restart ready', self.object_ready)
        for item in reads:
            verify(item)
        self.observe('control_process_and_minio_restart_preserve_adapter_published_artifacts')
        # Completed sessions are unnecessary for serving retained immutable assets.
        removed = self.sql("WITH removed AS (DELETE FROM asset_upload_sessions RETURNING id) SELECT count(*) FROM removed")
        assert removed == '4', removed
        for item in reads:
            verify(item)
        self.observe('deleting_staging_sessions_does_not_delete_published_assets')
        self.report['passed'] = True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--control', type=pathlib.Path, required=True)
    parser.add_argument('--runner', type=pathlib.Path, required=True)
    parser.add_argument('--image', default='nanoflare-qualification/runtime:step10-native')
    parser.add_argument('--workerd', default='/usr/local/bin/workerd-native')
    parser.add_argument('--report', type=pathlib.Path, required=True)
    args = parser.parse_args()
    args.control, args.runner = args.control.resolve(strict=True), args.runner.resolve(strict=True)
    drill = AssetDrill(args)
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
