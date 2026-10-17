package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/lib/pq"
)

func TestPostgresVarsActivationAndDurableRetries(t *testing.T) {
	databaseURL := os.Getenv("NANOFLARE_TEST_POSTGRES_URL")
	if databaseURL == "" {
		t.Skip("NANOFLARE_TEST_POSTGRES_URL is not configured")
	}
	admin, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("nanoflare_vars_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + pq.QuoteIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP SCHEMA " + pq.QuoteIdentifier(schema) + " CASCADE")
	dsn := databaseURL
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		dsn, err = pq.ParseURL(dsn)
		if err != nil {
			t.Fatal(err)
		}
	}
	dsn += " search_path=" + schema
	repo, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	app := nanoflare.App{ID: "vars-worker", OrgID: "org", Name: "Vars", Hostname: "vars.example.com", CreatedAt: time.Now().UTC()}
	if err := repo.CreateApp(app); err != nil {
		t.Fatal(err)
	}
	base := nanoflare.Deployment{ID: "base", AppID: app.ID, Files: []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}}, Entrypoint: "worker.js", Format: "modules", CompatibilityDate: "2025-12-10", Port: 9001, CreatedAt: time.Now().UTC()}
	if err := repo.Activate(base); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, id := range []string{"next-a", "next-b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			next := base
			next.ID = id
			next.Port++
			next.Vars = map[string]json.RawMessage{"X": json.RawMessage(`"new"`)}
			op := nanoflare.VarsUpdateOperation{AppID: app.ID, Key: id, Fingerprint: id, Status: "pending", Result: nanoflare.VarsUpdateResult{ID: id, BaseDeploymentID: base.ID, CreatedAt: next.CreatedAt}}
			errs <- repo.BeginVarsUpdate(op, next, base.ID)
		}(id)
	}
	close(start)
	wg.Wait()
	close(errs)
	successes, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, nanoflare.ErrDeploymentConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	active, err := repo.ActiveDeploymentsForApp(app.ID)
	if err != nil || len(active) != 1 {
		t.Fatalf("active=%#v, %v", active, err)
	}
	next := active[0].Deployment
	op, found, err := repo.VarsUpdate(app.ID, next.ID)
	if err != nil || !found || op.Status != "pending" || op.Result.ID != next.ID {
		t.Fatalf("op=%#v, %v, %v", op, found, err)
	}
	if err := repo.FinishVarsUpdate(app.ID, op.Key, "succeeded"); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	saved, found, err := reopened.VarsUpdate(app.ID, op.Key)
	if err != nil || !found || saved.Status != "succeeded" || saved.Result != op.Result {
		t.Fatalf("durable op=%#v, %v, %v", saved, found, err)
	}
	states, err := reopened.PublicationStates()
	if err != nil || len(states) != 1 || states[0].Generation != 2 || states[0].PublishedGeneration != 0 || len(states[0].Traffic) != 1 || states[0].Traffic[0].ID != next.ID {
		t.Fatalf("durable publication=%#v, %v", states, err)
	}
	if err := reopened.AcknowledgePublication(app.ID, 2); err != nil {
		t.Fatal(err)
	}
	// A failed INSERT must roll back the traffic change and request reservation.
	bad := next
	bad.ID = base.ID
	badOp := op
	badOp.Key = "failed-insert"
	badOp.Result.ID = bad.ID
	if err := repo.BeginVarsUpdate(badOp, bad, next.ID); err == nil {
		t.Fatal("expected duplicate deployment error")
	}
	if _, found, err := repo.VarsUpdate(app.ID, badOp.Key); err != nil || found {
		t.Fatalf("failed reservation retained: %v, %v", found, err)
	}
	active, err = repo.ActiveDeploymentsForApp(app.ID)
	if err != nil || len(active) != 1 || active[0].Deployment.ID != next.ID {
		t.Fatalf("failed insertion changed traffic: %#v, %v", active, err)
	}
	states, err = repo.PublicationStates()
	if err != nil || len(states) != 1 || states[0].Generation != 2 || states[0].PublishedGeneration != 2 {
		t.Fatalf("failed transaction altered publication: %#v, %v", states, err)
	}
	if err := repo.SetActiveTraffic(app.ID, []nanoflare.DeploymentTraffic{{ID: base.ID, TrafficPercent: 30}, {ID: next.ID, TrafficPercent: 70}}); err != nil {
		t.Fatal(err)
	}
	states, err = reopened.PublicationStates()
	if err != nil || len(states) != 1 || states[0].Generation != 3 || states[0].PublishedGeneration != 2 || len(states[0].Traffic) != 2 {
		t.Fatalf("traffic publication not durable: %#v, %v", states, err)
	}
	// The publication guard is shared between connections/processes.
	release, err := repo.LockDeploymentUpdates()
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan func(), 1)
	go func() {
		unlock, err := reopened.LockDeploymentUpdates()
		if err != nil {
			t.Error(err)
			acquired <- func() {}
			return
		}
		acquired <- unlock
	}()
	select {
	case unlock := <-acquired:
		unlock()
		release()
		t.Fatal("publication guard was not exclusive")
	case <-time.After(30 * time.Millisecond):
	}
	release()
	select {
	case unlock := <-acquired:
		unlock()
	case <-time.After(5 * time.Second):
		t.Fatal("publication guard was not released")
	}
}
