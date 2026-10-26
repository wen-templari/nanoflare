#!/usr/bin/env python3
"""Cold snapshot authoritative pilot stores, restore to a fresh isolated project."""
import base64, hashlib, json, os, re, pathlib, subprocess, time, urllib.request, ssl
root = pathlib.Path(__file__).resolve().parent
project = os.environ.get('PILOT_RESTORE_PROJECT', 'nanoflare-pilot-restore')
assert re.fullmatch(r'nanoflare-pilot-restore(?:-[a-z0-9]+)?', project)
offset = int(os.environ.get('PILOT_RESTORE_PORT_OFFSET', '30000' if project.endswith('-wasm') else '20000' if project.endswith('-d1') else '10000'))
def run(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)
def pilot(*args):
    run(str(root / 'pilot.sh'), *args, stdout=subprocess.DEVNULL)
config = json.loads((root / 'private/compose.lock.json').read_text())
for service in config['services'].values():
    for port in service.get('ports', []):
        assert 0 < int(port['published']) + offset <= 65535, 'Invalid restore port offset'
# A unique backup directory prevents accidental overwrite of any recovery set.
backup = root / 'private' / ('backup-' + str(time.time_ns()))
backup.mkdir(mode=0o700)
for name in ('pilot.env', 'compose.lock.json', 'recovery.json', 'wasm.json'):
    if name == 'wasm.json' and not (root / 'private' / name).exists():continue
    data = (root / 'private' / name).read_bytes()
    (backup / name).write_bytes(data); os.chmod(backup / name, 0o600)
image = config['services']['postgres']['image']
stores = ['postgres-data', 'minio-data', 'nanoflared-data', 'nanoflare-runner-data']
# Refuse to reuse an existing recovery project or populated target volumes.
existing = subprocess.check_output(['docker','volume','ls','--format','{{.Name}}'],text=True).splitlines()
assert not any(v.startswith(project + '_') for v in existing), 'Recovery project volumes already exist; inspect them before a new drill.'
try:
    pilot('stop')
    for store in stores:
        run('docker','run','--rm','--network','none','--entrypoint','tar','--mount','type=volume,src=nanoflare-pilot_'+store+',dst=/source,readonly','--mount','type=bind,src='+str(backup)+',dst=/backup',image,'-C','/source','-cpf','/backup/'+store+'.tar','.')
finally:
    # Always bring the original deployment back, even on snapshot failure.
    pilot('start')
manifest = {store: {'sha256': hashlib.file_digest(open(backup / (store+'.tar'),'rb'),'sha256').hexdigest(), 'bytes': (backup / (store+'.tar')).stat().st_size} for store in stores}
(backup / 'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
for store in stores:
    volume = project+'_'+store
    with open(backup / (store+'.tar'), 'rb') as archive:
        assert hashlib.file_digest(archive, 'sha256').hexdigest() == manifest[store]['sha256']
    run('docker','volume','create','--label','com.docker.compose.project='+project,'--label','com.docker.compose.volume='+store,volume,stdout=subprocess.DEVNULL)
    run('docker','run','--rm','--network','none','--entrypoint','tar','--mount','type=volume,src='+volume+',dst=/restore','--mount','type=bind,src='+str(backup)+',dst=/backup,readonly',image,'-C','/restore','-xpf','/backup/'+store+'.tar')
config['name'] = project
for service in config['services'].values():
    for port in service.get('ports',[]):
        port['published'] = str(int(port['published']) + offset)
restore_config = root / ('private/' + project + '.lock.json')
restore_config.write_text(json.dumps(config,indent=2)+'\n');os.chmod(restore_config,0o600)
compose = ['docker','compose','--project-directory',str(root),'--env-file',str(backup/'pilot.env'),'-p',project,'-f',str(restore_config)]
run(*compose,'up','-d','--pull','never')
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=ssl._create_unverified_context()))
def wait(check):
    deadline=time.monotonic()+90
    while time.monotonic()<deadline:
        try:
            if check():return
        except Exception:pass
        time.sleep(1)
    raise RuntimeError('restored Worker did not become ready')
