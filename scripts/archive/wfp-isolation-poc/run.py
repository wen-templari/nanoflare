#!/usr/bin/env python3
"""Opt-in, bounded Linux/Docker failure-domain experiment; never a production runner."""
import argparse
import concurrent.futures
import hashlib
import json
import pathlib
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid

ROOT = pathlib.Path(__file__).resolve().parent
BINARY = "/usr/local/lib/node_modules/workerd/node_modules/@cloudflare/workerd-linux-arm64/bin/workerd"


def command(*args, timeout=12, check=True):
    result = subprocess.run(args, capture_output=True, text=True, timeout=timeout)
    if check and result.returncode:
        raise RuntimeError(f"{args[0:3]} failed: {result.stderr.strip()}")
    return result.stdout.strip()


def request(base, path="/", timeout=1):
    started = time.monotonic()
    try:
        with urllib.request.urlopen(base + path, timeout=timeout) as response:
            return {"status": response.status, "body": response.read().decode(), "elapsed_ms": round((time.monotonic()-started)*1000)}
    except (OSError, urllib.error.URLError) as error:
        return {"error": type(error).__name__, "elapsed_ms": round((time.monotonic()-started)*1000)}


class Experiment:
    def __init__(self, image, binary):
        self.image = image
        self.binary = binary
        self.network = "nf-wfp-step3-" + uuid.uuid4().hex[:12]
        self.containers = []
        self.observations = []
        self.temp = tempfile.TemporaryDirectory(prefix="nf-wfp-step3-")

    def relay_source(self, default):
        return default

    def __enter__(self):
        command("docker", "network", "create", "--internal", "--label", "codex.task=wfp-step3", self.network)
        directory = pathlib.Path(self.temp.name)
        directory.chmod(0o755)
        (directory / "host-secret").write_text("synthetic-host-secret")
        source = (ROOT / "dispatcher.js").read_text()
        customer = (ROOT / "customer.js").read_text()
        broker = (ROOT / "broker.js").read_text()
        config = f'''using W = import "/workerd/workerd.capnp";
const config :W.Config = (services=[
 (name="dispatcher",worker=(compatibilityDate="2025-12-10",modules=[(name="dispatcher.js",esModule={json.dumps(source)}),(name="broker.js",esModule={json.dumps(broker)})],bindings=[(name="COUNTER",service="counter"),(name="LOADER",workerLoader=()),(name="SOURCE",text={json.dumps(customer)})])),
 (name="counter",external=(address="{self.network}-gateway:8080")),
 (name="management-fixture",worker=(compatibilityDate="2025-12-10",modules=[(name="worker.js",esModule="export default {{fetch(){{return new Response('management-decoy')}}}}")]))
],sockets=[(name="http",address="*:8080",http=(),service="dispatcher"),(name="management",address="127.0.0.1:9090",http=(),service="management-fixture")]);'''
        (directory / "config.capnp").write_text(config)
        # A separate trusted HTTP relay reaches cells on their internal-only network.
        # Only the relay has a host port. No Docker socket is mounted anywhere.
        gateway_name = self.network + "-gateway"
        self.containers.append(gateway_name)
        relay_source = r"""const http=require('node:http');
const prefix=process.argv[1];
const leases=new Map(),crypto=require('node:crypto');
http.createServer((req,res)=>{
 if(req.url==='/evidence'){res.setHeader('content-type','application/json');res.end(JSON.stringify([...leases.values()]));return;}
 if(req.url.startsWith('/budget/')){
  const lease=leases.get(req.url.slice('/budget/'.length));
  let body='';req.on('data',c=>body+=c);req.on('end',()=>{
   if(!lease||Date.now()>lease.expires){res.writeHead(403);res.end();return;}
   const call=JSON.parse(body),allowed=lease.accepted<2;
   lease.attempts.push({...call,allowed});
   if(!allowed){res.writeHead(429);res.end('budget exceeded');return;}
   lease.accepted++;
   const url=new URL(call.url);
   if(url.pathname.startsWith('/redirect/')){
    const remaining=Number(url.pathname.split('/').pop());
    if(remaining>0){res.writeHead(302,{location:'http://effect/redirect/'+(remaining-1)});res.end();return;}
   }
   res.writeHead(url.pathname==='/retry'?503:200);res.end('synthetic-effect');
  });return;
 }
 const end=req.url.indexOf('/',1),cell=req.url.slice(1,end),path=req.url.slice(end);
 if(end<0||!cell.startsWith(prefix)||!/^[a-z0-9-]+$/.test(cell)){res.writeHead(403);res.end();return;}
 const id=crypto.randomUUID();
 leases.set(id,{id,cell,path,accepted:0,attempts:[],expires:Date.now()+10000});
 const headers={...req.headers,'x-poc-lease':id};
 const upstream=http.request({hostname:cell,port:8080,path,method:req.method,headers},r=>{res.writeHead(r.statusCode,r.headers);r.pipe(res)});
 upstream.on('error',()=>{if(!res.headersSent)res.writeHead(502);res.end()});
 res.on('close',()=>upstream.destroy());req.pipe(upstream);
}).listen(8080,'0.0.0.0');"""
        proxy_image = command("docker","image","inspect","node:24-slim","--format","{{.Id}}")
        self.proxy_image = proxy_image
        command("docker","run","-d","--name",gateway_name,"--label","codex.task=wfp-step3",
                "--read-only","--user","65532:65532","--cap-drop","ALL","--security-opt","no-new-privileges:true",
                "--memory","64m","--pids-limit","64","-p","127.0.0.1::8080",proxy_image,"node","-e",self.relay_source(relay_source),self.network+"-")
        command("docker","network","connect",self.network,gateway_name)
        self.gateway = "http://"+command("docker","port",gateway_name,"8080/tcp")
        return self

    def __exit__(self, *_):
        for name in reversed(self.containers):
            command("docker", "rm", "-f", name, check=False)
        command("docker", "network", "rm", self.network, check=False)
        self.temp.cleanup()

    def record(self, name, status, **evidence):
        value = {"name": name, "status": status, "evidence": evidence}
        self.observations.append(value)
        print(json.dumps(value), flush=True)

    def start(self):
        name = self.network + "-" + str(len(self.containers))
        # Register before launch, so partial failures are cleaned up too.
        self.containers.append(name)
        command("docker", "run", "-d", "--name", name, "--label", "codex.task=wfp-step3",
                "--network", self.network, "--read-only", "--user", "65532:65532",
                "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
                "--pids-limit", "64", "--memory", "192m", "--memory-swap", "192m",
                "--cpus", "0.5", "--ulimit", "nofile=256:256",
                "--mount", f"type=bind,src={self.temp.name},dst=/fixture,readonly",
                "--tmpfs", "/tmp:rw,noexec,nosuid,size=8m",
                "--entrypoint", self.binary,
                self.image, "serve", "--experimental", "/fixture/config.capnp")
        url = self.gateway + "/" + name
        deadline = time.monotonic() + 6
        while time.monotonic() < deadline:
            if request(url, "/health", timeout=.2).get("status") == 200:
                return name, url
            time.sleep(.05)
        raise RuntimeError("runtime not ready: " + command("docker", "logs", name, check=False))

    def stop(self, name):
        command("docker", "kill", name, check=False)

    def cpu(self, name):
        # PID 1 is the native workerd binary. CPU is not customer Date.now().
        stat = command("docker", "exec", name, "cat", "/proc/1/stat")
        values = stat.rsplit(")", 1)[1].split()
        ticks = int(command("docker", "exec", name, "getconf", "CLK_TCK"))
        return (int(values[11]) + int(values[12])) * 1000 / ticks

    def log_seen(self, name, marker):
        deadline = time.monotonic() + 2
        while time.monotonic() < deadline:
            if marker in command("docker", "logs", name, check=False):
                return True
            # JS console may use stderr, which command() intentionally excludes.
            logs = subprocess.run(["docker", "logs", name], capture_output=True, text=True, timeout=4)
            if marker in logs.stderr:
                return True
            time.sleep(.03)
        return False

    def run(self):
        alpha, aurl = self.start()
        beta, burl = self.start()
        for operation in ["sequential", "parallel", "redirect", "retry", "resource", "rpc", "mixed", "background"]:
            path = "/budget-"+operation
            response = request(aurl,path,timeout=2)
            payload = json.loads(response.get("body","{}"))
            if response.get("status") != 200 or payload.get("leaseVisible") is not False:
                raise AssertionError(response)
            deadline = time.monotonic()+2
            while True:
                events = json.loads(request(self.gateway,"/evidence")["body"])
                lease = next(item for item in reversed(events) if item["path"] == path)
                if len(lease["attempts"]) >= 3 or time.monotonic()>deadline:
                    break
                time.sleep(.03)
            if lease["accepted"] != 2 or len(lease["attempts"]) < 3 or sum(item["allowed"] for item in lease["attempts"]) != 2:
                raise AssertionError(lease)
            self.record("External broker budget: "+operation,"verified", response=payload, accepted_effects=lease["accepted"], attempts=lease["attempts"], limit=2, scope="fixture-issued request lease, synthetic effects; production transports not integrated")
        idle_cpu = self.cpu(alpha)
        idle = request(aurl, "/sleep")
        idle_delta = self.cpu(alpha)-idle_cpu
        if idle.get("status") != 200:
            raise AssertionError(idle)
        self.record("CPU measured separately from elapsed time", "measured", sleep=idle, runtime_process_cpu_ms=idle_delta, accounting="Linux /proc/1/stat user+system ticks; entire workerd process, not per invocation")
        with concurrent.futures.ThreadPoolExecutor() as pool:
            spinning = pool.submit(request, aurl, "/spin", .4)
            if not self.log_seen(alpha, "spin-started"):
                raise AssertionError("CPU loop never started")
            aborted = spinning.result()
            before = self.cpu(alpha)
            time.sleep(.3)
            after = self.cpu(alpha)
            shared_peer = request(aurl, "/?worker=beta", timeout=.3)
            separate_peer = request(burl)
            if "error" not in aborted or "error" not in shared_peer or separate_peer.get("status") != 200 or after-before <= 10:
                raise AssertionError({"abort":aborted,"shared":shared_peer,"separate":separate_peer,"cpu":after-before})
            self.record("Native CPU limit and cancellation do not interrupt work", "blocker", requested_cpu_ms=10, client=aborted, cpu_ms_after_client_abort=after-before, shared_process_peer=shared_peer, separate_process_peer=separate_peer, process_cpu_quota="0.5 cores, rate limit only")
        self.stop(alpha)
        recovery, rurl = self.start()
        if request(rurl).get("status") != 200 or request(burl).get("status") != 200:
            raise AssertionError("recovery or independent peer failed")
        self.record("External kill and explicit replacement contain CPU failure", "verified", unit="entire customer process/container", peer_survived=True, replacement_ready=True, per_invocation_cpu_enforcement=False, automatic_recovery=False)
        # A running, reachable management decoy validates the negative HTTP/TCP probe.
        decoy = command("docker", "exec", recovery, "node", "-e", "fetch('http://127.0.0.1:9090/').then(r=>r.text()).then(console.log)")
        boundary = request(rurl, "/boundary")
        data = json.loads(boundary.get("body", "{}"))
        if decoy != "management-decoy" or not data or data["httpReached"] or data["tcpReached"] or data["hiddenLoader"] or any(v["read"] for v in data["files"].values()):
            raise AssertionError({"decoy":decoy,"boundary":boundary})
        self.record("Worker API denies host files and live management endpoint", "verified", response=data, management_decoy_live=True, scope="API capability checks, not proof against native/V8 escape")
        # Force a bounded allocation in one independently capped container.
        with concurrent.futures.ThreadPoolExecutor() as pool:
            memory = pool.submit(request, rurl, "/memory", 6)
            deadline = time.monotonic() + 6
            state = {}
            while time.monotonic() < deadline:
                state = json.loads(command("docker", "inspect", "--format", "{{json .State}}", recovery))
                if not state["Running"]:
                    break
                time.sleep(.05)
            result = memory.result()
        if state.get("Running") or request(burl).get("status") != 200:
            raise AssertionError({"state":state,"request":result})
        self.record("Memory exhaustion stays in customer container", "verified", state=state, client=result, peer_survived=True, memory_limit_bytes=192*1024*1024, failure="cgroup OOM" if state.get("OOMKilled") else "runtime process exit; inspect exit code", bounded_by="192 MiB RAM+swap cap and 6s parent deadline")
        background, background_url = self.start()
        response = request(background_url, "/background")
        if response.get("status") != 200 or not self.log_seen(background, "background-spin-started"):
            raise AssertionError(response)
        own = request(background_url, "/health", .3)
        peer = request(burl)
        if "error" not in own or peer.get("status") != 200:
            raise AssertionError({"own":own,"peer":peer})
        self.record("waitUntil work outlives response and can block its process", "blocker", initial_response=response, own_health=own, peer_survived=True, consequence="lease/budget must include bounded background work")
        self.stop(background)
        stream, stream_url = self.start()
        with urllib.request.urlopen(stream_url + "/stream", timeout=2) as response:
            first = response.readline().decode()
            self.stop(stream)
            try:
                remainder = response.read().decode()
                stream_error = None
            except (OSError, ValueError) as error:
                remainder, stream_error = "", type(error).__name__
        if first != "first\n" or "last" in remainder or request(burl).get("status") != 200:
            raise AssertionError({"first":first,"remainder":remainder,"error":stream_error})
        self.record("Killing customer cell interrupts its active stream", "verified", first_chunk=first, final_chunk_delivered=False, stream_error=stream_error, peer_survived=True, consequence="separate cells isolate tenants, not concurrent invocations of the same tenant")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default="nanoflare-e2e/nanoflare-runner:local", help="Existing local image, resolved to immutable image ID; never pulled")
    parser.add_argument("--binary", default=BINARY)
    parser.add_argument("--report", type=pathlib.Path, default=ROOT / "results-step3-linux.json")
    args = parser.parse_args()
    image = json.loads(command("docker", "image", "inspect", args.image))[0]
    if image["Architecture"] != "arm64" and args.binary == BINARY:
        parser.error("pass --binary for this image architecture")
    pinned = image["Id"]
    version = command("docker", "run", "--rm", "--network=none", "--entrypoint", args.binary, pinned, "--version")
    binary_hash = command("docker", "run", "--rm", "--network=none", "--entrypoint", "sha256sum", pinned, args.binary).split()[0]
    report = {"runtime":version,"image_id":pinned,"binary_sha256":binary_hash,"architecture":image["Architecture"],"fixture_sha256":{name:hashlib.sha256((ROOT/name).read_bytes()).hexdigest() for name in ["run.py","customer.js","dispatcher.js","broker.js"]},"tested_at_utc":time.strftime("%Y-%m-%dT%H:%M:%SZ",time.gmtime()),"not_verified":["hardened VM boundary or native escape resistance","per-invocation CPU interruption/accounting","native subrequest accounting","cross-process native props/RPC transfer","automatic restart/admission/capacity","redirect/retry/cache and background accounting integration"]}
    experiment = Experiment(pinned,args.binary)
    try:
        try:
            experiment.__enter__()
            experiment.run()
        finally:
            experiment.__exit__()
        report["suite_exit_code"] = 0
    except Exception as error:
        report["suite_exit_code"] = 1
        report["error"] = str(error)
        raise
    finally:
        report["observations"] = experiment.observations
        report["relay_image_id"] = getattr(experiment,"proxy_image",None)
        args.report.write_text(json.dumps(report, indent=2)+"\n")


if __name__ == "__main__":
    main()
