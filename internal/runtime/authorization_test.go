package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeGrantsFollowProcessLifetime(t *testing.T) {
	writer := &fakeWriter{}
	launcher := &fakeLauncher{healthy: true}
	manager := NewManager(writer, launcher, t.TempDir(), filepath.Join(t.TempDir(), "workerd.capnp"), testSocketDir(t), time.Second, time.Second)
	defer manager.Close()
	first := deployments(9001)
	first[0].Deployment.ID = "first"
	second := deployments(9002)
	second[0].Deployment.ID = "second"
	assertLive := func(id string, want bool) {
		t.Helper()
		got, err := manager.RuntimeDeploymentLive(context.Background(), "hello", id)
		if err != nil || got != want {
			t.Fatalf("%s live=%t want=%t err=%v", id, got, want, err)
		}
	}
	if err := manager.Write(first); err != nil {
		t.Fatal(err)
	}
	lease, err := manager.Ensure(context.Background(), first[0])
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	generation, _, err := manager.Prepare(second)
	if err != nil {
		t.Fatal(err)
	}
	assertLive("first", true)
	assertLive("second", true)
	if err := manager.Abort(generation); err != nil {
		t.Fatal(err)
	}
	assertLive("second", false)
	assertLive("first", true)
	crashed, _, err := manager.Prepare(second)
	if err != nil {
		t.Fatal(err)
	}
	launcher.process(2).crash(errors.New("prepared process died"))
	deadline := time.Now().Add(time.Second)
	for {
		live, _ := manager.RuntimeDeploymentLive(context.Background(), "hello", "second")
		if !live {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("crashed pending grant remained live")
		}
		time.Sleep(time.Millisecond)
	}
	if err := manager.Abort(crashed); err != nil {
		t.Fatal(err)
	}
	if err := manager.Write(second); err != nil {
		t.Fatal(err)
	}
	assertLive("first", true)
	assertLive("second", true)
	lease.Release()
	manager.retiring.Wait()
	assertLive("first", false)
	assertLive("second", true)
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	assertLive("second", false)
}

func TestRuntimeInstanceDoesNotReviveAfterReplacement(t *testing.T) {
	manager := NewManager(&fakeWriter{}, &fakeLauncher{healthy: true}, t.TempDir(), filepath.Join(t.TempDir(), "workerd.capnp"), testSocketDir(t), time.Second, time.Second)
	defer manager.Close()
	input := deployments(9001)
	generation, first, err := manager.Prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.Commit(generation); err != nil {
		t.Fatal(err)
	}
	held, err := manager.Ensure(context.Background(), input[0])
	if err != nil {
		t.Fatal(err)
	}
	next, second, err := manager.Prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].RuntimeInstance == "" || first[0].RuntimeInstance == second[0].RuntimeInstance {
		t.Fatal("runtime instances were not unique")
	}
	if err = manager.Commit(next); err != nil {
		t.Fatal(err)
	}
	alive, err := manager.RuntimeInstanceLive(context.Background(), input[0].App.ID, input[0].Deployment.ID, first[0].RuntimeInstance)
	if err != nil || !alive {
		t.Fatal("draining invocation lost its process grant")
	}
	held.Release()
	manager.retiring.Wait()
	alive, err = manager.RuntimeInstanceLive(context.Background(), input[0].App.ID, input[0].Deployment.ID, first[0].RuntimeInstance)
	if err != nil || alive {
		t.Fatal("replacement revived a retired instance grant")
	}
	alive, err = manager.RuntimeInstanceLive(context.Background(), input[0].App.ID, input[0].Deployment.ID, second[0].RuntimeInstance)
	if err != nil || !alive {
		t.Fatal("replacement instance unavailable")
	}
}
