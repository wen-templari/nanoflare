#!/usr/bin/env python3
"""Bounded local pilot soak: no retries, exact response checks, retained failures."""
import concurrent.futures, json, pathlib, ssl, statistics, subprocess, threading, time, urllib.request, urllib.error
root=pathlib.Path(__file__).resolve().parent
started=time.monotonic(); duration=120
lock=threading.Lock();results=[];failures=[]
def lane(index):
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPSHandler(context=ssl._create_unverified_context()))
    host='d1.workers.pilot.localhost' if index%2 else 'recovery.workers.pilot.localhost'
    count=0
    while time.monotonic()-started<duration:
        target=started+count*0.4
        if target>time.monotonic():time.sleep(target-time.monotonic())
        if time.monotonic()-started>=duration:break
        begin=time.monotonic();status=0;error=None
        try:
            request=urllib.request.Request('https://127.0.0.1:18443/',headers={'Host':host})
            with opener.open(request,timeout=10) as response:
                status=response.status;data=response.read()
            assert status==200
            if index%2:assert json.loads(data)['results']==[{'id':1,'value':'retained-sqlite-row'}]
            else:assert data==b'pilot:retained-pilot-value'
        except Exception as e:
            status=getattr(e,'code',status);error=type(e).__name__+': '+str(e)
        with lock:
            results.append({'worker':'d1' if index%2 else 'dispatch','seconds':round(time.monotonic()-begin,6),'offset':round(begin-started,3),'status':status,'ok':error is None})
            if error:failures.append({'offset':round(begin-started,3),'worker':host,'status':status,'error':error})
        count+=1
with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:list(pool.map(lane,range(4)))
def summary(kind):
    selected=[r for r in results if r['worker']==kind];lat=sorted(r['seconds']*1000 for r in selected)
    return {'requests':len(selected),'failures':sum(not r['ok'] for r in selected),'latency_ms':{'p50':round(statistics.median(lat),2),'p95':round(lat[int((len(lat)-1)*.95)],2),'max':round(max(lat),2)}}
containers=[json.loads(line) for line in subprocess.check_output([str(root/'pilot.sh'),'ps','--format','json'],text=True).splitlines()]
report={'passed':not failures and all(c['State']=='running' for c in containers),'duration_seconds':round(time.monotonic()-started,2),'configuration':{'lanes':4,'target_requests_per_second':10,'retries':0,'fixture':'two dispatch lanes and two populated D1 read lanes'},'workers':{k:summary(k) for k in ('dispatch','d1')},'failures':failures,'services':{c['Service']:c['State'] for c in containers},'limitations':'Two-minute local read soak, one customer and one D1 row; not production capacity, write load, large tenant count or long-running soak.'}
(root.parents[1]/'scripts/wfp-qualification/results-cloudflare-stages-local-pilot-load.json').write_text(json.dumps(report,indent=2)+'\n')
print(json.dumps(report));raise SystemExit(0 if report['passed'] else 1)
