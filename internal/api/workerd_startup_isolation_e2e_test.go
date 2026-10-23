package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/api"
	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runner"
	"github.com/clas/nanoflare/internal/runtime"
)

func (w *dynamicPublicationWriter) PrepareWorkerPublication(active []nanoflare.ActiveDeployment, appID string) (nanoflare.PublicationPreparation, error) {
	if w.remote != nil {
		return w.remote.PrepareWorkerPublication(active, appID)
	}
	return w.manager.PrepareWorkerPublication(active, appID)
}

func TestWorkerdStartupIsolatesInvalidRevisions(t *testing.T) {
	binary := os.Getenv("NANOFLARE_TEST_WORKERD")
	if binary == "" {
		t.Skip("set NANOFLARE_TEST_WORKERD to the production-pinned native workerd")
	}
	for _, remote := range []bool{false, true} {
		name := "local"
		if remote {
			name = "remote"
		}
		t.Run(name, func(t *testing.T) {
			store := nanoflare.NewStore()
			if err := store.CreateOrganization(nanoflare.Organization{ID: "isolation-org", Name: "Isolation"}); err != nil {
				t.Fatal(err)
			}
			publication := &dynamicPublicationWriter{}
			service := nanoflare.NewServiceWithObjects(store, publication, newE2EObjectStore())
			control := api.NewRuntimeExecutionServer(service, "isolation-control")
			defer control.Close()
			mux := http.NewServeMux()
			mux.Handle("/internal/runtime/executions/", control)
			private := httptest.NewServer(mux)
			defer private.Close()
			dir := t.TempDir()
			writer := config.NewRuntimeWriter(filepath.Join(dir, "workerd.capnp"), discardTraefik{})
			writer.SetNanoflareRuntimeAddr(strings.TrimPrefix(private.URL, "http://"))
			launcher := &dynamicPublicationLauncher{command: runtime.CommandLauncher{Executable: binary, Experimental: true}}
			manager := runtime.NewDynamicManager(writer, launcher, dir, filepath.Join(dir, "workerd.capnp"), testSocketDir(t), 5*time.Second, 5*time.Second)
			manager.SetRuntimeControlToken("isolation-control")
			publication.manager = manager
			defer manager.Close()
			var ensurer api.RuntimeEnsurer = manager
			var statuses runtime.RuntimeStatusReader = manager
			if remote {
				host := httptest.NewServer(runner.NewServer(manager, "isolation-control"))
				defer host.Close()
				client := runner.NewClient(host.URL, "isolation-control", discardTraefik{})
				publication.remote = client
				ensurer = client
				statuses = client
			}
			sources := []string{`export default {fetch(){return new Response("healthy")}}`, `export default {fetch(request: Request){return new Response("bad")}}`, `throw new Error("private-initialization-detail"); export default {fetch(){return new Response("bad")}}`}
			apps := make([]nanoflare.App, len(sources))
			for i, source := range sources {
				names := []string{"Healthy", "Syntax", "Initialization"}
				app, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "isolation-org", Name: names[i], Hostname: strings.ToLower(names[i]) + ".isolation.test"})
				if err != nil {
					t.Fatal(err)
				}
				apps[i] = app
				// Seed the legacy persisted state directly, bypassing current admission.
				d := nanoflare.Deployment{ID: "legacy-" + app.ID, AppID: app.ID, Entrypoint: "worker.js", Format: "modules", CompatibilityDate: "2026-09-11", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: source}}, CreatedAt: time.Now()}
				if i == 1 {
					historical := d
					historical.ID = "previous-" + app.ID
					historical.Files = []nanoflare.WorkerFile{{Path: "worker.js", Content: `export default {fetch(){return new Response("rolled-back")}}`}}
					if err := store.Activate(historical); err != nil {
						t.Fatal(err)
					}
				}
				if err := store.Activate(d); err != nil {
					t.Fatal(err)
				}
			}
			if err := service.ReconcileRuntime(); err != nil {
				t.Fatal("startup failed", err)
			}
			server := api.NewServerWithRuntime(service, nil, "", nil, nil, ensurer)
			request := func(path string, code int) string {
				t.Helper()
				out := httptest.NewRecorder()
				server.ServeHTTP(out, httptest.NewRequest("GET", path, nil))
				if out.Code != code {
					t.Fatalf("%s: got %d %s, want %d", path, out.Code, out.Body.String(), code)
				}
				return out.Body.String()
			}
			workerPath := func(i int) string { return "/internal/http/workers/" + apps[i].ID + "/" }
			check := func() {
				t.Helper()
				request("/healthz", 200)
				if body := request(workerPath(0), 200); body != "healthy" {
					t.Fatalf("healthy response %q", body)
				}
				for _, i := range []int{1, 2} {
					body := request(workerPath(i), 503)
					if !strings.Contains(body, "worker_initialization_failed") || strings.Contains(body, "private-initialization-detail") {
						t.Fatal("failure details", body)
					}
					state, err := statuses.RuntimeStatuses(context.Background(), apps[i].ID)
					if err != nil || len(state) != 1 || state[0].Reason != "worker_initialization_failed" {
						t.Fatalf("status %#v %v", state, err)
					}
				}
			}
			check()
			detailBody := request("/v1/organizations/isolation-org/workers/"+apps[1].ID, 200)
			var detail nanoflare.WorkerDetail
			if err := json.Unmarshal([]byte(detailBody), &detail); err != nil || len(detail.Runtime) != 1 || detail.Runtime[0].State != "unavailable" {
				t.Fatalf("management status: %s (%v)", detailBody, err)
			}
			metricBody := request("/metrics", 200)
			if !strings.Contains(metricBody, "nanoflare_worker_revision_unavailable") || !strings.Contains(metricBody, `reason="worker_initialization_failed"`) {
				t.Fatal("missing Worker failure metric", metricBody)
			}
			// A separate publication must not be blocked by either quarantined Worker.
			deploy := func(i int, code string) (nanoflare.Deployment, error) {
				return service.Deploy(apps[i].ID, nanoflare.DeployInput{Entrypoint: "worker.js", Format: "modules", CompatibilityDate: "2026-09-11", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: code}}})
			}
			if _, err := deploy(0, sources[0]); err != nil {
				t.Fatal("unrelated deployment blocked", err)
			}
			if _, err := deploy(0, sources[1]); err == nil {
				t.Fatal("invalid new revision activated")
			}
			check()
			// Exercise the watch/recovery path using the actual workerd process.
			launcher.mu.Lock()
			process := launcher.process
			launcher.mu.Unlock()
			if err := process.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				current, err := statuses.RuntimeStatuses(context.Background(), apps[0].ID)
				if launcher.launches.Load() > 1 && err == nil && len(current) == 1 && current[0].State == "ready" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("dispatcher did not recover")
				}
				time.Sleep(20 * time.Millisecond)
			}
			check()
			// Roll back one failed Worker while another remains unavailable.
			if _, err := service.SetDeploymentTraffic(apps[1].ID, []nanoflare.DeploymentTraffic{{ID: "previous-" + apps[1].ID, TrafficPercent: 100}}); err != nil {
				t.Fatal("rollback failed", err)
			}
			if body := request(workerPath(1), 200); body != "rolled-back" {
				t.Fatal(body)
			}
			if _, err := service.SetDeploymentTraffic(apps[1].ID, []nanoflare.DeploymentTraffic{{ID: "legacy-" + apps[1].ID, TrafficPercent: 100}}); err == nil {
				t.Fatal("traffic change activated invalid revision")
			}
			request(workerPath(1), 200)
			for _, i := range []int{1, 2} {
				if _, err := deploy(i, `export default {fetch(){return new Response("recovered")}}`); err != nil {
					t.Fatal("repair failed", err)
				}
				if body := request(workerPath(i), 200); body != "recovered" {
					t.Fatal(body)
				}
				state, err := statuses.RuntimeStatuses(context.Background(), apps[i].ID)
				if err != nil || len(state) != 1 || state[0].State != "ready" {
					t.Fatalf("repair status %#v %v", state, err)
				}
			}
			extra := apps[2]
			if err := store.SetActive(extra.ID, "legacy-"+extra.ID); err != nil {
				t.Fatal(err)
			}
			if err := service.ReconcileRuntime(); err != nil {
				t.Fatal(err)
			}
			if err := service.DeleteApp(extra.ID); err != nil {
				t.Fatal("delete broken Worker", err)
			}
			if state, err := statuses.RuntimeStatuses(context.Background(), extra.ID); err != nil || len(state) != 0 {
				t.Fatalf("deleted Worker status %#v %v", state, err)
			}
			request(workerPath(0), 200)
			// Public diagnostics should remain serializable and contain no exception text.
			state, _ := statuses.RuntimeStatuses(context.Background(), "")
			encoded, _ := json.Marshal(state)
			if strings.Contains(string(encoded), "private-initialization-detail") {
				t.Fatal("status leaked initialization exception")
			}
		})
	}
}
