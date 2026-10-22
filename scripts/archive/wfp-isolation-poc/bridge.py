#!/usr/bin/env python3
"""Bounded warm-reuse, capability-bridge and lease lifecycle experiment."""
import argparse
import concurrent.futures
import hashlib
import json
import pathlib
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

from run import BINARY, Experiment, command

ROOT = pathlib.Path(__file__).resolve().parent


class BridgeExperiment(Experiment):
    def __init__(self, image, binary):
        super().__init__(image, binary)
        self.control = uuid.uuid4().hex
        self.cell_key = uuid.uuid4().hex

    def relay_source(self, default):
        # run.py passes a single prefix argv. Bind fixture credentials in trusted
        # relay source instead; user Worker code never receives these values.
        config = {"control":self.control,"cellKey":self.cell_key,"prefix":self.network+"-"}
        source = (ROOT/"bridge/relay.js").read_text()
        return source.replace("JSON.parse(process.argv[1])",json.dumps(config))

    def __enter__(self):
        super().__enter__()
        dispatcher=(ROOT/"bridge/dispatcher.js").read_text()
        customer=(ROOT/"bridge/customer.js").read_text()
        config=f'''using W = import "/workerd/workerd.capnp";
const config :W.Config = (services=[
 (name="dispatcher",worker=(compatibilityDate="2025-12-10",modules=[(name="dispatcher.js",esModule={json.dumps(dispatcher)})],bindings=[(name="LOADER",workerLoader=()),(name="SOURCE",text={json.dumps(customer)}),(name="BROKER",service="broker")])),
 (name="broker",external=(address="{self.network}-gateway:8080",http=(injectRequestHeaders=[(name="x-poc-cell-key",value="{self.cell_key}")])))
],sockets=[(name="http",address="*:8080",http=(),service="dispatcher")]);'''
        (pathlib.Path(self.temp.name)/"config.capnp").write_text(config)
        return self

    def call(self, base, path="/", subject="alpha", timeout=3):
        req=urllib.request.Request(base+path,headers={"Authorization":"Bearer "+subject,"x-poc-lease":"forged","x-poc-revision":"forged"})
        deadline=time.monotonic()+timeout
        while True:
            try:
                with urllib.request.urlopen(req,timeout=max(.1,deadline-time.monotonic())) as response:
                    return response.status,response.read().decode()
            except urllib.error.HTTPError as error:
                body=error.read().decode()
                if error.code==503 and time.monotonic()<deadline:
                    time.sleep(.05)
                    continue
                raise RuntimeError(f"HTTP {error.code}: {body}") from error

    def control_call(self, path="evidence", **params):
        suffix="?"+urllib.parse.urlencode(params) if params else ""
        request=urllib.request.Request(self.gateway+"/__control/"+path+suffix,headers={"Authorization":"Bearer "+self.control})
        with urllib.request.urlopen(request,timeout=2) as response:return json.load(response)

    def start(self):
        cell=self.network+"-"+str(len(self.containers))
        self.control_call("register",cell=cell,revision="v1")
        cell,url=super().start()
        return cell,url

    def latest(self):
        evidence=self.control_call()
        return (evidence["active"]+evidence["history"])[-1]

    def run(self):
        cell,url=self.start()
        # Sequential then concurrent requests share one revision-cached isolate.
        values=[]
        for index in range(12):values.append(json.loads(self.call(url,subject="alpha" if index%2==0 else "beta")[1]))
        with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
            futures=[(subject,pool.submit(self.call,url,"/",subject)) for subject in ["alpha","beta"]*12]
            for subject,future in futures:
                value=json.loads(future.result()[1])
                if value["identity"]["subject"]!=subject:raise AssertionError(value)
                values.append(value)
        if len({v["instance"] for v in values})!=1 or any(v["revision"]!="v1" or v["checked"]!=200 or v["leaseVisible"] for v in values):raise AssertionError(values)
        self.record("Warm revision reuse with per-request native capability facade","verified",requests=len(values),unique_instances=1,subjects=["alpha","beta"],forged_headers_ignored=True,authority="external fixture broker, reached through typed local RPC facade",global_outbound="null")
        self.call(url,"/remember")
        if not json.loads(self.call(url,"/reuse","beta")[1])["rejected"]:raise AssertionError("old native capability reused")
        self.record("Saved native facade cannot cross request contexts","verified",later_request_rejected=True)
        unsupported=json.loads(self.call(url,"/unsupported")[1])
        if unsupported!={"rawFetchDenied":True,"unknownRpcDenied":True,"undeclaredResourceDenied":True}:raise AssertionError(unsupported)
        limit=json.loads(self.call(url,"/limit")[1])
        if limit["statuses"]!=[200,200,200,429]:raise AssertionError(limit)
        self.record("Explicit transport subset and broker budget","verified",unsupported=unsupported,limit=limit,arbitrary_cross_process_native_rpc=False)
        # Old stream is pinned to v1 while future invocations resolve v2.
        req=urllib.request.Request(url+"/stream",headers={"Authorization":"Bearer alpha"})
        with urllib.request.urlopen(req,timeout=2) as response:
            first=response.readline().decode()
            self.control_call("register",cell=cell,revision="v2")
            new=json.loads(self.call(url)[1]);last=response.readline().decode()
        if first!="v1:first\n" or last!="v1:last\n" or new["revision"]!="v2" or new["identity"]["revision"]!="v2" or new["instance"]==values[0]["instance"]:raise AssertionError({"first":first,"last":last,"new":new})
        self.record("Revision transition preserves stream and authority snapshot","verified",old_stream=first+last,new_revision="v2",unique_revision_instances=2)
        self.call(url,"/background")
        # Wait past background lease expiry and the deliberately late effect.
        time.sleep(.7)
        audit=self.control_call()
        bg=next(x for x in reversed(audit["history"]) if x["path"]=="/background" and x["reason"]=="background_deadline")
        if not any(x["id"]==bg["id"] and x["status"]==410 for x in audit["denied"]):raise AssertionError(audit)
        self.record("Background effects share original bounded lease","verified",accepted_before_deadline=1,late_effect_status=410,reason=bg["reason"])
        # Explicit revocation while the user handler is waiting stops later effects.
        try:self.call(url,"/deadline")
        except (OSError,urllib.error.URLError,RuntimeError):pass
        time.sleep(1.3)
        audit=self.control_call()
        deadline=next(x for x in reversed(audit["history"]) if x["path"]=="/deadline" and x["reason"]=="deadline")
        if deadline["accepted"]!=0:raise AssertionError(deadline)
        self.record("Wall deadline revokes authority independently of CPU","verified",accepted_effects=0,reason="deadline",does_not_interrupt_cpu=True)
        # Close a response after its first chunk; observe trusted cancellation.
        req=urllib.request.Request(url+"/cancel",headers={"Authorization":"Bearer alpha"})
        with urllib.request.urlopen(req,timeout=2) as response:
            if response.readline()!=b"v2:first\n":raise AssertionError("cancel stream")
            active=self.control_call()["active"]
            active_id=next(x["id"] for x in active if x["path"]=="/cancel" and x["state"]=="running")
        end=time.monotonic()+2
        while time.monotonic()<end:
            audit=self.control_call()
            cancelled=next((x for x in audit["history"] if x["id"]==active_id),None)
            if cancelled:break
            time.sleep(.02)
        if not cancelled or cancelled["reason"]!="cancelled":raise AssertionError(audit)
        self.record("Client stream cancellation revokes invocation lease","verified",reason=cancelled["reason"],accepted_before_cancel=cancelled["accepted"])
        req=urllib.request.Request(url+"/stream",headers={"Authorization":"Bearer alpha"})
        with urllib.request.urlopen(req,timeout=2) as response:
            if response.readline()!=b"v2:first\n":raise AssertionError("revoke stream")
            active_id=next(x["id"] for x in self.control_call()["active"] if x["path"]=="/stream" and x["state"]=="running")
            self.control_call("revoke",lease=active_id)
            try:response.read()
            except OSError:pass
        time.sleep(.5)
        audit=self.control_call()
        revoked=next(x for x in audit["history"] if x["id"]==active_id)
        if revoked["reason"]!="explicit_revoke" or revoked["accepted"]!=1:raise AssertionError(revoked)
        for key,want in [("forged",401),(self.cell_key,410)]:
            replay=urllib.request.Request(self.gateway+"/effect/"+active_id,data=b'{"operation":"identify"}',headers={"x-poc-cell-key":key})
            try:
                urllib.request.urlopen(replay,timeout=2).close();raise AssertionError("replay accepted")
            except urllib.error.HTTPError as error:
                if error.code!=want:raise
                error.close()
        self.record("Explicit revocation stops further broker effects","verified",accepted_effects=1,reason="explicit_revoke",forged_transport_status=401,replayed_revoked_lease_status=410)
        time.sleep(.3)
        with concurrent.futures.ThreadPoolExecutor(max_workers=16) as pool:
            held=[pool.submit(self.call,url,"/hold") for _ in range(16)]
            end=time.monotonic()+2
            while time.monotonic()<end:
                active=self.control_call()["active"]
                if len([x for x in active if x["path"]=="/hold"] )==16:break
                time.sleep(.01)
            if len([x for x in active if x["path"]=="/hold"])!=16:raise AssertionError("did not reach admission cap")
            req=urllib.request.Request(url+"/unsupported",headers={"Authorization":"Bearer alpha"})
            try:
                urllib.request.urlopen(req,timeout=1).close();raise AssertionError("admission exceeded")
            except urllib.error.HTTPError as error:
                if error.code!=503:raise
                error.close()
            for future in held:
                if future.result()[0]!=200:raise AssertionError("admitted request failed")
        self.record("Admission rejects excess live invocation leases","verified",admitted=16,excess_status=503)
        # Strict bounded storage and input admission are observed, not just configured.
        time.sleep(.5)
        for _ in range(70):self.call(url,"/unsupported")
        time.sleep(.3)
        audit=self.control_call()
        if len(audit["history"])>64 or len(audit["active"])>16:raise AssertionError("unbounded lease storage")
        self.record("Bounded fixture lease retention","verified",history_entries=len(audit["history"]),history_cap=64,active_cap=audit["capacity"],note="loader eviction/capacity not qualified")


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image",default="nanoflare-e2e/nanoflare-runner:local")
    parser.add_argument("--binary",default=BINARY)
    parser.add_argument("--report",type=pathlib.Path,default=ROOT/"results-step3-bridge.json")
    args=parser.parse_args()
    image=json.loads(command("docker","image","inspect",args.image))[0]
    image_id=image["Id"]
    version=command("docker","run","--rm","--network=none","--entrypoint",args.binary,image_id,"--version")
    binary_hash=command("docker","run","--rm","--network=none","--entrypoint","sha256sum",image_id,args.binary).split()[0]
    report={"runtime":version,"binary_sha256":binary_hash,"image_id":image_id,"architecture":image["Architecture"],"fixture_sha256":{name:hashlib.sha256((ROOT/name).read_bytes()).hexdigest() for name in ["bridge.py","run.py","bridge/dispatcher.js","bridge/customer.js","bridge/relay.js"]},"tested_at_utc":time.strftime("%Y-%m-%dT%H:%M:%SZ",time.gmtime()),"not_verified":["arbitrary native RPC serialization across processes","global fetch with request-specific outbound and warm reuse","production resource adapters and sandbox hardening","per-invocation CPU enforcement","loader eviction, WebSockets and production capacity"]}
    experiment=BridgeExperiment(image_id,args.binary)
    try:
        try:experiment.__enter__();experiment.run()
        finally:
            if sys.exc_info()[0] is not None:
                for name in experiment.containers:
                    logs=subprocess.run(["docker","logs",name],capture_output=True,text=True,timeout=4)
                    print(name,logs.stdout,logs.stderr,file=sys.stderr)
            experiment.__exit__()
        report["suite_exit_code"]=0
    except Exception as error:report["suite_exit_code"]=1;report["error"]=str(error);raise
    finally:
        report["observations"]=experiment.observations
        report["relay_image_id"]=getattr(experiment,"proxy_image",None)
        args.report.write_text(json.dumps(report,indent=2)+"\n")


if __name__=="__main__":main()
