#!/usr/bin/env python3
"""Real scrape-loss alert through Prometheus -> Alertmanager -> local webhook."""
import json,pathlib,subprocess,time,urllib.request
root=pathlib.Path(__file__).resolve().parent
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
def records():
    script='const fs=require("fs");const p="/data/notifications.jsonl";process.stdout.write(fs.existsSync(p)?fs.readFileSync(p,"utf8"):"")'
    raw=subprocess.check_output([str(root/'pilot.sh'),'exec','-T','alert-receiver','node','-e',script],text=True)
    return [json.loads(line) for line in raw.splitlines()]
def wait(check, seconds=120):
    deadline=time.monotonic()+seconds
    while time.monotonic()<deadline:
        result=check()
        if result:return result
        time.sleep(2)
    raise RuntimeError('Alert delivery deadline exceeded')
# Wait for a real successful scrape before injecting the outage.
def scraped():
    with opener.open('http://127.0.0.1:19090/api/v1/targets',timeout=5) as response:data=json.load(response)
    return any(t['labels']['job']=='nanoflared' and t['health']=='up' for t in data['data']['activeTargets'])
wait(scraped);before=len(records());started=time.monotonic()
def delivered(status):
    return next((r for r in records()[before:] if any(a['alertname']=='NanoflarePilotControlUnavailable' and a['status']==status for a in r['alerts'])),None)
try:
    subprocess.run([str(root/'pilot.sh'),'stop','nanoflared'],check=True)
    firing=wait(lambda:delivered('firing'));fired_seconds=round(time.monotonic()-started,2)
finally:
    subprocess.run([str(root/'pilot.sh'),'start','nanoflared'],check=True)
daemon_only_recovery = True
try:
    wait(scraped, 45)
except RuntimeError:
    daemon_only_recovery = False
    subprocess.run([str(root/'pilot.sh'), 'restart', 'nanoflare-runner'], check=True)
    subprocess.run([str(root/'pilot.sh'), 'restart', 'nanoflared'], check=True)
resolved=wait(lambda:delivered('resolved'));wait(scraped)
report={'alert_delivery_passed':True,'daemon_only_recovery_passed':daemon_only_recovery,'route':'real pilot scrape failure -> Prometheus rule -> Alertmanager -> network-local webhook','firing_delivery_seconds':fired_seconds,'total_recovery_delivery_seconds':round(time.monotonic()-started,2),'firing':firing,'resolved':resolved,'limitations':'Local durable receiver only; no external operator notification or on-call acknowledgement qualified.'}
(root.parents[1]/'scripts/wfp-qualification/results-cloudflare-stages-local-pilot-alert-delivery.json').write_text(json.dumps(report,indent=2)+'\n')
print(json.dumps(report))
