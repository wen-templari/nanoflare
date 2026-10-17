package nanoflare

import (
	"errors"
	"reflect"
	"testing"
)

type publicationTestWriter struct {
	varsTestWriter
	validate func([]ActiveDeployment) error
}

func (w *publicationTestWriter) ValidatePublication(active []ActiveDeployment) error {
	if w.validate != nil {
		return w.validate(active)
	}
	return nil
}

func publicationState(t *testing.T, store *Store, appID string) WorkerPublication {
	t.Helper()
	states, err := store.PublicationStates()
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range states {
		if state.AppID == appID {
			return state
		}
	}
	t.Fatal("missing publication")
	return WorkerPublication{}
}

func TestPublicationValidationPrecedesActivation(t *testing.T) {
	service, store, _, _, app, base := varsFixture(t)
	before := publicationState(t, store, app.ID)
	writer := &publicationTestWriter{}
	writer.validate = func(candidate []ActiveDeployment) error {
		active, err := store.ActiveDeploymentsForApp(app.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(active) != 1 || active[0].Deployment.ID != base.ID {
			t.Fatal("candidate activated before validation")
		}
		selected := activeForApp(candidate, app.ID)
		if selected == nil || selected.Deployment.ID == base.ID || len(selected.Deployment.Files) == 0 || selected.App.SecretValues["SECRET"] != "secret-value" {
			t.Fatalf("candidate missing hydrated code/secrets: %#v", selected)
		}
		return errors.New("invalid candidate")
	}
	service.writer = writer
	if _, err := service.UpdateVarsForOrg("org", app.ID, varsInput(base.ID, "invalid")); err == nil {
		t.Fatal("expected validation failure")
	}
	if after := publicationState(t, store, app.ID); !reflect.DeepEqual(after, before) {
		t.Fatalf("validation mutated publication: %#v", after)
	}
	records, _ := store.ListDeployments()
	if len(records) != 1 {
		t.Fatal("validation persisted candidate")
	}
}

func TestPublicationRecoveryUsesCurrentWeightedTraffic(t *testing.T) {
	service, store, _, _, app, first := varsFixture(t)
	second, err := service.Deploy(app.ID, DeployInput{Files: []WorkerFile{{Path: "worker.js", Content: "export default {fetch(){}}"}}, Entrypoint: "worker.js", Format: "modules", CompatibilityDate: "2025-12-10"})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate two committed traffic changes interrupted before runtime publication.
	if err := store.SetActive(app.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	desired := []DeploymentTraffic{{ID: first.ID, TrafficPercent: 25}, {ID: second.ID, TrafficPercent: 75}}
	if err := store.SetActiveTraffic(app.ID, desired); err != nil {
		t.Fatal(err)
	}
	before := publicationState(t, store, app.ID)
	if before.Generation <= before.PublishedGeneration {
		t.Fatal("missing recoverable pending publication")
	}
	writer := &publicationTestWriter{}
	service.writer = writer
	if err := service.ReconcilePublications(); err != nil {
		t.Fatal(err)
	}
	actual := activeTrafficForApp(writer.last, app.ID)
	percents := map[string]int{}
	for _, item := range actual {
		percents[item.ID] = item.TrafficPercent
	}
	if percents[first.ID] != 25 || percents[second.ID] != 75 {
		t.Fatalf("recovered stale routing: %#v", actual)
	}
	after := publicationState(t, store, app.ID)
	if after.Generation != before.Generation || after.PublishedGeneration != after.Generation {
		t.Fatalf("publication not acknowledged: %#v", after)
	}
	// A stale completion cannot acknowledge a later unpublished generation.
	if err := store.SetActive(app.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.AcknowledgePublication(app.ID, before.Generation); err != nil {
		t.Fatal(err)
	}
	after = publicationState(t, store, app.ID)
	if after.Generation <= after.PublishedGeneration {
		t.Fatal("stale ack consumed newer operation")
	}
}

func TestTrafficPublicationFailureRestoresRuntimeAndGeneration(t *testing.T) {
	service, store, _, writer, app, first := varsFixture(t)
	second, err := service.Deploy(app.ID, DeployInput{Files: []WorkerFile{{Path: "worker.js", Content: "export default {fetch(){}}"}}, CompatibilityDate: "2025-12-10"})
	if err != nil {
		t.Fatal(err)
	}
	before := publicationState(t, store, app.ID)
	writer.failNext = true
	if _, err := service.SetDeploymentTraffic(app.ID, []DeploymentTraffic{{ID: first.ID, TrafficPercent: 100}}); err == nil {
		t.Fatal("expected publication failure")
	}
	if len(writer.last) != 1 || writer.last[0].Deployment.ID != second.ID {
		t.Fatal("failed writer runtime was not restored")
	}
	after := publicationState(t, store, app.ID)
	if after.Generation != before.Generation+2 || after.PublishedGeneration != after.Generation {
		t.Fatalf("rollback must allocate and acknowledge a new generation: %#v", after)
	}
}

func TestPublicationStartupRecoversVarsRollback(t *testing.T) {
	service, store, _, _, app, base := varsFixture(t)
	next := base
	next.ID = "interrupted-vars"
	op := VarsUpdateOperation{AppID: app.ID, Key: "interrupted", Status: "pending", Result: VarsUpdateResult{ID: next.ID, BaseDeploymentID: base.ID}}
	if err := store.BeginVarsUpdate(op, next, base.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishVarsUpdate(app.ID, op.Key, "rolling_back"); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcilePublications(); err != nil {
		t.Fatal(err)
	}
	active, _ := store.ActiveDeploymentsForApp(app.ID)
	if len(active) != 1 || active[0].Deployment.ID != base.ID {
		t.Fatal("startup failed to restore vars base")
	}
	recovered, _, _ := store.VarsUpdate(app.ID, op.Key)
	if recovered.Status != "failed" {
		t.Fatalf("rollback outcome = %s", recovered.Status)
	}
	state := publicationState(t, store, app.ID)
	if state.Generation != state.PublishedGeneration {
		t.Fatal("recovered rollback was not acknowledged")
	}
}
