package nanoflare

import (
	"errors"
	"reflect"
	"sync"
	"testing"
)

func TestDispatchBindingsRetainedPublication(t *testing.T) {
	s, store, objects, writer, app, base := varsFixture(t)
	s.SetPlatformDispatchEnabled(true)
	org := store.organizations["org"]
	org.UsageLevel = UsageLevelPaid
	store.organizations["org"] = org
	ns, err := s.CreateDispatchNamespace("org", CreateDispatchNamespaceInput{Name: "customers"})
	if err != nil {
		t.Fatal(err)
	}
	input := UpdateDispatchBindingsInput{DispatchNamespaces: []DispatchBinding{{Binding: "CUSTOMERS", NamespaceID: ns.ID}}, ExpectedDeploymentID: base.ID, IdempotencyKey: "bindings", CreatedBy: "owner", CommitMessage: "remove bindings"}
	invalid := input
	invalid.DispatchNamespaces = []DispatchBinding{{Binding: "KV", NamespaceID: ns.ID}}
	if _, err := s.UpdateDispatchBindingsForOrg("org", app.ID, invalid); !errors.Is(err, ErrInvalidDispatchBindingsUpdate) {
		t.Fatalf("retained binding collision accepted: %v", err)
	}
	if _, err := s.UpdateDispatchBindingsForOrg("foreign-org", app.ID, input); !errors.Is(err, ErrAppNotFound) {
		t.Fatalf("cross-org publication accepted: %v", err)
	}
	beforeObjects := len(objects.objects)
	result, err := s.UpdateDispatchBindingsForOrg("org", app.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	live, _, ok, err := s.WorkerRuntimeDeployment(app.ID, "/")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if live.App.SecretValues["SECRET"] != "secret-value" {
		t.Fatal("retained publication changed secret version")
	}
	expected := base
	expected.ID = live.Deployment.ID
	expected.Port = live.Deployment.Port
	expected.CreatedAt = live.Deployment.CreatedAt
	expected.CreatedBy = input.CreatedBy
	expected.CommitMessage = input.CommitMessage
	expected.DispatchNamespaces = input.DispatchNamespaces
	if !reflect.DeepEqual(expected, live.Deployment) {
		t.Fatalf("retained settings changed: got %#v want %#v", live.Deployment, expected)
	}
	if len(objects.objects) != beforeObjects {
		t.Fatal("retained update uploaded artifacts")
	}
	replay, err := s.UpdateDispatchBindingsForOrg("org", app.ID, input)
	if err != nil || replay != result {
		t.Fatalf("replay: %#v %v", replay, err)
	}
	changed := input
	changed.CommitMessage = "different"
	if _, err = s.UpdateDispatchBindingsForOrg("org", app.ID, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("key reuse: %v", err)
	}
	changed = input
	changed.IdempotencyKey = "stale"
	if _, err = s.UpdateDispatchBindingsForOrg("org", app.ID, changed); !errors.Is(err, ErrDeploymentConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	if err = store.SetActiveTraffic(app.ID, []DeploymentTraffic{{ID: base.ID, TrafficPercent: 50}, {ID: result.ID, TrafficPercent: 50}}); err != nil {
		t.Fatal(err)
	}
	changed.ExpectedDeploymentID = result.ID
	if _, err = s.UpdateDispatchBindingsForOrg("org", app.ID, changed); !errors.Is(err, ErrDeploymentConflict) {
		t.Fatalf("split traffic: %v", err)
	}
	if err = store.SetActiveTraffic(app.ID, []DeploymentTraffic{{ID: result.ID, TrafficPercent: 100}}); err != nil {
		t.Fatal(err)
	}
	writer.failNext = true
	changed.IdempotencyKey = "failure"
	if _, err = s.UpdateDispatchBindingsForOrg("org", app.ID, changed); err == nil {
		t.Fatal("expected publication failure")
	}
	live, _, _, err = s.WorkerRuntimeDeployment(app.ID, "/")
	if err != nil || live.Deployment.ID != result.ID {
		t.Fatal("failed publication did not restore active revision")
	}
}

func TestDispatchBindingsConcurrentPublication(t *testing.T) {
	s, _, _, _, app, base := varsFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, key := range []string{"first", "second"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, err := s.UpdateDispatchBindingsForOrg("org", app.ID, UpdateDispatchBindingsInput{DispatchNamespaces: []DispatchBinding{}, ExpectedDeploymentID: base.ID, IdempotencyKey: key})
			results <- err
		}(key)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrDeploymentConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflict)
	}
}
