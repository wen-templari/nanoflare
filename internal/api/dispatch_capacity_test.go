package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goRuntime "runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

type capacityLatency struct {
	Count int     `json:"count"`
	P50   float64 `json:"p50_ms"`
	P95   float64 `json:"p95_ms"`
	P99   float64 `json:"p99_ms"`
	Max   float64 `json:"max_ms"`
}

func capacityQuantiles(values []float64) capacityLatency {
	if len(values) == 0 {
		return capacityLatency{}
	}
	copy := append([]float64{}, values...)
	sort.Float64s(copy)
	q := func(fraction float64) float64 { index := int(float64(len(copy)-1) * fraction); return copy[index] }
	return capacityLatency{Count: len(copy), P50: q(.5), P95: q(.95), P99: q(.99), Max: copy[len(copy)-1]}
}

type capacityLoad struct {
	Concurrency int             `json:"concurrency"`
	Seconds     float64         `json:"elapsed_seconds"`
	Success     int             `json:"success"`
	Rejected    int             `json:"admission_429"`
	Errors      int             `json:"errors"`
	FirstError  string          `json:"first_error,omitempty"`
	RPS         float64         `json:"success_per_second"`
	Latency     capacityLatency `json:"successful_latency"`
}
type capacityStage struct {
	Workers   int             `json:"workers"`
	DeployMS  float64         `json:"added_deployment_ms"`
	First     capacityLatency `json:"first_request_per_new_revision"`
	Warm      capacityLatency `json:"repeat_requests"`
	RSSBytes  int64           `json:"native_rss_bytes"`
	Processes int             `json:"native_processes"`
	Load      []capacityLoad  `json:"load"`
}
type capacityReport struct {
	At                  time.Time       `json:"at"`
	OS                  string          `json:"os"`
	Arch                string          `json:"arch"`
	CPUs                int             `json:"logical_cpus"`
	Go                  string          `json:"go"`
	Workerd             string          `json:"workerd"`
	BinarySHA256        string          `json:"binary_sha256"`
	PhaseSeconds        int             `json:"load_phase_seconds"`
	Stages              []capacityStage `json:"stages"`
	Churn               capacityLatency `json:"churn_publish_ms"`
	ChurnRSS            int64           `json:"churn_rss_bytes"`
	RevisitSameIsolate  bool            `json:"revisit_after_128_revision_scan_same_isolate"`
	RollbackSameIsolate bool            `json:"rollback_same_isolate"`
	Launches            int32           `json:"runtime_launches"`
	Passed              bool            `json:"passed"`
	Limits              []string        `json:"limitations"`
}

// Point-in-time native-process RSS, selected by this fixture's unique config
// directory. Never print unrelated process command lines or treat this as peak RSS.
func capacityRSS(directory string) (int64, int, error) {
	if goRuntime.GOOS == "linux" {
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return 0, 0, err
		}
		var sum int64
		count := 0
		for _, entry := range entries {
			if _, err := strconv.Atoi(entry.Name()); err != nil {
				continue
			}
			// Keep a proc directory handle: a recycled PID must not redirect the
			// second read to a different process after matching its command line.
			root, err := os.OpenRoot(filepath.Join("/proc", entry.Name()))
			if err != nil {
				continue
			}
			command, err := root.ReadFile("cmdline")
			if err != nil || !strings.Contains(string(command), directory) {
				root.Close()
				continue
			}
			stat, err := root.ReadFile("statm")
			root.Close()
			if err != nil {
				continue
			}
			fields := strings.Fields(string(stat))
			if len(fields) < 2 {
				return 0, 0, fmt.Errorf("invalid owned process statm")
			}
			pages, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0, 0, err
			}
			sum += pages * int64(os.Getpagesize())
			count++
		}
		if count == 0 {
			return 0, 0, fmt.Errorf("no owned native process found")
		}
		return sum, count, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,rss=,args=").Output()
	if err != nil {
		return 0, 0, err
	}
	var sum int64
	count := 0
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || !strings.Contains(strings.Join(fields[3:], " "), directory) {
			continue
		}
		rss, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return 0, 0, err
		}
		sum += rss * 1024
		count++
	}
	if count == 0 {
		return 0, 0, fmt.Errorf("no owned native process found")
	}
	return sum, count, nil
}