def worker(port):
    req=urllib.request.Request('https://127.0.0.1:'+str(port)+'/',headers={'Host':'recovery.workers.pilot.localhost'})
    with opener.open(req,timeout=3) as response:return response.read()==b'pilot:retained-pilot-value'
wait(lambda:worker(18443+offset));wait(lambda:worker(18443))
state=json.loads((backup/'recovery.json').read_text())
req=urllib.request.Request('http://127.0.0.1:'+str(18080+offset)+'/v1/organizations/'+state['org']+'/workers',headers={'Authorization':'Bearer '+state['token']})
with opener.open(req,timeout=5) as response:assert response.status==200
d1_verified=False
if 'd1_database' in state:
    def d1(port):
        req=urllib.request.Request('https://127.0.0.1:'+str(port)+'/',headers={'Host':'d1.workers.pilot.localhost'})
        with opener.open(req,timeout=5) as response:
            return json.load(response)['results']==[{'id':1,'value':'retained-sqlite-row'}]
    wait(lambda:d1(18443+offset));wait(lambda:d1(18443));d1_verified=True
wasm_verified=False
wasm_path=backup/'wasm.json'
if wasm_path.exists():
    wasm_state=json.loads(wasm_path.read_text())
    customer_path='/v1/organizations/'+state['org']+'/workers/'+wasm_state['customer']
    def wasm_api(method,path,body=None):
        req=urllib.request.Request('http://127.0.0.1:'+str(18080+offset)+path, data=None if body is None else json.dumps(body).encode(), headers={'Authorization':'Bearer '+state['token'],'Content-Type':'application/json'},method=method)
        with opener.open(req,timeout=20) as response:
            data=response.read()
            return json.loads(data) if data else None
    def wasm(port,answer):
        req=urllib.request.Request('https://127.0.0.1:'+str(port)+'/',headers={'Host':'wasm.workers.pilot.localhost'})
        with opener.open(req,timeout=5) as response:
            return response.read()==('pilot-wasm:retained-wasm-value:'+str(answer)).encode()
    def wasm_bytes(answer):
        files=wasm_api('GET',customer_path+'/files')
        binary=next(file for file in files if file['path']=='answer.wasm')
        expected=bytes.fromhex('0061736d010000000105016000017f030201000707010372756e00000a0601040041'+format(answer,'02x')+'0b00040178fffe')
        assert base64.b64decode(binary['wasm'])==expected and binary['size']==len(expected) and not binary['content']
        return next(file['content'] for file in files if file['path']=='worker.js')
    wait(lambda:wasm(18443+offset,42));wait(lambda:wasm(18443,42))
    original_js=wasm_bytes(42)
    for revision,answer in [(wasm_state['second'],7),(wasm_state['first'],42)]:
        wasm_api('PUT',customer_path+'/deployment-traffic',{'deployments':[{'id':revision,'traffic_percent':100}]})
        wait(lambda:wasm(18443+offset,answer))
        assert wasm_bytes(answer)==original_js
    wait(lambda:wasm(18443,42))
    wasm_verified=True
report={'wasm_restored_and_both_revisions_verified':wasm_verified,'populated_d1_restored':d1_verified,'passed':True,'backup_type':'cold physical snapshot, same pinned images and signing/encryption keys','stores':manifest,'restored_project':project,'restored_dispatch_and_secret':True,'restored_login_token':True,'original_pilot_recovered':True,'port_offset':offset,'production_qualified':False,'untrusted_execution_qualified':False,'limitations':'No point-in-time recovery, cross-version migration or off-host/disaster recovery qualification. Restore stack retained for inspection.'}
(root.parents[1]/('scripts/wfp-qualification/results-cloudflare-stages-local-pilot-restore'+('-wasm' if wasm_verified else '-d1' if d1_verified else '')+'.json')).write_text(json.dumps(report,indent=2)+'\n')
print(json.dumps(report))
