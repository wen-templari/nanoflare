package nanoflare

import (
	"errors"
	"testing"
	"time"
)

func resourceGrantSnapshot(t *testing.T) *ExecutionSnapshot {
	t.Helper()
	snapshot, err := FreezeExecution(ActiveDeployment{App: App{ID: "app", OrgID: "org"}, Deployment: Deployment{
		ID: "revision", AppID: "app", ObjectKey: "bundle", KVNamespaces: []KVBinding{{Binding: "KV", ID: "kv"}},
		Databases: []DatabaseBinding{{Binding: "DB", DatabaseID: "db"}}, ObjectStorageBuckets: []ObjectStorageBucketBinding{{Binding: "OBJECTS", BucketID: "bucket"}},
		Assets: []AssetFile{{Path: "index.html", ObjectKey: "asset"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestResourceGrantLifecycleAndOperationPins(t *testing.T) {
	r := NewExecutionRegistry()
	snapshot := resourceGrantSnapshot(t)
	r.RenewResourceSession("session", time.Now().Add(time.Minute))
	grant, err := r.PrepareResourceGrant(snapshot, "session", "registration")
	if err != nil {
		t.Fatal(err)
	}
	if grant == snapshot.ID() {
		t.Fatal("content hash used as authority")
	}
	if replay, err := r.PrepareResourceGrant(snapshot, "session", "registration"); err != nil || replay != grant {
		t.Fatal("registration retry changed grant", err)
	}
	if _, err := r.Admit(grant); err == nil {
		t.Fatal("prepared grant admitted invocation")
	}
	if _, err := r.AcquireResource(grant, ExecutionKV, "kv"); err == nil {
		t.Fatal("prepared grant authorized resources")
	}
	if err := r.PublishResourceGrant("foreign-session", grant); err == nil {
		t.Fatal("foreign session published grant")
	}
	if err := r.RetireResourceGrant("foreign-session", grant); err == nil {
		t.Fatal("foreign session retired grant")
	}
	if err := r.PublishResourceGrant("session", grant); err != nil {
		t.Fatal(err)
	}
	if err := r.PublishResourceGrant("session", grant); err != nil {
		t.Fatal("publish retry failed", err)
	}
	for _, kind := range []ExecutionResourceKind{ExecutionKV, ExecutionDatabase, ExecutionObject, ExecutionAssets} {
		id := map[ExecutionResourceKind]string{ExecutionKV: "kv", ExecutionDatabase: "db", ExecutionObject: "bucket", ExecutionAssets: "revision"}[kind]
		operation, err := r.AcquireResource(grant, kind, id)
		if err != nil {
			t.Fatal("published idle revision lost resource authority", kind, err)
		}
		if _, err := r.Authorize(operation.Token(), kind, id); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Authorize(operation.Token(), kind, "undeclared"); err == nil {
			t.Fatal("operation token widened resource scope")
		}
		operation.Release()
	}
	if _, err := r.AcquireResource(grant, ExecutionKV, "other"); err == nil {
		t.Fatal("undeclared resource authorized")
	}
	first, err := r.Admit(grant)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Admit(grant)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RetireResourceGrant("session", grant); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Admit(grant); !errors.Is(err, ErrExecutionRetired) {
		t.Fatal("retired revision admitted invocation", err)
	}
	first.Release()
	operation, err := r.AcquireResource(grant, ExecutionKV, "kv")
	if err != nil {
		t.Fatal("remaining socket lost authority", err)
	}
	second.Release()
	if _, err := r.AcquireResource(grant, ExecutionKV, "kv"); err == nil {
		t.Fatal("operation pin admitted new operations after last invocation")
	}
	if _, err := r.Authorize(operation.Token(), ExecutionKV, "kv"); err != nil {
		t.Fatal("accepted operation revoked before completion", err)
	}
	if !r.ReferencesDeployment("app", "revision") || !r.ReferencesObject("app", "asset") {
		t.Fatal("operation did not protect deployment objects")
	}
	operation.Release()
	operation.Release()
	if r.ReferencesDeployment("app", "revision") {
		t.Fatal("drained revision retained objects")
	}
	if _, err := r.Authorize(operation.Token(), ExecutionKV, "kv"); err == nil {
		t.Fatal("completed operation retained authority")
	}
	if err := r.PublishResourceGrant("session", grant); err == nil {
		t.Fatal("revoked grant revived")
	}
	if _, err := r.PrepareResourceGrant(snapshot, "session", "registration"); err == nil {
		t.Fatal("old registration revived grant")
	}
	replacement, err := r.PrepareResourceGrant(snapshot, "session", "replacement")
	if err != nil {
		t.Fatal(err)
	}
	if replacement == grant {
		t.Fatal("identical code reused retired generation")
	}
	if err := r.PublishResourceGrant("session", replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AcquireResource(grant, ExecutionKV, "kv"); err == nil {
		t.Fatal("republication revived old capability")
	}
}

func TestResourceGrantSessionIsolationExpiryAndCanceledRegistration(t *testing.T) {
	r := NewExecutionRegistry()
	snapshot := resourceGrantSnapshot(t)
	for _, session := range []string{"first", "second"} {
		r.RenewResourceSession(session, time.Now().Add(time.Minute))
	}
	first, _ := r.PrepareResourceGrant(snapshot, "first", "same-registration")
	second, _ := r.PrepareResourceGrant(snapshot, "second", "same-registration")
	if first == second {
		t.Fatal("sessions shared authority")
	}
	if err := r.PublishResourceGrant("first", first); err != nil {
		t.Fatal(err)
	}
	if err := r.PublishResourceGrant("second", second); err != nil {
		t.Fatal(err)
	}
	if lease, err := r.AdmitResourceGrant("second", first); err == nil {
		lease.Release()
		t.Fatal("another session admitted a foreign resource grant")
	}
	invocation, err := r.AdmitResourceGrant("first", first)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := r.AcquireResource(first, ExecutionKV, "kv")
	if err != nil {
		t.Fatal(err)
	}
	// Expiry is enforced at authorization, even before the listener's janitor.
	r.RenewResourceSession("first", time.Now().Add(-time.Second))
	if _, err := r.AcquireResource(first, ExecutionKV, "kv"); err == nil {
		t.Fatal("expired session authorized resource")
	}
	if _, err := r.Admit(first); err == nil {
		t.Fatal("expired session admitted work")
	}
	if _, err := r.Authorize(invocation.Token(), ExecutionKV, "kv"); err == nil {
		t.Fatal("expired invocation retained authority")
	}
	if pin, err := invocation.Pin(); err == nil {
		pin.Release()
		t.Fatal("expired invocation acquired another pin")
	}
	r.RevokeResourceSession("first")
	invocation.Release()
	if _, err := r.Authorize(operation.Token(), ExecutionKV, "kv"); err != nil {
		t.Fatal("accepted operation interrupted by session cleanup", err)
	}
	operation.Release()
	remaining, err := r.AcquireResource(second, ExecutionKV, "kv")
	if err != nil {
		t.Fatal("one session revoked another's grant", err)
	}
	remaining.Release()
	canceled := ResourceGrantID("second", "uncertain-registration")
	if err := r.RetireResourceGrant("second", canceled); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PrepareResourceGrant(snapshot, "second", "uncertain-registration"); err == nil {
		t.Fatal("late registration escaped cancellation")
	}
	r.RevokeResourceSession("second")
	if r.ReferencesDeployment("app", "revision") {
		t.Fatal("session revocation leaked snapshots")
	}
}
