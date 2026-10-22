package runner

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunnerBootStatusRequiresAuthentication(t *testing.T) {
	handler := NewServer(&fakeManager{}, "secret")
	server := httptest.NewServer(handler)
	defer server.Close()
	if _, err := NewClient(server.URL, "wrong", nil).BootID(context.Background()); err == nil {
		t.Fatal("unauthenticated boot status accepted")
	}
	client := NewClient(server.URL, "secret", nil)
	first, err := client.BootID(context.Background())
	if err != nil || first == "" {
		t.Fatalf("boot identity: %q %v", first, err)
	}
	second, err := client.BootID(context.Background())
	if err != nil || second != first {
		t.Fatal("identity changed within a runner process")
	}
	other := NewServer(&fakeManager{}, "secret")
	if other.bootID == first {
		t.Fatal("new runner reused boot identity")
	}
}

func TestRunnerReconciliationRetriesChangedBootAndStops(t *testing.T) {
	var boot, polls atomic.Int32
	boot.Store(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(401)
			return
		}
		polls.Add(1)
		fmt.Fprintf(w, `{"boot_id":"boot-%d"}`, boot.Load())
	}))
	defer server.Close()
	client := NewClient(server.URL, "secret", nil)
	var attempts, failures atomic.Int32
	var fail atomic.Bool
	stop := client.StartReconciliation(context.Background(), 5*time.Millisecond, func() error {
		attempts.Add(1)
		if fail.Swap(false) {
			return errors.New("publication unavailable")
		}
		return nil
	}, func(error) { failures.Add(1) })
	defer stop()
	wait := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("reconciliation did not progress")
	}
	wait(func() bool { return polls.Load() >= 3 })
	if attempts.Load() != 1 {
		t.Fatal("unchanged boot republished runtime")
	}
	fail.Store(true)
	boot.Store(2)
	wait(func() bool { return attempts.Load() >= 3 && polls.Load() >= 6 })
	if failures.Load() != 1 {
		t.Fatal("failed publication was not reported/retried")
	}
	stop()
	before := attempts.Load()
	boot.Store(3)
	time.Sleep(20 * time.Millisecond)
	if attempts.Load() != before {
		t.Fatal("reconciliation continued after stop")
	}
}

func TestRunnerBootStatusRejectsMissingIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{}`) }))
	defer server.Close()
	if _, err := NewClient(server.URL, "secret", nil).BootID(context.Background()); err == nil {
		t.Fatal("missing identity accepted")
	}
}
