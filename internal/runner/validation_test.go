package runner

import (
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestClientValidatesCandidateWithoutPublication(t *testing.T) {
	manager := &fakeManager{}
	server := httptest.NewServer(NewServer(manager, "secret"))
	defer server.Close()
	client := NewClient(server.URL, "secret", &fakeTraefikWriter{err: errors.New("must not write routing")})
	active := deployments(0)
	active[0].App.SecretValues = map[string]string{"SECRET": "candidate-secret"}
	if err := client.ValidatePublication(active); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manager.events, []string{"validate"}) {
		t.Fatalf("validation published candidate: %#v", manager.events)
	}
	if manager.validated[0].App.SecretValues["SECRET"] != "candidate-secret" {
		t.Fatal("validation lost secrets")
	}
	manager.validationErr = errors.New("invalid candidate")
	if err := client.ValidatePublication(active); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestClientValidationRequiresAuthentication(t *testing.T) {
	manager := &fakeManager{}
	server := httptest.NewServer(NewServer(manager, "secret"))
	defer server.Close()
	if err := NewClient(server.URL, "wrong", &fakeTraefikWriter{}).ValidatePublication(deployments(0)); err == nil {
		t.Fatal("expected authentication error")
	}
	if len(manager.events) != 0 {
		t.Fatal("unauthenticated validation reached runtime")
	}
}

func TestClientWorkerValidationUsesCompatibilityValidator(t *testing.T) {
	// A compatibility manager's prepare may reuse an already committed generation.
	// Its own validator must decide whether there is pending state to abort.
	manager := &fakeManager{}
	server := httptest.NewServer(NewServer(manager, "secret"))
	defer server.Close()
	client := NewClient(server.URL, "secret", &fakeTraefikWriter{err: errors.New("must not publish routes")})
	active := deployments(0)
	if err := client.ValidateWorkerPublication(active, active[0].App.ID); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manager.events, []string{"validate"}) {
		t.Fatalf("validation prepared or aborted a generation: %#v", manager.events)
	}
	manager.validationErr = errors.New("invalid target")
	if err := client.ValidateWorkerPublication(active, active[0].App.ID); err == nil {
		t.Fatal("validation failure was ignored")
	}
}

type targetValidationManager struct {
	fakeManager
	appID string
}

func (m *targetValidationManager) ValidateWorkerPublication(active []nanoflare.ActiveDeployment, appID string) error {
	m.appID = appID
	return m.fakeManager.ValidatePublication(active)
}

func TestClientWorkerValidationPreservesTarget(t *testing.T) {
	manager := &targetValidationManager{}
	server := httptest.NewServer(NewServer(manager, "secret"))
	defer server.Close()
	client := NewClient(server.URL, "secret", &fakeTraefikWriter{})
	active := deployments(0)
	if err := client.ValidateWorkerPublication(active, "target-worker"); err != nil {
		t.Fatal(err)
	}
	if manager.appID != "target-worker" || !reflect.DeepEqual(manager.events, []string{"validate"}) {
		t.Fatalf("targeted validation lost target or published: %q %#v", manager.appID, manager.events)
	}
}
