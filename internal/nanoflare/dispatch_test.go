package nanoflare

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDispatchRejectsAndRemovesExpiredLease(t *testing.T) {
	s := NewService(NewStore(), &recordingWriter{})
	s.SetPlatformDispatchEnabled(true)
	// Expired entries are rejected before consulting any caller identity.
	s.dispatch.leases = map[string]*dispatchLease{"nfi1.expired": {Expires: time.Now().Add(-time.Second)}}
	if _, err := s.authorizeInvocation(context.Background(), "nfi1.expired"); !errors.Is(err, ErrInvocationExpired) {
		t.Fatal(err)
	}
	if _, exists := s.dispatch.leases["nfi1.expired"]; exists {
		t.Fatal("expired lease retained")
	}
}

func TestIngressCompletionBoundsBackgroundAndCancellation(t *testing.T) {
	service := NewService(NewStore(), &recordingWriter{})
	service.SetPlatformDispatchEnabled(true)
	root, deadline, err := service.BeginDispatchRoot(ActiveDeployment{App: App{ID: "platform"}, Deployment: Deployment{ID: "revision"}})
	if err != nil {
		t.Fatal(err)
	}
	service.dispatch.leases = map[string]*dispatchLease{"nfi1.child": {Root: root, Deadline: deadline, Expires: deadline}}
	service.EndDispatchRoot(root, false)
	first := service.dispatch.leases["nfi1.child"].Expires
	if first.After(time.Now().Add(2*time.Second)) || !first.Before(deadline) {
		t.Fatal("response completion did not bound background work")
	}
	service.EndDispatchRoot(root, false)
	if service.dispatch.leases["nfi1.child"].Expires != first {
		t.Fatal("repeated completion extended the background window")
	}
	service.EndDispatchRoot(root, true)
	if service.dispatch.leases["nfi1.child"].Expires.After(time.Now()) {
		t.Fatal("disconnect failed to revoke child effects")
	}
	if _, err = service.authorizeInvocation(context.Background(), "nfi1.child"); !errors.Is(err, ErrInvocationExpired) {
		t.Fatal(err)
	}
}

func TestAdmittedDispatchEffectCanceledByIngress(t *testing.T) {
	service := NewService(NewStore(), &recordingWriter{})
	service.SetPlatformDispatchEnabled(true)
	rootID, deadline, err := service.BeginDispatchRoot(ActiveDeployment{App: App{ID: "platform"}, Deployment: Deployment{ID: "revision"}})
	if err != nil {
		t.Fatal(err)
	}
	root := service.dispatch.roots[rootID]
	leaseContext, leaseCancel := context.WithDeadline(root.Context, deadline)
	defer leaseCancel()
	service.dispatch.leases = map[string]*dispatchLease{"nfi1.query": {Root: rootID, Expires: deadline, Context: leaseContext, Cancel: leaseCancel}}
	effectContext, cancel, err := service.DispatchEffectContext(context.Background(), "nfi1.query")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	service.EndDispatchRoot(rootID, true)
	select {
	case <-effectContext.Done():
	case <-time.After(time.Second):
		t.Fatal("admitted effect continued after ingress cancellation")
	}
	if _, _, err = service.DispatchEffectContext(context.Background(), "nfi1.query"); !errors.Is(err, ErrInvocationExpired) {
		t.Fatal(err)
	}
}
