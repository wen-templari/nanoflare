package runtime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestDynamicRestoreIsolatesWorkerAndKeepsCandidateValidationStrict(t *testing.T) {
	m, _, healthy, _ := newDynamicControlFixture(t)
	broken := healthy
	broken.App.ID, broken.App.Name, broken.Deployment.AppID, broken.Deployment.ID = "broken", "broken", "broken", "bad"
	broken.Deployment.Triggers.Crons = []string{"* * * * *"}
	published := map[string]bool{}
	m.controlClient.Transport = dynamicUnitTransport(func(r *http.Request) (*http.Response, error) {
		body := `{}`
		if strings.HasSuffix(r.URL.Path, "/register") {
			body = dynamicGrantRegistrationResponse(t, r)
		}
		if strings.HasSuffix(r.URL.Path, "/publish") {
			data, _ := io.ReadAll(r.Body)
			published[string(data)] = true
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	m.client.Transport = dynamicUnitTransport(func(r *http.Request) (*http.Response, error) {
		snapshot := m.snapshots[r.Header.Get("X-Nanoflare-Execution")]
		status, header := http.StatusNoContent, http.Header{}
		if snapshot.frozen.ActiveDeployment().Deployment.ID == "bad" {
			status = http.StatusUnprocessableEntity
			header.Set("X-Nanoflare-Validation-Failure", "worker_initialization")
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader("sensitive exception"))}, nil
	})
	active := []nanoflare.ActiveDeployment{broken, healthy}
	id, routed, err := m.PrepareRestore(active)
	if err != nil {
		t.Fatal(err)
	}
	if len(routed) != 2 {
		t.Fatal("failed Worker route was removed")
	}
	if err := m.Commit(id); err != nil {
		t.Fatal(err)
	}
	if m.scheduler != nil {
		t.Fatal("failed revision was scheduled")
	}
	if len(published) != 1 {
		t.Fatalf("published %d grants, want only healthy grant", len(published))
	}
	_, err = m.Ensure(context.Background(), broken)
	var unavailable *WorkerUnavailableError
	if !errors.As(err, &unavailable) || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("unexpected failure: %v", err)
	}
	live, err := m.RuntimeDeploymentLive(context.Background(), broken.App.ID, broken.Deployment.ID)
	if err != nil || live {
		t.Fatalf("broken revision authorized: %v %v", live, err)
	}
	statuses, err := m.RuntimeStatuses(context.Background(), broken.App.ID)
	if err != nil || len(statuses) != 1 || statuses[0].Reason != "worker_initialization_failed" {
		t.Fatalf("status: %#v %v", statuses, err)
	}
	if _, err := m.PrepareWorkerPublication(active, broken.App.ID); !errors.As(err, &unavailable) {
		t.Fatalf("invalid target accepted: %v", err)
	}
	// An unrelated publication may retain the exact failed snapshot.
	p, err := m.PrepareWorkerPublication(active, healthy.App.ID)
	if err != nil {
		t.Fatal("unrelated publication blocked", err)
	}
	if err := m.Abort(p.Generation); err != nil {
		t.Fatal(err)
	}
	// Changing the failed revision's executable identity must validate anew.
	changed := broken
	changed.Deployment.Entrypoint = "changed.js"
	if _, _, err := m.Prepare([]nanoflare.ActiveDeployment{changed, healthy}); !errors.As(err, &unavailable) {
		t.Fatalf("changed failed snapshot accepted: %v", err)
	}
	// A valid replacement recovers without restarting the manager.
	fixed := broken
	fixed.Deployment.ID = "fixed"
	fixed.Deployment.Triggers.Crons = nil
	p, err = m.PrepareWorkerPublication([]nanoflare.ActiveDeployment{fixed, healthy}, broken.App.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Commit(p.Generation); err != nil {
		t.Fatal(err)
	}
	statuses, err = m.RuntimeStatuses(context.Background(), broken.App.ID)
	if err != nil || len(statuses) != 1 || statuses[0].State != "ready" {
		t.Fatalf("recovery: %#v %v", statuses, err)
	}
}

func TestDynamicRestoreDoesNotHideInfrastructureFailure(t *testing.T) {
	for _, code := range []int{401, 403, 404, 500, 502, 422} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			m, _, active, key := newDynamicControlFixture(t)
			m.snapshots[key].validated = false
			m.client.Transport = dynamicUnitTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("secret"))}, nil
			})
			_, _, err := m.PrepareRestore([]nanoflare.ActiveDeployment{active})
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("infrastructure error hidden or leaked: %v", err)
			}
			if m.pending != nil {
				t.Fatal("failed restore left pending publication")
			}
		})
	}
}

func TestDynamicRestoreDoesNotHideValidationTransportFailure(t *testing.T) {
	m, _, active, key := newDynamicControlFixture(t)
	m.snapshots[key].validated = false
	m.client.Transport = dynamicUnitTransport(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })
	_, _, err := m.PrepareRestore([]nanoflare.ActiveDeployment{active})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("transport failure was hidden: %v", err)
	}
	if m.pending != nil {
		t.Fatal("failed restore left a pending publication")
	}
}
