#!/usr/bin/env python3
"""Opt-in Linux API authorization and sustained native capacity qualification."""
import argparse
import datetime
import hashlib
import json
import pathlib
import shlex
import subprocess
import tempfile
import time
import uuid

from process_recovery import command, wait_for

ROOT=pathlib.Path(__file__).resolve().parents[2]
MATRIX='^Test(RuntimeCapabilityBoundaries|RuntimeStorageCancellationReachesBackend|RuntimeReconciliationSerializesWithDeletion|PoolMetricsKeepUnknownAndStaleUsageUnavailable|DispatchLeaseAuthorization|DynamicServicesInheritAuthorityAndBudget|DispatchResourceSharingRevisionAndRevocationMatrix|DispatchTelemetryScopesPolicyAndTrustedElapsedTime|PrivateDynamicDispatchEndToEnd|ImmutableArtifactsAndPublicationRecovery|ArtifactAPIProtectionAndIdempotency|ManagedPublication.*|ManagedRecovery.*|Cloudflare.*)$'


def source_snapshot():
    files=sorted(set(list(ROOT.glob('cmd/**/*.go'))+list(ROOT.glob('internal/**/*.go'))+list(ROOT.glob('internal/**/*.js'))+[ROOT/'go.mod',ROOT/'go.sum']))
    entries=[(p.relative_to(ROOT).as_posix(),hashlib.sha256(p.read_bytes()).hexdigest()) for p in files]
    return {'source_scope':'cmd/**/*.go, internal/**/*.go, internal/**/*.js, go.mod, go.sum; sorted relative path NUL SHA-256 newline',
            'source_files':len(entries),'source_tree_sha256':hashlib.sha256(''.join(f'{p}\0{digest}\n' for p,digest in entries).encode()).hexdigest()}


