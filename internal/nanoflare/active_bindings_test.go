package nanoflare

import (
	"errors"
	"testing"
)

func TestDeploymentRejectsStaleActiveBindings(t *testing.T) {
	service := NewServiceWithObjects(newObjectBackedRepo(), &recordingWriter{}, newMemoryObjectStore())
	app, err := service.CreateApp(CreateAppInput{Name: "bindings", Hostname: "bindings.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	input := DeployInput{Files: []WorkerFile{{Path: "worker.js", Content: "export default {}"}}, CompatibilityDate: "2025-12-10"}
	empty := []DeploymentTraffic{}
	input.ExpectedActiveTraffic = &empty
	first, err := service.Deploy(app.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Deploy(app.ID, input); !errors.Is(err, ErrActiveBindingsChanged) {
		t.Fatalf("stale deploy error = %v", err)
	}
	current := []DeploymentTraffic{{ID: first.ID, TrafficPercent: 100}}
	input.ExpectedActiveTraffic = &current
	if _, err = service.Deploy(app.ID, input); err != nil {
		t.Fatal(err)
	}
}