func TestPrivateDispatchCapacityQualification(t *testing.T) {
	if os.Getenv("NANOFLARE_RUN_CAPACITY") != "1" {
		t.Skip("opt-in bounded native capacity qualification")
	}
	executable, err := exec.LookPath("workerd")
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if len(binary) < 2 || string(binary[:2]) == "#!" {
		t.Fatal("capacity qualification requires a native workerd executable")
	}
	version, err := exec.Command(executable, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	seconds := 2
	if value := os.Getenv("NANOFLARE_CAPACITY_PHASE_SECONDS"); value != "" {
		seconds, err = strconv.Atoi(value)
		if err != nil || seconds < 1 || seconds > 15 {
			t.Fatal("load phase must be 1-15 seconds")
		}
	}
	digest := sha256.Sum256(binary)
	report := capacityReport{At: time.Now().UTC(), OS: goRuntime.GOOS, Arch: goRuntime.GOARCH, CPUs: goRuntime.NumCPU(), Go: goRuntime.Version(), Workerd: strings.TrimSpace(string(version)), BinarySHA256: hex.EncodeToString(digest[:]), PhaseSeconds: seconds, Stages: []capacityStage{}, Limits: []string{"single-host trusted shared pool; no untrusted sandbox/CPU qualification", "memory metadata and object-store fixture; not PostgreSQL/S3 capacity", "first request means first observed revision request, not a cold machine", "RSS samples are not peaks; immutable descriptor eviction does not prove native isolate eviction", "short bounded load slices; not a production soak or maximum capacity"}}
	defer func() {
		report.Passed = !t.Failed()
		if path := os.Getenv("NANOFLARE_CAPACITY_REPORT"); path != "" {
			data, err := json.MarshalIndent(report, "", "  ")
			if err == nil {
				err = os.MkdirAll(filepath.Dir(path), 0755)
			}
			if err == nil {
				err = os.WriteFile(path, append(data, '\n'), 0600)
			}
			if err != nil {
				t.Error(err)
			}
		}
	}()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	repo := nanoflare.NewStore()
	must(repo.CreateOrganization(nanoflare.Organization{ID: "capacity", Name: "capacity", UsageLevel: nanoflare.UsageLevelPaid}))
	var service *nanoflare.Service
	var server *Server
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/runtime/dispatch/") {
			NewRuntimeDispatchServer(service, server).ServeHTTP(w, r)
			return
		}
		w.WriteHeader(404)
	}))
	defer control.Close()
	directory := t.TempDir()
	sockets, err := os.MkdirTemp("/tmp", "nf-capacity-")
	must(err)
	defer os.RemoveAll(sockets)
	writer := config.NewRuntimeWriter(filepath.Join(directory, "workerd.capnp"), dispatchTraefik{})
	writer.SetNanoflareRuntimeAddr(strings.TrimPrefix(control.URL, "http://"))
	launcher := &dispatchCountingLauncher{executable: executable}
	manager := runtime.NewManager(writer, launcher, directory, filepath.Join(directory, "workerd.capnp"), sockets, 5*time.Second, time.Second)
	defer manager.Close()
	service = nanoflare.NewServiceWithObjects(repo, manager, &dispatchObjectStore{apiObjectStore: newAPIObjectStore()})
	service.SetPlatformDispatchEnabled(true)
	codec, err := nanoflare.NewSecretCodec("0123456789abcdef0123456789abcdef")
	must(err)
	service.SetSecretCodec(codec)
	server = NewServerWithRuntime(service, nil, "", nil, nil, manager)
	public := httptest.NewServer(server)
	defer public.Close()
	ns, err := service.CreateDispatchNamespace("capacity", nanoflare.CreateDispatchNamespaceInput{Name: "staging", CustomerMode: "platform"})
	must(err)
	platform, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "capacity", Name: "platform", Hostname: "capacity.local.test"})
	must(err)
	_, err = service.DeployForOrg("capacity", platform.ID, nanoflare.DeployInput{Format: "modules", CompatibilityDate: "2025-12-10", DispatchNamespaces: []nanoflare.DispatchBinding{{Binding: "CUSTOMERS", NamespaceID: ns.ID}}, Files: []nanoflare.WorkerFile{{Path: "platform.js", Content: `export default {async fetch(request,env){if(request.headers.get("Authorization")!=="Bearer fixture")return new Response("denied",{status:403});const name=new URL(request.url).pathname.split("/").filter(Boolean)[0];try{return await env.CUSTOMERS.get(name,{props:{marker:request.headers.get("X-Marker")}}).fetch(request)}catch(error){return new Response(error.message,{status:error.status||500})}}}`}}})
	must(err)
	type sample struct {
		Worker   int    `json:"worker"`
		Version  int    `json:"version"`
		Marker   string `json:"marker"`
		Instance string `json:"instance"`
		Calls    int    `json:"calls"`
	}
	var marker atomic.Int64
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxIdleConns = 128
	transport.MaxIdleConnsPerHost = 64
	transport.MaxConnsPerHost = 64
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 4 * time.Second, Transport: transport}
	invoke := func(index, version int) (sample, int, float64, error) {
		id := strconv.FormatInt(marker.Add(1), 10)
		request, err := http.NewRequest("GET", fmt.Sprintf("%s/internal/http/workers/%s/w%03d", public.URL, platform.ID, index), nil)
		if err != nil {
			return sample{}, 0, 0, err
		}
		request.Header.Set("Authorization", "Bearer fixture")
		request.Header.Set("X-Marker", id)
		started := time.Now()
		response, err := client.Do(request)
		if err != nil {
			completed := time.Now()
			elapsed := float64(completed.Sub(started).Microseconds()) / 1000
			wall := float64(completed.UnixNano()-started.UnixNano()) / 1e6
			return sample{}, 0, elapsed, fmt.Errorf("worker %d request %s: elapsed_ms=%.3f wall_elapsed_ms=%.3f: %w", index, id, elapsed, wall, err)
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		completed := time.Now()
		elapsed := float64(completed.Sub(started).Microseconds()) / 1000
		wall := float64(completed.UnixNano()-started.UnixNano()) / 1e6
		if err != nil {
			return sample{}, response.StatusCode, elapsed, fmt.Errorf("worker %d request %s: elapsed_ms=%.3f wall_elapsed_ms=%.3f: %w", index, id, elapsed, wall, err)
		}
		if response.StatusCode == 429 {
			return sample{}, 429, elapsed, nil
		}
		if response.StatusCode != 200 {
			return sample{}, response.StatusCode, elapsed, fmt.Errorf("worker %d request %s: status %d elapsed_ms=%.3f wall_elapsed_ms=%.3f: %s", index, id, response.StatusCode, elapsed, wall, body)
		}
		var result sample
		if err = json.Unmarshal(body, &result); err != nil {
			return result, 200, elapsed, err
		}
		if result.Worker != index || result.Version != version || result.Marker != id || result.Instance == "" {
			return result, 200, elapsed, fmt.Errorf("wrong revision/props returned")
		}
		return result, 200, elapsed, nil
	}
	apps := []nanoflare.App{}
	firstRevisions := []string{}
	deploy := func(index, version int) nanoflare.Deployment {
		t.Helper()
		source := fmt.Sprintf(`let instance;let calls=0;export default {fetch(request,env,ctx){instance??=crypto.randomUUID();return Response.json({worker:%d,version:%d,marker:ctx.props.marker,instance,calls:++calls})}}`, index, version)
		d, err := service.DeployForOrg("capacity", apps[index].ID, nanoflare.DeployInput{Format: "modules", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: source}}})
		must(err)
		return d
	}
	for _, count := range []int{1, 16, 64, 128} {
		stage := capacityStage{Workers: count, Load: []capacityLoad{}}
		cold, warm := []float64{}, []float64{}
		started := time.Now()
		old := len(apps)
		for index := old; index < count; index++ {
			app, err := service.CreateNamespaceScript("capacity", ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: fmt.Sprintf("w%03d", index), CustomerID: fmt.Sprintf("c%03d", index), Execution: "dynamic"})
			must(err)
			apps = append(apps, app)
			d := deploy(index, 0)
			firstRevisions = append(firstRevisions, d.ID)
		}
		stage.DeployMS = float64(time.Since(started).Microseconds()) / 1000
		// The two-second post-response authority window also consumes bounded
		// admission. Drain previous load and pace calibration below that bound.
		time.Sleep(2100 * time.Millisecond)
		for index := old; index < count; index++ {
			first, status, elapsed, err := invoke(index, 0)
			must(err)
			if status != 200 {
				t.Fatal("first request rejected")
			}
			cold = append(cold, elapsed)
			time.Sleep(25 * time.Millisecond)
			for range 3 {
				next, status, elapsed, err := invoke(index, 0)
				must(err)
				if status != 200 || next.Instance != first.Instance {
					t.Fatal("immediate revision reuse was not observed")
				}
				warm = append(warm, elapsed)
				time.Sleep(25 * time.Millisecond)
			}
		}
		stage.First = capacityQuantiles(cold)
		stage.Warm = capacityQuantiles(warm)
		for _, concurrency := range []int{1, 8, 32} {
			time.Sleep(2100 * time.Millisecond)
			load := capacityLoad{Concurrency: concurrency}
			latencies := []float64{}
			var lock sync.Mutex
			var wg sync.WaitGroup
			var sequence atomic.Int64
			var firstError error
			start := time.Now()
			deadline := start.Add(time.Duration(seconds) * time.Second)
			for range concurrency {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for time.Now().Before(deadline) {
						index := int(sequence.Add(1)-1) % count
						_, status, elapsed, err := invoke(index, 0)
						lock.Lock()
						if err != nil {
							load.Errors++
							if firstError == nil {
								firstError = err
								load.FirstError = err.Error()
							}
						} else if status == 429 {
							load.Rejected++
						} else {
							load.Success++
							latencies = append(latencies, elapsed)
						}
						lock.Unlock()
						// Backpressure is measured, not immediately retried in a
						// busy loop that can exhaust the load generator's ports.
						if status == 429 {
							time.Sleep(10 * time.Millisecond)
						}
					}
				}()
			}
			wg.Wait()
			load.Seconds = time.Since(start).Seconds()
			load.RPS = float64(load.Success) / load.Seconds
			load.Latency = capacityQuantiles(latencies)
			stage.Load = append(stage.Load, load)
			if firstError != nil {
				report.Stages = append(report.Stages, stage)
				t.Fatal(firstError)
			}
		}
		stage.RSSBytes, stage.Processes, err = capacityRSS(directory)
		must(err)
		report.Stages = append(report.Stages, stage)
		t.Logf("workers=%d rss_mib=%.1f first_p95_ms=%.2f warm_p95_ms=%.2f", count, float64(stage.RSSBytes)/(1<<20), stage.First.P95, stage.Warm.P95)
	}
	time.Sleep(2100 * time.Millisecond)
	before, status, _, err := invoke(0, 0)
	must(err)
	if status != 200 {
		t.Fatal("revisit admission rejected")
	}
	for index := 1; index < len(apps); index++ {
		_, status, _, err = invoke(index, 0)
		must(err)
		if status != 200 {
			t.Fatal("descriptor scan rejected")
		}
		time.Sleep(25 * time.Millisecond)
	}
	after, status, _, err := invoke(0, 0)
	must(err)
	if status != 200 {
		t.Fatal("post-scan revisit rejected")
	}
	report.RevisitSameIsolate = before.Instance == after.Instance
	churn := []float64{}
	time.Sleep(2100 * time.Millisecond)
	for version := 1; version <= 16; version++ {
		start := time.Now()
		deploy(0, version)
		churn = append(churn, float64(time.Since(start).Microseconds())/1000)
		_, status, _, err = invoke(0, version)
		must(err)
		if status != 200 {
			t.Fatal("churn request rejected")
		}
	}
	report.Churn = capacityQuantiles(churn)
	report.ChurnRSS, _, err = capacityRSS(directory)
	must(err)
	_, err = service.SetDeploymentTrafficForOrg("capacity", apps[0].ID, []nanoflare.DeploymentTraffic{{ID: firstRevisions[0], TrafficPercent: 100}})
	must(err)
	rolled, status, _, err := invoke(0, 0)
	must(err)
	if status != 200 {
		t.Fatal("rollback request rejected")
	}
	report.RollbackSameIsolate = before.Instance == rolled.Instance
	report.Launches = launcher.count.Load()
	if report.Launches != 1 {
		t.Fatalf("customer churn restarted platform runtime: %d launches", report.Launches)
	}
}
