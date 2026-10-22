package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/objects"
	"github.com/clas/nanoflare/internal/runner"
)

// This destructive qualification targets only containers created by this test.
// The control API and PostgreSQL remain outside the runner's cgroup. It is not a
// production pool configuration or an untrusted sandbox qualification.
func TestPrivateDispatchLinuxRecoveryQualification(t *testing.T) {
	if os.Getenv("NANOFLARE_RUN_LINUX_RECOVERY") != "1" {
		t.Skip("opt-in disposable Linux runner/cgroup qualification")
	}
	if os.Getenv("NANOFLARE_TEST_POSTGRES_URL") == "" {
		t.Fatal("Linux recovery qualification requires disposable PostgreSQL")
	}
	memoryMiB := 256
	if value := os.Getenv("NANOFLARE_QUALIFICATION_MEMORY_MIB"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || (n != 256 && n != 1024 && n != 2048) {
			t.Fatal("qualification memory must be 256, 1024 or 2048 MiB")
		}
		memoryMiB = n
	}
	memoryBytes := int64(memoryMiB) << 20
	memoryOption := fmt.Sprintf("%dm", memoryMiB)
	binary, err := filepath.Abs(os.Getenv("NANOFLARE_QUALIFICATION_RUNNER"))
	if err != nil || os.Getenv("NANOFLARE_QUALIFICATION_RUNNER") == "" {
		t.Fatal("set NANOFLARE_QUALIFICATION_RUNNER to a cross-compiled Linux runner")
	}
	content, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	report := map[string]any{"at": time.Now().UTC(), "runner_sha256": hex.EncodeToString(digest[:]), "memory_bytes": memoryBytes, "swap_total_bytes": memoryBytes, "pids_limit": 128, "limitations": []string{"single disposable Linux shared pool; no production sizing or untrusted qualification", "control API remains in the host test process; not a control-process crash drill", "PostgreSQL remains available; storage/network failure drills are separate", "no per-invocation CPU enforcement claim"}}
	t.Cleanup(func() {
		report["passed"] = !t.Failed()
		if path := os.Getenv("NANOFLARE_LINUX_RECOVERY_REPORT"); path != "" {
			data, err := json.MarshalIndent(report, "", "  ")
			if err == nil {
				err = os.WriteFile(path, append(data, '\n'), 0600)
			}
			if err != nil {
				t.Error(err)
			}
		}
	})
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	docker := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("docker %s: %w: %s", args[0], err, out)
		}
		return strings.TrimSpace(string(out)), nil
	}
	image := os.Getenv("NANOFLARE_QUALIFICATION_RUNNER_IMAGE")
	if image == "" {
		image = "nanoflare-e2e/nanoflare-runner:local"
	}
	imageID, err := docker("image", "inspect", image, "--format", "{{.Id}}")
	must(err) // No implicit image pull or build.
	report["image_id"] = imageID
	minioID, err := docker("image", "inspect", "minio/minio:RELEASE.2025-04-22T22-12-26Z", "--format", "{{.Id}}")
	must(err)
	report["object_store_image_id"] = minioID
	prefix := fmt.Sprintf("nf-wfp-qual-%d", time.Now().UnixNano())
	start := func(name, image string, options, arguments []string) {
		t.Helper()
		t.Cleanup(func() {
			_, err := docker("rm", "-f", "-v", name)
			if err != nil {
				t.Error(err)
			}
		})
		args := append([]string{"run", "--pull=never", "-d", "--name", name, "--label", "codex.task=wfp-step10"}, options...)
		args = append(args, image)
		_, err := docker(append(args, arguments...)...)
		must(err)
	}
	// MinIO's command must follow its image, unlike Docker run options.
	minioName := prefix + "-objects"
	t.Cleanup(func() {
		_, err := docker("rm", "-f", "-v", minioName)
		if err != nil {
			t.Error(err)
		}
	})
	_, err = docker("run", "--pull=never", "-d", "--name", minioName, "--label", "codex.task=wfp-step10", "--memory", "256m", "--memory-swap", "256m", "--pids-limit", "128", "-p", "127.0.0.1::9000", "-e", "MINIO_ROOT_USER=qualification", "-e", "MINIO_ROOT_PASSWORD=qualification-disposable", minioID, "server", "/data")
	must(err)
	port := func(name, containerPort string) string {
		t.Helper()
		address, err := docker("port", name, containerPort)
		must(err)
		return strings.TrimSpace(address)
	}
	objectAddress := port(minioName, "9000/tcp")
	client := &http.Client{Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	wait := func(label string, check func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("timed out: %s", label)
	}
	wait("MinIO ready", func() bool {
		r, err := client.Get("http://" + objectAddress + "/minio/health/ready")
		if err != nil {
			return false
		}
		defer r.Body.Close()
		return r.StatusCode == 200
	})
	objectStore, err := objects.Open(context.Background(), objects.MinIOConfig{Endpoint: objectAddress, AccessKey: "qualification", SecretKey: "qualification-disposable", Bucket: "qualification"})
	must(err)
	repo := runtimePostgres(t)
	const orgID = "linux-qualification"
	must(repo.CreateOrganization(nanoflare.Organization{ID: orgID, Name: orgID, UsageLevel: nanoflare.UsageLevelPaid}))
	var service *nanoflare.Service
	var server *Server
	var broker atomic.Value // http.Handler; replaced only after new service initialization.
	control := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/runtime/dispatch/") {
			if handler := broker.Load(); handler != nil {
				handler.(http.Handler).ServeHTTP(w, r)
				return
			}
			http.Error(w, "fixture not initialized", 503)
			return
		}
		http.NotFound(w, r)
	}))
	// Docker Desktop reaches this synthetic broker through host.docker.internal.
	// No control management route or real credential is exposed on this listener.
	control.Listener.Close()
	control.Listener, err = net.Listen("tcp", "0.0.0.0:0")
	must(err)
	control.Start()
	defer control.Close()
	_, brokerPort, err := net.SplitHostPort(control.Listener.Addr().String())
	must(err)
	runnerName := prefix + "-runner"
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	runnerAddress := reserved.Addr().String()
	must(reserved.Close())
	workerd := "/usr/local/lib/node_modules/workerd/node_modules/@cloudflare/workerd-linux-arm64/bin/workerd"
	if value := os.Getenv("NANOFLARE_QUALIFICATION_WORKERD"); value != "" {
		workerd = value
	}
	start(runnerName, imageID, []string{"--init", "--memory", memoryOption, "--memory-swap", memoryOption, "--pids-limit", "128", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user=65532:65532", "--tmpfs", "/tmp:rw,nosuid,nodev,size=64m,mode=1777", "--tmpfs", "/var/lib/nanoflare:rw,nosuid,nodev,size=64m,uid=65532,gid=65532", "--mount", "type=bind,src=" + binary + ",dst=/qualification-runner,readonly", "--entrypoint", "/qualification-runner", "--add-host", "host.docker.internal:host-gateway", "-p", runnerAddress + ":8090", "-e", "NANOFLARE_RUNNER_TOKEN=qualification-disposable"}, []string{"-addr=:8090", "-config-dir=/var/lib/nanoflare/generated", "-runtime-socket-dir=/tmp/sockets", "-nanoflare-runtime-addr=host.docker.internal:" + brokerPort, "-workerd=" + workerd})
	runnerURL := "http://" + port(runnerName, "8090/tcp")
	healthy := func() bool {
		r, err := client.Get(runnerURL + "/healthz")
		if err != nil {
			return false
		}
		defer r.Body.Close()
		return r.StatusCode == 200
	}
	wait("runner health", healthy)
	version, err := docker("exec", runnerName, workerd, "--version")
	must(err)
	report["workerd"] = version
	workerdHash, err := docker("exec", runnerName, "sha256sum", workerd)
	must(err)
	report["workerd_sha256"] = strings.Fields(workerdHash)[0]
	limits, err := docker("exec", runnerName, "sh", "-c", "cat /sys/fs/cgroup/memory.max /sys/fs/cgroup/memory.swap.max /sys/fs/cgroup/pids.max")
	must(err)
	if limits != fmt.Sprintf("%d\n0\n128", memoryBytes) {
		t.Fatalf("cgroup limits not applied: %q", limits)
	}
	report["verified_cgroup_limits"] = limits
	remote := runner.NewClient(runnerURL, "qualification-disposable", dispatchTraefik{})
	newService := func() {
		service = nanoflare.NewServiceWithObjects(repo, remote, objectStore)
		service.SetPlatformDispatchEnabled(true)
		codec, err := nanoflare.NewSecretCodec("0123456789abcdef0123456789abcdef")
		must(err)
		service.SetSecretCodec(codec)
		server = NewServerWithRuntime(service, nil, "", nil, nil, remote)
		broker.Store(http.Handler(NewRuntimeDispatchServer(service, server)))
	}
	newService()
	ns, err := service.CreateDispatchNamespace(orgID, nanoflare.CreateDispatchNamespaceInput{Name: "qualification", CustomerMode: "platform"})
	must(err)
	kv, err := service.CreateKVNamespace(nanoflare.CreateKVNamespaceInput{OrgID: orgID, Name: "durable"})
	must(err)
	_, err = service.SetNamespaceGrants(orgID, ns.ID, nanoflare.NamespaceGrantsInput{ExpectedRevision: 1, Grants: []nanoflare.NamespaceResourceGrant{{Kind: "kv", ResourceID: kv.ID, CustomerID: "stable"}}})
	must(err)
	peerNamespace, err := service.CreateDispatchNamespace(orgID, nanoflare.CreateDispatchNamespaceInput{Name: "peer-namespace", CustomerMode: "platform"})
	must(err)
	peerWorker, err := service.CreateNamespaceScript(orgID, peerNamespace.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "peer", Execution: "dynamic", CustomerID: "peer"})
	must(err)
	_, err = service.DeployForOrg(orgID, peerWorker.ID, nanoflare.DeployInput{Format: "modules", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `let instance;export default {fetch(){instance??=crypto.randomUUID();return Response.json({instance})}}`}}})
	must(err)
	peerPlatform, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: orgID, Name: "peer-platform", Hostname: "peer-qualification.example.test"})
	must(err)
	_, err = service.DeployForOrg(orgID, peerPlatform.ID, nanoflare.DeployInput{Format: "modules", CompatibilityDate: "2025-12-10", DispatchNamespaces: []nanoflare.DispatchBinding{{Binding: "PEER", NamespaceID: peerNamespace.ID}}, Files: []nanoflare.WorkerFile{{Path: "platform.js", Content: `export default {fetch(request,env){return env.PEER.get("peer").fetch(request)}}`}}})
	must(err)
	platform, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: orgID, Name: "platform", Hostname: "qualification.example.test"})
	must(err)
	_, err = service.DeployForOrg(orgID, platform.ID, nanoflare.DeployInput{Format: "modules", CompatibilityDate: "2025-12-10", DispatchNamespaces: []nanoflare.DispatchBinding{{Binding: "CUSTOMERS", NamespaceID: ns.ID}}, Files: []nanoflare.WorkerFile{{Path: "platform.js", Content: `export default {async fetch(request,env){try{return await env.CUSTOMERS.get(new URL(request.url).pathname.slice(1)).fetch(request)}catch(error){return new Response(error.message,{status:error.status||500})}}}`}}})
	must(err)
	worker, err := service.CreateNamespaceScript(orgID, ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "stable", Execution: "dynamic", CustomerID: "stable"})
	must(err)
	input := func(version string) nanoflare.DeployInput {
		return nanoflare.DeployInput{IdempotencyKey: version, Format: "modules", CompatibilityDate: "2025-12-10", KVNamespaces: []nanoflare.KVBinding{{Binding: "STATE", ID: kv.ID}}, Assets: []nanoflare.AssetFile{{Path: "proof.txt", Data: []byte("asset-" + version)}}, Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `let instance;export default {async fetch(request,env){instance??=crypto.randomUUID();let saved=await env.STATE.get("proof");if(!saved){saved="durable";await env.STATE.put("proof",saved)}return Response.json({version:"` + version + `",saved,asset:await(await env.ASSETS.fetch("/proof.txt")).text(),instance})}}`}}}
	}
	first, err := service.DeployForOrg(orgID, worker.ID, input("v1"))
	must(err)
	invoke := func(name string) (int, string) {
		r := httptest.NewRequest("GET", "/internal/http/workers/"+platform.ID+"/"+name, nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	peerInstance := func() string {
		t.Helper()
		request := httptest.NewRequest("GET", "/internal/http/workers/"+peerPlatform.ID+"/peer", nil)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		var value struct {
			Instance string `json:"instance"`
		}
		must(json.Unmarshal(response.Body.Bytes(), &value))
		if response.Code != 200 || value.Instance == "" {
			t.Fatalf("separate namespace peer unavailable: %d %s", response.Code, response.Body.String())
		}
		return value.Instance
	}
	verify := func(version string) string {
		t.Helper()
		status, body := invoke("stable")
		var result struct{ Version, Saved, Asset, Instance string }
		must(json.Unmarshal([]byte(body), &result))
		if status != 200 || result.Version != version || result.Saved != "durable" || result.Asset != "asset-"+version || result.Instance == "" {
			t.Fatalf("stable dispatch %d: %s", status, body)
		}
		return result.Instance
	}
	original := verify("v1")
	if verify("v1") != original {
		t.Fatal("immediate request did not reuse revision")
	}
	report["postgres_kv_and_minio_assets_verified"] = true
	_, err = service.DeployForOrg(orgID, worker.ID, input("v2"))
	must(err)
	verify("v2")
	// Kill the actual runner process tree, preserving durable metadata/objects.
	_, err = docker("kill", "--signal=KILL", runnerName)
	must(err)
	status, _ := invoke("stable")
	if status == 200 {
		t.Fatal("dead runner returned success")
	}
	report["runner_kill_dispatch_status"] = status
	if _, err := service.DispatchNamespacesForOrg(orgID); err != nil {
		t.Fatal("runner crash affected control metadata", err)
	}
	_, err = docker("start", runnerName)
	must(err)
	runnerURL = "http://" + port(runnerName, "8090/tcp")
	wait("restarted runner health", healthy)
	remote = runner.NewClient(runnerURL, "qualification-disposable", dispatchTraefik{})
	newService() // Reconstruct volatile service state using the durable backends.
	// A health response alone does not rehydrate the lost generation.
	status, _ = invoke("stable")
	if status == 200 {
		t.Fatal("new empty runner unexpectedly has the old generation")
	}
	report["empty_runner_dispatch_status"] = status
	reconcile := func() {
		t.Helper()
		must(service.ReconcileRuntime())
	}
	reconcile()
	if verify("v2") == original {
		t.Fatal("killed runner retained a native isolate")
	}
	report["runner_restart_and_explicit_reconciliation_verified"] = true
	_, err = service.SetDeploymentTrafficForOrg(orgID, worker.ID, []nanoflare.DeploymentTraffic{{ID: first.ID, TrafficPercent: 100}})
	must(err)
	verify("v1")
	report["pinned_rollback_after_restart_verified"] = true
	var reconciliations, reconciliationErrors atomic.Int32
	stopReconciliation := remote.StartReconciliation(context.Background(), 100*time.Millisecond, func() error {
		err := service.ReconcileRuntime()
		if err == nil {
			reconciliations.Add(1)
		}
		return err
	}, func(error) { reconciliationErrors.Add(1) })
	defer stopReconciliation()
	wait("initial runner reconciliation", func() bool { return reconciliations.Load() >= 1 })
	verify("v1")
	peerBeforeOOM := peerInstance()
	// Retain native typed-array pages in distinct dynamic isolates until the
	// deliberately small shared cgroup reports a kill. No host allocator is used.
	oomKills := func() int {
		t.Helper()
		events, err := docker("exec", runnerName, "cat", "/sys/fs/cgroup/memory.events")
		must(err)
		for _, line := range strings.Split(events, "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "oom_kill" {
				n, err := strconv.Atoi(fields[1])
				must(err)
				return n
			}
		}
		t.Fatal("cgroup v2 oom_kill accounting unavailable")
		return 0
	}
	before := oomKills()
	loaded := 0
	for loaded < memoryMiB/32+16 && oomKills() == before {
		name := fmt.Sprintf("pressure-%d", loaded)
		app, err := service.CreateNamespaceScript(orgID, ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: name, Execution: "dynamic", CustomerID: name})
		must(err)
		_, err = service.DeployForOrg(orgID, app.ID, nanoflare.DeployInput{Format: "modules", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: `let pages;export default {fetch(){pages??=new Uint8Array(32*1024*1024).fill(7);return new Response(String(pages[pages.length-1]))}}`}}})
		must(err)
		status, body := invoke(name)
		loaded++
		if status != 200 && oomKills() == before {
			t.Fatalf("pressure failed without cgroup kill %d: %s", status, body)
		}
	}
	after := oomKills()
	if after <= before {
		t.Fatal("bounded native workload did not exercise a cgroup OOM kill")
	}
	report["oom_kill_delta"] = after - before
	report["pressure_worker_attempts"] = loaded
	_, err = remote.BootID(context.Background())
	must(err)
	resources, _ := remote.Resources()
	if resources == nil || resources.MemoryLimitBytes != uint64(memoryBytes) || resources.OOMKills < uint64(after) {
		t.Fatalf("authenticated pool telemetry did not report the real cgroup OOM: %+v", resources)
	}
	report["authenticated_pool_oom_counter_verified"] = true
	if _, err := service.DispatchNamespacesForOrg(orgID); err != nil {
		t.Fatal("native OOM affected control metadata", err)
	}
	report["control_metadata_available_after_oom"] = true
	// The manager restarts a killed native child without a runner boot change.
	wait("native child automatic recovery", func() bool {
		status, body := invoke("stable")
		return status == 200 && strings.Contains(body, `"version":"v1"`)
	})
	verify("v1")
	report["native_child_automatic_recovery_verified"] = true
	if peerInstance() == peerBeforeOOM {
		t.Fatal("separate namespace peer did not share the observed physical runtime restart")
	}
	report["separate_namespace_peer_shares_physical_failure_boundary"] = true
	// Keep service/client alive across a full runner restart and require recovery
	// without a manual Write. The fixed host address matches deployment routing.
	priorReconciliations := reconciliations.Load()
	_, err = docker("restart", runnerName)
	must(err)
	if address := port(runnerName, "8090/tcp"); address != runnerAddress {
		t.Fatalf("fixed runner address changed: %s", address)
	}
	wait("post-OOM runner health", healthy)
	wait("runner boot automatic reconciliation", func() bool { return reconciliations.Load() > priorReconciliations })
	verify("v1")
	report["recovery_after_native_cgroup_oom_verified"] = true
	report["runner_boot_automatic_reconciliation_verified"] = true
	report["successful_reconciliations"] = reconciliations.Load()
	report["reconciliation_poll_errors"] = reconciliationErrors.Load()
}
