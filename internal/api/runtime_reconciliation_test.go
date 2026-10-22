package api

import (
	"sync"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type reconciliationWriter func([]nanoflare.ActiveDeployment) error

func (f reconciliationWriter) Write(active []nanoflare.ActiveDeployment) error { return f(active) }

// Two service instances share only their repository publication guard, as two
// control-plane processes would. Reconciliation must not publish a snapshot
// after a concurrent deletion has already removed the Worker.
func TestRuntimeReconciliationSerializesWithDeletion(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var repo nanoflare.Repository = nanoflare.NewStore()
			if backend == "postgres" {
				repo = runtimePostgres(t)
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(repo.CreateOrganization(nanoflare.Organization{ID: "reconcile", Name: "reconcile"}))
			writer := &managedRecordingWriter{}
			service, _ := managedService(t, repo, writer)
			app, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: "reconcile", Name: "stable", Hostname: "reconcile.example.test"})
			must(err)
			_, err = service.DeployForOrg("reconcile", app.ID, nanoflare.DeployInput{IdempotencyKey: "v1", CompatibilityDate: "2025-12-10", Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}})
			must(err)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			restarted, _ := managedService(t, repo, reconciliationWriter(func(active []nanoflare.ActiveDeployment) error {
				close(entered)
				<-release
				return writer.Write(active)
			}))
			reconciled, deleted := make(chan error, 1), make(chan error, 1)
			go func() { reconciled <- restarted.ReconcileRuntime() }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("reconciliation did not reach publication")
			}
			go func() { deleted <- service.DeleteAppForOrg("reconcile", app.ID) }()
			select {
			case err := <-deleted:
				t.Fatalf("delete bypassed reconciliation snapshot guard: %v", err)
			case <-time.After(30 * time.Millisecond):
			}
			unblock()
			select {
			case err := <-reconciled:
				must(err)
			case <-time.After(3 * time.Second):
				t.Fatal("reconciliation stuck")
			}
			select {
			case err := <-deleted:
				must(err)
			case <-time.After(3 * time.Second):
				t.Fatal("deletion stuck")
			}
			writer.mu.Lock()
			defer writer.mu.Unlock()
			if len(writer.writes) == 0 || len(writer.writes[len(writer.writes)-1]) != 0 {
				t.Fatal("stale snapshot restored a deleted Worker")
			}
		})
	}
}
