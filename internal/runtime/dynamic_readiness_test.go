package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestDynamicPendingPublicationWaitsWithoutBlockingPeers(t *testing.T) {
	m, _, active, _ := newDynamicControlFixture(t)
	beta := active
	beta.App.ID = "beta"
	beta.Deployment.AppID = "beta"
	betaSnapshot, err := nanoflare.FreezeExecution(beta, m.compileOptions)
	if err != nil {
		t.Fatal(err)
	}
	betaKey := betaSnapshot.ID()
	m.snapshots[betaKey] = &dynamicSnapshot{frozen: betaSnapshot, registered: true, registeredSession: m.session}
	m.committed[dynamicIdentity(beta)] = betaKey
	next := active
	next.Deployment.ID = "next"
	snapshot, err := nanoflare.FreezeExecution(next, m.compileOptions)
	if err != nil {
		t.Fatal(err)
	}
	key := snapshot.ID()
	m.snapshots[key] = &dynamicSnapshot{frozen: snapshot, registered: true, registeredSession: m.session}
	m.pending = &dynamicPublication{id: "pending", done: make(chan struct{}), keys: map[string]string{dynamicIdentity(next): key, dynamicIdentity(beta): betaKey}, active: []nanoflare.ActiveDeployment{next, beta}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	admitted := make(chan error, 1)
	go func() {
		worker, err := m.Ensure(ctx, next)
		if err == nil {
			worker.Release()
		}
		admitted <- err
	}()
	select {
	case err := <-admitted:
		t.Fatalf("candidate admitted before commit: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	peer, err := m.Ensure(ctx, beta)
	if err != nil {
		t.Fatal("pending publication blocked peer:", err)
	}
	peer.Release()
	if err := m.Commit("pending"); err != nil {
		t.Fatal(err)
	}
	if err := <-admitted; err != nil {
		t.Fatal("committed candidate was unavailable:", err)
	}
}

func TestDynamicPendingPublicationWaitRespectsCancellation(t *testing.T) {
	m, _, active, _ := newDynamicControlFixture(t)
	active.Deployment.ID = "next"
	m.pending = &dynamicPublication{id: "pending", done: make(chan struct{}), keys: map[string]string{dynamicIdentity(active): "candidate"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Ensure(ctx, active); err != context.Canceled {
		t.Fatalf("pending admission ignored cancellation: %v", err)
	}
}

func TestDynamicValidationOfPrivateRevisionsDoesNotStartRuntime(t *testing.T) {
	m := NewDynamicManager(nil, nil, "", "", "", time.Second, time.Second)
	defer m.Close()
	active := nanoflare.ActiveDeployment{App: nanoflare.App{ID: "private", Platform: &nanoflare.WorkerPlatform{Execution: "dynamic", Exposure: "private"}}, Deployment: nanoflare.Deployment{ID: "revision"}}
	if err := m.ValidatePublication([]nanoflare.ActiveDeployment{active}); err != nil {
		t.Fatal(err)
	}
	if m.process != nil || m.broker != nil {
		t.Fatal("private-only validation started the shared runtime")
	}
}
