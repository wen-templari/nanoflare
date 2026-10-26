#!/usr/bin/env python3
"""Seed durable application data through a trusted Worker D1 binding."""
import json, pathlib, ssl, urllib.request
root=pathlib.Path(__file__).resolve().parent
path=root/'private/recovery.json';state=json.loads(path.read_text())
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPSHandler(context=ssl._create_unverified_context()))
prefix='/v1/organizations/'+state['org']
def api(method,route,body):
    request=urllib.request.Request('http://127.0.0.1:18080'+route,data=json.dumps(body).encode(),method=method,headers={'Authorization':'Bearer '+state['token'],'Content-Type':'application/json'})
    with opener.open(request,timeout=20) as response:
        data=response.read();return json.loads(data) if data else None
if 'd1_worker' not in state:
    db=api('POST',prefix+'/databases',{'name':'pilot-recovery-data'})
    worker=api('POST',prefix+'/workers',{'name':'pilot-d1','hostname':'d1.workers.pilot.localhost'})
    state['d1_worker']=worker['id'];state['d1_database']=db['id'];path.write_text(json.dumps(state))
api('POST',prefix+'/workers/'+state['d1_worker']+'/deployments',{'format':'modules','compatibility_date':'2026-09-11','db':[{'binding':'DB','database_id':state['d1_database']}],'files':[{'path':'worker.js','content':'''export default {async fetch(r,env){await env.DB.prepare("CREATE TABLE IF NOT EXISTS recovery_probe (id INTEGER PRIMARY KEY, value TEXT NOT NULL)").run();if(r.method==="POST")await env.DB.prepare("INSERT INTO recovery_probe VALUES (1, 'retained-sqlite-row') ON CONFLICT(id) DO NOTHING").run();return Response.json(await env.DB.prepare("SELECT id,value FROM recovery_probe ORDER BY id").all())}}'''}]})
request=urllib.request.Request('https://127.0.0.1:18443/',data=b'',method='POST',headers={'Host':'d1.workers.pilot.localhost'})
# Traefik refreshes routes asynchronously.
import time
for attempt in range(30):
    try:
        with opener.open(request,timeout=5) as response:
            result=json.load(response);assert result['results']==[{'id':1,'value':'retained-sqlite-row'}];break
    except Exception:
        if attempt==29:raise
        time.sleep(1)
print('D1 Worker committed and read the retained SQLite row.')
