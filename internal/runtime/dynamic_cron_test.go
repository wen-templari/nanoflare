package runtime

import (
	"net/http"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestDynamicPublicationPreservesRunningCron(t *testing.T) {
	m, _, active, key := newDynamicControlFixture(t)
	beta := nanoflare.ActiveDeployment{App: nanoflare.App{ID: "beta"}, SocketPath: "/tmp/cron.sock", Deployment: nanoflare.Deployment{ID: "beta-rev", Triggers: nanoflare.TriggerConfig{Crons: []string{"* * * * *"}}}}
	started := make(chan struct{})
	canceled := make(chan struct{})
	finish := make(chan struct{})
	cron := newCronRunner([]nanoflare.ActiveDeployment{beta}, NewOutputBuffer(), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		select {
		case <-r.Context().Done():
			close(canceled)
			return nil, r.Context().Err()
		case <-finish:
			return &http.Response{StatusCode: 204, Body: http.NoBody}, nil
		}
	}), nil)
	go cron.Run()
	cron.runDue(time.Now().UTC())
	<-started
	frozen, _ := nanoflare.FreezeExecution(beta, m.compileOptions)
	bk := frozen.ID()
	m.snapshots[bk] = &dynamicSnapshot{frozen: frozen, registeredSession: m.session, registered: true, validated: true}
	m.committed[dynamicIdentity(beta)] = bk
	m.scheduler = cron
	m.pending = &dynamicPublication{id: "publication", keys: map[string]string{dynamicIdentity(active): key, dynamicIdentity(beta): bk}, active: []nanoflare.ActiveDeployment{active, beta}}
	if err := m.Commit("publication"); err != nil {
		t.Fatal(err)
	}
	defer m.scheduler.Stop()
	defer close(finish)
	select {
	case <-canceled:
		t.Fatal("publishing Alpha canceled Beta's already-running cron request")
	default:
	}
}
