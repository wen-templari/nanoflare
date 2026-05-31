package platform

import (
	"errors"
	"testing"
)

func TestDeployRestoresPreviousActivationWhenRuntimePublicationFails(t *testing.T) {
	store := NewStore()
	service := NewService(store, &failAfterWriter{remaining: 1})
	if _, err := service.CreateApp(CreateAppInput{ID: "hello", Hostname: "hello.example.com"}); err != nil {
		t.Fatal(err)
	}
	first, err := service.Deploy("hello", DeployInput{BundlePath: "/srv/apps/hello/first.js", CompatibilityDate: "2026-05-31"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Deploy("hello", DeployInput{BundlePath: "/srv/apps/hello/second.js", CompatibilityDate: "2026-05-31"}); err == nil {
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