class Qualification:
    def __init__(self,args):
        self.args=args
        self.prefix='nf-wfp-linux-'+uuid.uuid4().hex[:12]
        self.network=self.prefix+'-net'
        self.network_created=False
        self.names=[]
        self.volumes=[]
        self.temp=tempfile.TemporaryDirectory(prefix='nf-wfp-linux-')
        self.directory=pathlib.Path(self.temp.name)
        self.report={'at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'passed':False,
                     'probe_sha256':hashlib.sha256(args.probe.read_bytes()).hexdigest(),
                     'matrix_expression':MATRIX,'stages':[],**source_snapshot(),
                     'limitations':['single-host trusted shared profile; no untrusted sandbox or CPU enforcement',
                                    'capacity has memory metadata/object fixtures; separate process drills cover real S3/PG recovery',
                                    'whole probe cgroup includes the Go control fixture and native engine; production API is outside runner cgroup',
                                    'RSS is approximate and point-in-time; cgroup peak is whole-probe memory, not per-isolate RSS',
                                    'bounded sustained slices are not production soak or an advertised maximum tenant count']}

    def run_stage(self,stage,expression,seconds=2):
        name=self.prefix+'-'+stage
        self.names.append(name)
        volume=name+'-results';self.volumes.append(volume)
        command('docker','volume','create','--label','codex.task=wfp-step10-linux-pool',volume)
        command('docker','run','--rm','--pull=never','--network','none','--mount',f'type=volume,src={volume},dst=/results','--entrypoint','chown',self.image,'65532:65532','/results')
        script='mkdir -p /tmp/nativebin; ln -s '+shlex.quote(self.args.workerd)+' /tmp/nativebin/workerd; '
        script+='PATH=/tmp/nativebin:$PATH /probe/api.test -test.v -test.run '+shlex.quote(expression)+' -test.timeout='+('6m' if stage=='capacity' else '4m')+'; '
        script+='probe_status=$?; for metric in memory.max memory.swap.max memory.current memory.peak memory.events pids.max pids.current; do cat /sys/fs/cgroup/$metric > /results/cgroup-$metric; done; exit $probe_status'
        sdk_mount=[]
        if self.args.sdk:
            sdk_mount=['--mount',f'type=bind,src={self.args.sdk},dst=/sdk/cloudflare,readonly',
                       '--mount',f'type=bind,src={ROOT/"scripts/wfp-client-flows"},dst=/scripts/wfp-client-flows,readonly',
                       '--workdir','/usr/local','-e','NANOFLARE_TEST_CF_SDK_DIR=/sdk/cloudflare']
        command('docker','run','--pull=never','-d','--name',name,'--label','codex.task=wfp-step10-linux-pool',
                '--network',self.network,'--init','--memory='+self.args.memory,'--memory-swap='+self.args.memory,'--pids-limit=128',
                '--read-only','--cap-drop=ALL','--security-opt=no-new-privileges','--user=65532:65532',
                '--tmpfs','/tmp:rw,nosuid,nodev,size=256m,mode=1777',
                '--mount',f'type=bind,src={self.args.probe},dst=/probe/api.test,readonly',
                '--mount',f'type=volume,src={volume},dst=/results',
                '-e','NANOFLARE_TEST_POSTGRES_URL=postgres://postgres:qualification@postgres:5432/postgres?sslmode=disable',
                '-e','NANOFLARE_RUN_CAPACITY='+('1' if stage=='capacity' else '0'),
                '-e','NANOFLARE_CAPACITY_PHASE_SECONDS='+str(seconds),
                '-e','NANOFLARE_CAPACITY_REPORT=/results/capacity.json',
                *sdk_mount,'--entrypoint','/bin/sh',self.image,'-c',script)
        logfile=self.directory/(stage+'.log')
        with logfile.open('wb') as output:
            reader=subprocess.Popen(['docker','logs','--follow',name],stdout=output,stderr=subprocess.STDOUT)
            try:status=command('docker','wait',name,timeout=390)
            finally:
                try:reader.wait(timeout=5)
                except subprocess.TimeoutExpired:reader.terminate();reader.wait(timeout=5)
        text=logfile.read_text()
        result={'name':stage,'exit_code':int(status),'passes':sum(line.lstrip().startswith('--- PASS:') for line in text.splitlines()),
                'skips':[line.strip() for line in text.splitlines() if line.lstrip().startswith('--- SKIP:')]}
        target=self.args.report.with_name(self.args.report.stem+'-'+stage+'.log')
        target.write_text(text)
        self.report['stages'].append(result)
        assert status=='0',text[-6000:]
        for metric in ['memory.max','memory.swap.max','memory.current','memory.peak','memory.events','pids.max','pids.current']:
            destination=self.directory/(stage+'-'+metric)
            command('docker','cp',name+':/results/cgroup-'+metric,str(destination))
            result[metric]=destination.read_text().strip()
        assert result['memory.max']==str(int(self.args.memory[0])*(1<<30)) and result['memory.swap.max']=='0' and result['pids.max']=='128',result
        events=dict(line.split() for line in result['memory.events'].splitlines())
        assert events.get('oom_kill')=='0',result
        if stage=='authorization':
            assert '--- PASS: TestPrivateDynamicDispatchEndToEnd ' in text, 'native end-to-end test did not pass'
            assert '--- PASS: TestRuntimeCapabilityBoundaries ' in text, 'capability matrix did not pass'
            assert not result['skips'],'selected Linux matrix skipped required checks'
        else:
            command('docker','cp',name+':/results/capacity.json',str(self.args.capacity_report))
            result['capacity_report_sha256']=hashlib.sha256(self.args.capacity_report.read_bytes()).hexdigest()
            assert json.loads(self.args.capacity_report.read_text())['passed']
        print(stage+' passed; peak whole-probe MiB='+str(round(int(result['memory.peak'])/(1<<20),1)),flush=True)
        # Retain only this fixture's test output at the requested operator path.
        target=self.args.report.with_name(self.args.report.stem+'-'+stage+'.log')
        target.write_text(text)

    def run(self):
        self.image=command('docker','image','inspect',self.args.image,'--format','{{.Id}}')
        pg=command('docker','image','inspect','postgres:17-alpine','--format','{{.Id}}')
        self.report.update(image_id=self.image,postgres_image_id=pg,phase_seconds=self.args.seconds,memory_bytes=int(self.args.memory[0])*(1<<30))
        if self.args.sdk:
            package=json.loads((self.args.sdk/'package.json').read_text())
            assert package['version']=='7.3.0'
            self.report['sdk_version']=package['version']
        command('docker','network','create','--internal','--label','codex.task=wfp-step10-linux-pool',self.network)
        self.network_created=True
        postgres=self.prefix+'-postgres';self.names.append(postgres)
        command('docker','run','--pull=never','-d','--name',postgres,'--label','codex.task=wfp-step10-linux-pool',
                '--network',self.network,'--network-alias','postgres','--memory=256m','--memory-swap=256m','--pids-limit=128',
                '-e','POSTGRES_PASSWORD=qualification',pg)
        def ready():
            try:return bool(command('docker','exec',postgres,'pg_isready','-U','postgres'))
            except RuntimeError:return False
        wait_for('disposable PostgreSQL',ready)
        if not self.args.capacity_only:self.run_stage('authorization',MATRIX)
        if not self.args.matrix_only:self.run_stage('capacity','^TestPrivateDispatchCapacityQualification$',self.args.seconds)
        self.report['passed']=True

    def close(self):
        errors=[]
        for name in reversed(self.names):
            try:command('docker','rm','-f','-v',name)
            except Exception as error:errors.append(str(error))
        for volume in reversed(self.volumes):
            try:command('docker','volume','rm',volume)
            except Exception as error:errors.append(str(error))
        if self.network_created:
            try:command('docker','network','rm',self.network)
            except Exception as error:errors.append(str(error))
        if errors:self.report.update(passed=False,cleanup_errors=errors)
        self.report['cleanup_verified']=not errors
        self.temp.cleanup()


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--probe',type=pathlib.Path,required=True)
    parser.add_argument('--sdk',type=pathlib.Path,help='isolated cloudflare@7.3.0 package; required by the authorization matrix')
    parser.add_argument('--image',default='nanoflare-qualification/runtime:step10')
    parser.add_argument('--workerd',default='/usr/local/bin/workerd-native')
    parser.add_argument('--memory',choices=['1g','2g'],default='2g')
    parser.add_argument('--seconds',type=int,default=15,choices=range(1,16))
    parser.add_argument('--report',type=pathlib.Path,required=True)
    parser.add_argument('--capacity-report',type=pathlib.Path,required=True)
    group=parser.add_mutually_exclusive_group();group.add_argument('--matrix-only',action='store_true');group.add_argument('--capacity-only',action='store_true')
    args=parser.parse_args();args.probe=args.probe.resolve(strict=True)
    if not args.capacity_only and args.sdk is None:parser.error('--sdk is required to run the authorization matrix without skips')
    if args.sdk:args.sdk=args.sdk.resolve(strict=True)
    run=Qualification(args)
    try:run.run()
    except Exception as error:run.report.update(passed=False,failure=str(error));raise
    finally:run.close();args.report.write_text(json.dumps(run.report,indent=2)+'\n')
    if not run.report['passed']:raise SystemExit('qualification or cleanup failed')


if __name__=='__main__':main()
