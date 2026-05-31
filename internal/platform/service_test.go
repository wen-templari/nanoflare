package platform

import (
	"errors"
	"testing"
)

func TestDeployRestoresPreviousActivationWhenRuntimePublicationFails(t *testing.T) {
	store := NewStore()
	service := NewService(store, &failAfterWriter{remaining: 1})
	app, err := service.CreateApp(CreateAppInput{Name: "Hello", Hostname: "hello.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Deploy(app.ID, DeployInput{Files: []WorkerFile{{Path: "first.js", Content: "first"}}, CompatibilityDate: "2025-12-10"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Deploy(app.ID, DeployInput{Files: []WorkerFile{{Path: "second.js", Content: "second"}}, CompatibilityDate: "2025-12-10"}); err == nil {
		t.Fatal("Deploy() error = nil, want runtime publication error")
	}
	active, err := store.ActiveDeployments()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].Deployment.ID != first.ID {
		t.Fatalf("active deployments = %#v, want first deployment restored", active)
	}
	if _, err := store.AppIDForCapability(first.CapabilityToken); err != nil {
		t.Fatalf("first capability was not restored: %v", err)
	}
}

type failAfterWriter struct {
	remaining int
}

func (w *failAfterWriter) Write([]ActiveDeployment) error {
	if w.remaining == 0 {
		return errors.New("runtime unavailable")
	}
	w.remaining--
	return nil
}
