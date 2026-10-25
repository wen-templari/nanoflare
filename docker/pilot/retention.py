#!/usr/bin/env python3
"""Inspect active pilot retention settings without exposing credentials."""
import json, pathlib, subprocess, urllib.request, ssl
root=pathlib.Path(__file__).resolve().parent
ids=subprocess.check_output([str(root/'pilot.sh'),'ps','-q'],text=True).split()
containers=json.loads(subprocess.check_output(['docker','inspect',*ids],text=True))
assert len(containers)==len(json.loads((root/'compose.json').read_text())['services'])
observed={}
for c in containers:
    service=c['Config']['Labels']['com.docker.compose.service']
    logging=c['HostConfig']['LogConfig'];assert logging['Type']=='local'
    assert logging['Config']['max-size']=='10m' and logging['Config']['max-file']=='3'
    assert c['State']['Running']
    observed[service]={'log_driver':logging['Type'],'options':logging['Config']}
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPSHandler(context=ssl._create_unverified_context()))
def get(url,headers=None):
    with opener.open(urllib.request.Request(url,headers=headers or {}),timeout=5) as response:return response.read()
flags=json.loads(get('http://127.0.0.1:19090/api/v1/status/flags'))['data']
assert flags['storage.tsdb.retention.time']=='1w' and flags['storage.tsdb.retention.size']=='512MiB'
for host in ('recovery.workers.pilot.localhost','d1.workers.pilot.localhost'):
    data=get('https://127.0.0.1:18443/',{'Host':host})
    if host.startswith('d1.'):assert json.loads(data)['results']==[{'id':1,'value':'retained-sqlite-row'}]
    else:assert data==b'pilot:retained-pilot-value'
report={'passed':True,'container_logging':observed,'prometheus':{k:v for k,v in flags.items() if k.startswith('storage.tsdb.retention.')},'worker_and_d1_survived_container_recreation':True,'limitations':'Active configuration verified, not time-based deletion. Metrics block retention excludes WAL and head storage; log limits are per container and exclude other stores. No deployment revision or backup retention policy qualified.'}
(root.parents[1]/'scripts/wfp-qualification/results-cloudflare-stages-local-pilot-retention.json').write_text(json.dumps(report,indent=2)+'\n')
print('All running services have bounded rotating logs; Prometheus reports one-week/512 MiB block retention; both Workers retained content.')
