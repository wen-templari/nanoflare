#!/usr/bin/env python3
"""Disposable real daemon/Vector/Loki/Prometheus qualification; no production data."""
import argparse
import base64
import hashlib
import json
import pathlib
import os
import time
import uuid
import urllib.parse
import urllib.error
import urllib.request
import re

from process_recovery import Drill, command, free_port, wait_for, ROOT


class Monitoring(Drill):
    label = "codex.task=wfp-step10-monitoring"

    def __init__(self, args):
        super().__init__(args)
        self.network = self.prefix + '-network'
        self.volumes = []
        self.network_created = False
        self.metrics_token = 'qualification-disposable-metrics'
        self.loki_port, self.prometheus_port, self.grafana_port = free_port(), free_port(), free_port()
        self.report['limitations'] = [
            'disposable single-host Linux monitoring stack, not production retention/sizing',
            'Docker log source restricted to this fixture forge container; no unrelated logs read',
            'socket UID/GID permissions qualify Linux only; trusted UID 0/GID 65532 producers can submit identity',
            'no untrusted sandbox, per-invocation CPU, multi-host outage or soak qualification']

    def container(self, suffix, image, options, arguments=()):
        return super().container(suffix, image, ['--network', self.network, '--network-alias', suffix] + options, arguments)

    def volume(self, suffix):
        name = self.prefix + '-' + suffix
        command('docker', 'volume', 'create', '--label', 'codex.task=wfp-step10-monitoring', name)
        self.volumes.append(name)
        return name

    def get(self, base, path, headers=None):
        request = urllib.request.Request(base+path, headers=headers or {})
        try:
            with self.opener.open(request, timeout=2) as response:
                raw = response.read()
                try:
                    body = json.loads(raw)
                except ValueError:
                    body = raw.decode(errors='replace')
                return response.status, body
        except urllib.error.HTTPError as error:
            return error.code, error.read().decode(errors='replace')
        except (OSError, urllib.error.URLError):
            return None, None

    def assert_running(self, name):
        state = command('docker', 'inspect', name, '--format', '{{.State.Running}}')
        if state != 'true':
            raise RuntimeError('disposable container stopped: ' + name + ': ' + command('docker', 'logs', '--tail', '15', name))

    def logs_for(self, selector):
        path = '/loki/api/v1/query_range?' + urllib.parse.urlencode({'query': selector, 'limit': 1000, 'start': str(int((time.time()-120)*1e9)), 'end': str(time.time_ns())})
        status, body = self.get(f'http://127.0.0.1:{self.loki_port}', path)
        if status != 200 or not isinstance(body, dict) or body.get('status') != 'success':
            return []
        return body['data']['result']

    def run(self):
        image = self.image(self.args.image)
        node_image = self.image('node:24-slim')
        vector_image = self.image('timberio/vector:0.46.1-alpine')
        loki_image = self.image('grafana/loki:3.5.0')
        prometheus_image = self.image('prom/prometheus:v3.5.0')
        grafana_image = self.image('grafana/grafana:12.0.2')
        pg_image = self.image('postgres:17-alpine')
        object_image = self.image('minio/minio:RELEASE.2025-04-22T22-12-26Z')
        self.report.update({'control_sha256': hashlib.sha256(self.args.control.read_bytes()).hexdigest(),
                            'runner_sha256': hashlib.sha256(self.args.runner.read_bytes()).hexdigest(),
                            'image_ids': {'runtime': image, 'node_fixture': node_image, 'vector': vector_image,
                                          'loki': loki_image, 'prometheus': prometheus_image, 'grafana': grafana_image,
                                          'postgres': pg_image, 'minio': object_image}})
        command('docker', 'network', 'create', '--label', 'codex.task=wfp-step10-monitoring', self.network)
        self.network_created = True
        socket_volume, token_volume = self.volume('collector'), self.volume('metrics-secret')
        # Initialization affects only a newly created disposable volume. Prometheus
        # runs as its real UID 65534 and reads an owner-only credential file.
        command('docker', 'run', '--rm', '--pull=never', '--network', 'none',
                '--mount', f'type=volume,src={token_volume},dst=/secrets',
                '--entrypoint', '/bin/sh', vector_image, '-ec',
                'printf %s qualification-disposable-metrics > /secrets/nanoflare-metrics-token; chown 65534:65534 /secrets/nanoflare-metrics-token; chmod 0400 /secrets/nanoflare-metrics-token')
        self.postgres = self.container('postgres', pg_image, ['--memory=256m', '--memory-swap=256m', '--pids-limit=128', '-e', 'POSTGRES_PASSWORD=qualification'])
        self.object_store = self.container('objects', object_image, ['--memory=256m', '--memory-swap=256m', '--pids-limit=128', '-p', f'127.0.0.1:{self.object_port}:9000', '-e', 'MINIO_ROOT_USER=qualification', '-e', 'MINIO_ROOT_PASSWORD=qualification-disposable'], ['server', '/data'])
        wait_for('PostgreSQL ready', self.pg_ready)
        def object_ready():
            self.assert_running(self.object_store)
            return self.get(f'http://127.0.0.1:{self.object_port}', '/minio/health/ready')[0] == 200
        wait_for('MinIO ready', object_ready)
        self.loki = self.container('loki', loki_image, ['--memory=384m', '--memory-swap=384m', '--pids-limit=128', '-p', f'127.0.0.1:{self.loki_port}:3100', '--mount', f'type=bind,src={ROOT / "docker/loki/loki-config.yml"},dst=/etc/loki/config.yml,readonly'], ['-config.file=/etc/loki/config.yml'])
        wait_for('Loki ready', lambda: self.get(f'http://127.0.0.1:{self.loki_port}', '/ready')[0] == 200, seconds=45)
        vector_config = (ROOT/'docker/vector/vector.yaml').read_text()
        # Operator Docker diagnostics must not read unrelated host containers.
        vector_config = vector_config.replace('    docker_host: unix:///var/run/docker.sock', '    docker_host: unix:///var/run/docker.sock\n    include_containers: ['+json.dumps(self.prefix+'-forgery')+']')
        vector_path = self.directory/'vector.yaml'
        vector_path.write_text(vector_config)
        # Resolve only defaults in a clean environment, without loading .env or
        # substituting any user service credentials. Preserve Docker connection
        # settings so the same configured local engine handles all fixture calls.
        compose_env={key:value for key,value in os.environ.items() if key in {
            'PATH','HOME','DOCKER_HOST','DOCKER_CONTEXT','DOCKER_CONFIG','DOCKER_CERT_PATH','DOCKER_TLS_VERIFY'}}
        compose = json.loads(command('docker', 'compose', '--env-file', '/dev/null', '-f',
                                     str(ROOT/'docker/compose.yml'), '-f',str(ROOT/'docker/compose.trusted-pilot.yml'), 'config', '--format', 'json',env=compose_env))
        vector_service = compose['services']['vector']
        entrypoint = vector_service['entrypoint']
        assert entrypoint[0] == '/bin/sh'
        vector_arguments = entrypoint[1:] + vector_service['command']
        self.vector = self.container('vector', vector_image, ['--memory=256m', '--memory-swap=256m', '--pids-limit=128', '--user='+vector_service['user'], '--entrypoint='+entrypoint[0], '--mount', f'type=bind,src={vector_path},dst=/etc/vector/vector.yaml,readonly', '--mount', 'type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock,readonly', '--mount', f'type=volume,src={socket_volume},dst=/var/run/nanoflare'], vector_arguments)
        def socket_ready():
            self.assert_running(self.vector)
            try:
                return command('docker', 'exec', self.vector, 'test', '-S', '/var/run/nanoflare/vector.sock') == ''
            except RuntimeError:
                return False
        wait_for('Vector collector socket', socket_ready)
        permissions = command('docker', 'exec', self.vector, 'stat', '-c', '%u:%g %a', '/var/run/nanoflare', '/var/run/nanoflare/vector.sock')
        assert permissions == '0:65532 710\n0:65532 660', permissions
        self.observe('collector_permissions_verified', stat=permissions)
        # A different Linux UID/GID must be denied even if the volume is mounted.
        intruder = "const net=require('node:net');const s=net.connect('/collector/vector.sock');s.on('error',e=>{if(e.code==='EACCES')process.exit(0);console.error(e.code);process.exit(2)});s.on('connect',()=>process.exit(3));setTimeout(()=>process.exit(4),1500)"
        command('docker', 'run', '--rm', '--pull=never', '--network', 'none', '--user=65534:65534', '--cap-drop=ALL', '--security-opt=no-new-privileges', '--mount', f'type=volume,src={socket_volume},dst=/collector,readonly', node_image, 'node', '-e', intruder)
        self.observe('foreign_uid_cannot_submit_collector_identity')
        # Conversely, the explicitly trusted producer group can use a read-only mount.
        trusted = "const net=require('node:net');const s=net.connect('/collector/vector.sock');s.on('error',e=>{console.error(e.code);process.exit(2)});s.on('connect',()=>s.end(JSON.stringify({app_id:'trusted-probe',deployment_id:'probe',level:'info',message:'trusted-group-probe'})+'\\n',()=>process.exit(0)));setTimeout(()=>process.exit(4),1500)"
        command('docker', 'run', '--rm', '--pull=never', '--network', 'none', '--user=65532:65532', '--cap-drop=ALL', '--mount', f'type=volume,src={socket_volume},dst=/collector,readonly', node_image, 'node', '-e', trusted)
        wait_for('trusted group event reaches Loki',lambda:bool(self.logs_for('{worker_id="trusted-probe"}')))
        self.observe('trusted_group_can_connect_readonly_collector_mount')
        runner_profile=compose['services']['nanoflare-runner']
        control_profile=compose['services']['nanoflared']
        assert self.args.workerd==runner_profile['environment']['NANOFLARE_WORKERD_EXECUTABLE']
        assert control_profile['environment']['NANOFLARE_PLATFORM_DISPATCH_ENABLED']=='true'
        runner_bytes=str(runner_profile['mem_limit'])
        control_bytes=str(control_profile['mem_limit'])
        self.report['pilot_bounds']={key:{field:compose['services'][key][field] for field in ['mem_limit','memswap_limit','pids_limit']} for key in ['nanoflared','nanoflare-runner']}
        self.runner = self.container('runner', image, ['--init', '--memory='+runner_bytes, '--memory-swap='+str(runner_profile['memswap_limit']), '--pids-limit='+str(runner_profile['pids_limit']), '--read-only', '--cap-drop=ALL', '--security-opt=no-new-privileges', '--user=65532:65532', '--tmpfs', '/tmp:rw,nosuid,nodev,size=64m,mode=1777', '--tmpfs', '/var/lib/nanoflare:rw,nosuid,nodev,size=64m,uid=65532,gid=65532', '--mount', f'type=bind,src={self.args.runner},dst=/qualification-runner,readonly', '--entrypoint=/qualification-runner', '--mount', f'type=volume,src={socket_volume},dst=/var/run/nanoflare,readonly', '-e', 'NANOFLARE_RUNNER_TOKEN=qualification-disposable-runner', '-e', 'NANOFLARE_LOG_VECTOR_SOCKET=unix:///var/run/nanoflare/vector.sock', '-e','NANOFLARE_WORKERD_EXECUTABLE='+self.args.workerd], ['-addr=:8090', '-config-dir=/var/lib/nanoflare/generated', '-runtime-socket-dir=/tmp/sockets', '-nanoflare-runtime-addr=nanoflared:8081'])
        self.control = self.container('nanoflared', image, ['--init', '--memory='+control_bytes, '--memory-swap='+str(control_profile['memswap_limit']), '--pids-limit='+str(control_profile['pids_limit']), '--mount', f'type=bind,src={self.args.control},dst=/qualification-control,readonly', '--entrypoint=/qualification-control', '--mount', f'type=volume,src={socket_volume},dst=/var/run/nanoflare,readonly', '-p', self.base.removeprefix('http://')+':8080', '-e', 'DATABASE_URL=postgres://postgres:qualification@postgres:5432/postgres?sslmode=disable', '-e', 'MINIO_ENDPOINT=objects:9000', '-e', 'MINIO_ACCESS_KEY=qualification', '-e', 'MINIO_SECRET_KEY=qualification-disposable', '-e', 'MINIO_BUCKET=qualification', '-e', 'NANOFLARE_SECRET_KEY=0123456789abcdef0123456789abcdef', '-e', 'NANOFLARE_AUTH_SECRET=qualification-disposable-control', '-e', 'NANOFLARE_RUNNER_TOKEN=qualification-disposable-runner', '-e', 'NANOFLARE_TRAEFIK_TOKEN=qualification-disposable-traefik', '-e', 'NANOFLARE_METRICS_TOKEN='+self.metrics_token, '-e', 'NANOFLARE_LOG_VECTOR_SOCKET=unix:///var/run/nanoflare/vector.sock', '-e','NANOFLARE_PLATFORM_DISPATCH_ENABLED='+control_profile['environment']['NANOFLARE_PLATFORM_DISPATCH_ENABLED'], '-e','NANOFLARE_CLOUDFLARE_COMPATIBILITY_ENABLED=true', '-e','NANOFLARE_WORKERD_EXECUTABLE='+self.args.workerd], ['-addr=:8080', '-runtime-addr=:8081', '-config-dir=/tmp/generated', '-runner-url=http://runner:8090', '-base-hostname=qualification.example.test', '-loki-url=http://loki:3100'])
        wait_for('daemon ready', lambda: self.request('GET', '/healthz', auth=False)['status'] == 200)
        # Production target/service discovery config, with a shorter fixture scrape interval.
        prom_config = (ROOT/'docker/prometheus/prometheus.yml').read_text().replace('scrape_interval: 15s','scrape_interval: 1s\n  evaluation_interval: 1s')
        prom_path = self.directory/'prometheus.yml'
        prom_path.write_text(prom_config)
        self.prometheus = self.container('prometheus', prometheus_image, ['--memory=256m', '--memory-swap=256m', '--pids-limit=128', '-p', f'127.0.0.1:{self.prometheus_port}:9090', '--mount', f'type=bind,src={prom_path},dst=/etc/prometheus/prometheus.yml,readonly', '--mount',f'type=bind,src={ROOT/"docker/prometheus/nanoflare-alerts.yml"},dst=/etc/prometheus/nanoflare-alerts.yml,readonly','--mount', f'type=volume,src={token_volume},dst=/run/secrets,readonly'], ['--config.file=/etc/prometheus/prometheus.yml', '--storage.tsdb.path=/prometheus'])
        wait_for('Prometheus ready', lambda: self.get(f'http://127.0.0.1:{self.prometheus_port}', '/-/ready')[0] == 200)
        grafana_service = compose['services']['grafana']
        assert grafana_service['environment']['GF_AUTH_ANONYMOUS_ENABLED'] == 'false'
        for service, targets in {'grafana':{3000}, 'prometheus':{9090}, 'postgres':{5432},
                                 'minio':{9000,9001}, 'tempo':{3200}, 'traefik':{8080}}.items():
            for port in compose['services'][service].get('ports',[]):
                if port['target'] in targets:
                    assert port.get('host_ip') == '127.0.0.1', (service, port)
        self.observe('production_admin_ports_bind_loopback')
        self.grafana = self.container('grafana', grafana_image,
                                      ['--memory=384m','--memory-swap=384m','--pids-limit=128',
                                       '-p',f'127.0.0.1:{self.grafana_port}:3000',
                                       '-e','GF_AUTH_ANONYMOUS_ENABLED='+grafana_service['environment']['GF_AUTH_ANONYMOUS_ENABLED'],
                                       '-e','GF_SECURITY_ADMIN_USER=qualification',
                                       '-e','GF_SECURITY_ADMIN_PASSWORD=qualification-disposable',
                                       '--mount',f'type=bind,src={ROOT/"docker/grafana/provisioning"},dst=/etc/grafana/provisioning,readonly'])
        grafana_url=f'http://127.0.0.1:{self.grafana_port}'
        wait_for('Grafana ready',lambda:self.get(grafana_url,'/api/health')[0]==200)
        assert self.get(grafana_url,'/api/datasources')[0]==401
        basic=base64.b64encode(b'qualification:qualification-disposable').decode()
        datasource_observation={}
        def datasources_ready():
            status,datasources=self.get(grafana_url,'/api/datasources',{'Authorization':'Basic '+basic})
            datasource_observation.update(status=status,body=datasources)
            return status==200 and any(item['type']=='loki' for item in datasources)
        try:wait_for('Grafana operator data source provisioning',datasources_ready)
        except AssertionError:raise AssertionError('Grafana data source observation: '+json.dumps(datasource_observation))
        self.observe('grafana_global_datasources_require_operator_login')
        assert command('docker','exec',self.prometheus,'id','-u') == '65534'
        assert command('docker','exec',self.prometheus,'stat','-c','%u:%g %a','/run/secrets/nanoflare-metrics-token') == '65534:65534 400'
        session = self.api('POST','/v1/setup/signup',{'email':'monitor@example.test','password':'qualification-disposable','organization_name':'Monitoring'},want=201)
        self.token = session['access_token']
        org = session['active_org_id']
        assert re.fullmatch(r'[a-zA-Z0-9_-]+',org)
        self.sql(f"UPDATE organizations SET usage_level='paid' WHERE id='{org}'")
        base = '/v1/organizations/'+org
        preview='/client/v4/accounts/'+org+'/workers/dispatch/namespaces'
        assert self.request('POST',preview,{'name':'preview-fixture'})['status']==422
        preview_headers={'Authorization':'Bearer '+self.token,'Content-Type':'application/json','X-Nanoflare-Execution-Profile':'trusted_shared_workerd'}
        request=urllib.request.Request(self.base+preview,method='POST',data=json.dumps({'name':'preview-fixture'}).encode(),headers=preview_headers)
        with self.opener.open(request,timeout=3) as response:
            assert response.status==200 and json.loads(response.read())['success']
        request=urllib.request.Request(self.base+preview+'/preview-fixture',method='DELETE',headers=preview_headers)
        with self.opener.open(request,timeout=3) as response:assert response.status==200 and json.loads(response.read())['success']
        self.observe('normal_daemon_experimental_adapter_requires_explicit_trust_acknowledgment')
        ns = self.api('POST',base+'/dispatch-namespaces',{'name':'monitoring','customer_mode':'platform'},want=201)
        workers = {}
        for name in ['alpha','beta']:
            worker = self.api('POST',base+'/dispatch-namespaces/'+ns['id']+'/scripts',{'script_name':name,'execution':'dynamic','customer_id':name},want=201)
            workers[name]=worker
            self.api('PUT',base+'/workers/'+worker['id']+'/secrets/TOKEN',{'value':'fixture-pinned-secret'},want=204)
            code = 'export default {fetch(request,env){console.log("observed-'+name+'",env.TOKEN,new URL(request.url).searchParams.get("probe")||"initial","__NANOFLARE_LOG__ {app_id:forged-victim}");return new Response("'+name+'")}}'
            self.api('POST',base+'/workers/'+worker['id']+'/deployments',{'format':'modules','compatibility_date':'2025-12-10','files':[{'path':'worker.js','content':code}]},want=201)
        platform = self.api('POST',base+'/workers',{'name':'platform','hostname':'monitor.example.test'},want=201)
        self.api('POST',base+'/workers/'+platform['id']+'/deployments',{'format':'modules','compatibility_date':'2025-12-10','dispatch_namespaces':[{'binding':'CUSTOMERS','namespace_id':ns['id']}],'files':[{'path':'platform.js','content':'export default {fetch(request,env){return env.CUSTOMERS.get(new URL(request.url).pathname.slice(1)).fetch(request)}}'}]},want=201)
        self.dispatch = '/internal/http/workers/'+platform['id']+'/alpha'
        for name in workers:
            result=self.request('GET','/internal/http/workers/'+platform['id']+'/'+name)
            assert result['status']==200 and result['body']==name,result
        for name, worker in workers.items():
            def output_arrived():
                result=self.request('GET',base+'/workers/'+worker['id']+'/output')
                if result['status']!=200:return False
                body=json.dumps(result['body'])
                if 'observed-'+name not in body:return False
                assert 'fixture-pinned-secret' not in body, 'pinned secret leaked through Loki output'
                assert 'observed-'+('beta' if name=='alpha' else 'alpha') not in body, 'cross-customer output'
                assert '[redacted]' in body
                return True
            wait_for('attributed '+name+' Loki output',output_arrived,seconds=45)
        self.observe('native_authenticated_logs_reach_vector_loki_scoped_output_with_secret_redaction')
        forge = json.dumps({'app_id':'forged-victim','deployment_id':'fake-revision','message':'forged-docker-marker'})
        self.container('forgery',node_image,['--memory=64m','--memory-swap=64m','--pids-limit=64'],['node','-e','console.log('+json.dumps(forge)+');setInterval(()=>{},1000)'])
        wait_for('forged Docker diagnostic recorded',lambda:any('forged-docker-marker' in json.dumps(s) for s in self.logs_for('{service=~".+",worker_id=""}')),seconds=45)
        assert not self.logs_for('{worker_id="forged-victim"}'), 'Docker/stdout text forged producer identity'
        self.observe('customer_markers_and_docker_json_cannot_assign_producer_identity')
        assert self.get(self.base,'/metrics')[0]==401
        assert self.get(self.base,'/metrics',{'Authorization':'Bearer '+self.token})[0]==401
        status,metrics=self.get(self.base,'/metrics',{'Authorization':'Bearer '+self.metrics_token})
        assert status==200 and 'nanoflare_log_forwarder_dropped_total' in metrics
        assert 'nanoflare_worker_cpu' not in metrics
        self.observe('operator_metrics_authentication_and_truthful_elapsed_metrics_verified')
        def scraped():
            status,payload=self.get(f'http://127.0.0.1:{self.prometheus_port}','/api/v1/query?'+urllib.parse.urlencode({'query':'up{job="nanoflared"}'}))
            return status==200 and bool(payload['data']['result']) and payload['data']['result'][0]['value'][1]=='1'
        wait_for('authenticated Prometheus scrape',scraped)
        self.observe('real_prometheus_uses_private_readable_token_and_container_target')
        def pool_metrics_available():
            status,body=self.get(self.base,'/metrics',{'Authorization':'Bearer '+self.metrics_token})
            if status!=200:return False
            metrics={line.split()[0]:float(line.split()[-1]) for line in body.splitlines() if line.startswith('nanoflare_runner_')}
            return metrics.get('nanoflare_runner_resources_available')==1 and metrics.get('nanoflare_runner_memory_limit_bytes')==int(runner_bytes) and metrics.get('nanoflare_runner_pids_limit')==int(runner_profile['pids_limit'])
        wait_for('authenticated measured physical pool',pool_metrics_available)
        status,rules=self.get(f'http://127.0.0.1:{self.prometheus_port}','/api/v1/rules')
        assert status==200 and any(group['name']=='nanoflare-physical-runner' and len(group['rules'])==6 for group in rules['data']['groups'])
        self.observe('selected_pilot_bounds_measured_and_operator_alert_rules_loaded')
        command('docker','stop','-t','1',self.vector)
        lost_marker=uuid.uuid4().hex
        result=self.request('GET',self.dispatch+'?probe='+lost_marker)
        assert result['status']==200,result
        def loss_visible():
            status,body=self.get(self.base,'/metrics',{'Authorization':'Bearer '+self.metrics_token})
            return status==200 and any(line.startswith('nanoflare_log_forwarder_dropped_total ') and float(line.split()[-1])>=1 for line in body.splitlines())
        wait_for('collector loss counter',loss_visible)
        command('docker','start',self.vector)
        wait_for('collector socket after restart',socket_ready)
        recovered_marker=uuid.uuid4().hex
        result=self.request('GET',self.dispatch+'?probe='+recovered_marker); assert result['status']==200,result
        wait_for('new event after collector restart',lambda:recovered_marker in json.dumps(self.logs_for('{worker_id='+json.dumps(workers['alpha']['id'])+'}')))
        assert lost_marker not in json.dumps(self.logs_for('{worker_id='+json.dumps(workers['alpha']['id'])+'}'))
        self.observe('collector_outage_preserves_dispatch_and_exports_collection_loss')
        def loss_alert_fires():
            status,payload=self.get(f'http://127.0.0.1:{self.prometheus_port}','/api/v1/alerts')
            return status==200 and any(alert['labels'].get('alertname')=='NanoflareLogCollectionLoss' and alert['state']=='firing' for alert in payload['data']['alerts'])
        wait_for('actual collector-loss alert',loss_alert_fires)
        self.observe('real_prometheus_collection_loss_alert_fires')
        command('docker','pause',self.runner)
        try:
            def stale_unavailable():
                status,body=self.get(self.base,'/metrics',{'Authorization':'Bearer '+self.metrics_token})
                return status==200 and 'nanoflare_runner_resources_available 0' in body and 'nanoflare_runner_memory_current_bytes ' not in body
            wait_for('stale runner sample becomes unavailable',stale_unavailable,seconds=40)
            assert self.request('GET',base+'/dispatch-namespaces')['status']==200
        finally:command('docker','unpause',self.runner)
        wait_for('runner resource sampling recovers',pool_metrics_available)
        assert self.request('GET',self.dispatch)['status']==200
        self.observe('runner_transport_stall_marks_samples_unavailable_without_blocking_control_metadata')
        self.report['passed']=True

    def close(self):
        super().close()
        errors=[]
        for name in reversed(self.volumes):
            try:command('docker','volume','rm',name)
            except Exception as error:errors.append(str(error))
        if self.network_created:
            try:command('docker','network','rm',self.network)
            except Exception as error:errors.append(str(error))
        if errors:self.report.update(passed=False,cleanup_errors=self.report.get('cleanup_errors',[])+errors,cleanup_verified=False)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--control',type=pathlib.Path,required=True,help='current Linux nanoflared binary')
    parser.add_argument('--runner',type=pathlib.Path,required=True,help='current Linux runner binary')
    parser.add_argument('--image',default='nanoflare-qualification/runtime:step10')
    parser.add_argument('--workerd',default='/usr/local/bin/workerd-native')
    parser.add_argument('--report',type=pathlib.Path,required=True)
    args=parser.parse_args()
    args.control,args.runner=args.control.resolve(strict=True),args.runner.resolve(strict=True)
    drill=Monitoring(args)
    try:drill.run()
    except Exception as error:
        drill.report.update(passed=False,failure=str(error));raise
    finally:
        drill.close();args.report.write_text(json.dumps(drill.report,indent=2)+'\n')
    if not drill.report['passed']:raise SystemExit('qualification or cleanup failed')


if __name__=='__main__':main()
