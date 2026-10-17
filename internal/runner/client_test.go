package runner

import (
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func TestClientPreparesRoutesAndCommitsGeneration(t *testing.T) {
	manager := &fakeManager{generation: "workerd-000001.capnp", routed: deployments(10001)}
	server := httptest.NewServer(NewServer(manager, "secret"))
	defer server.Close()
	traefik := &fakeTraefikWriter{}

	if err := NewClient(server.URL, "secret", traefik).Write(deployments(9001)); err != nil {
		t.Fatal(err)
	}
	if got, want := manager.events, []string{"prepare", "commit:workerd-000001.capnp"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	if got := traefik.active[0].Deployment.Port; got != 10001 {
		t.Fatalf("Traefik port = %d, want runner-routed port", got)
	}
}

func TestClientAbortsGenerationWhenTraefikPublicationFails(t *testing.T) {
	manager := &fakeManager{generation: "workerd-000001.capnp", routed: deployments(10001)}
	server := httptest.NewServer(NewServer(manager, "secret"))
	defer server.Close()
	traefik := &fakeTraefikWriter{err: errors.New("disk full")}

	if err := NewClient(server.URL, "secret", traefik).Write(deployments(9001)); err == nil {
		t.Fatal("Write() error = nil, want Traefik publication error")
	}
	if got, want := manager.events, []string{"prepare", "abort:workerd-000001.capnp"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
}

func TestServerRejectsInvalidToken(t *testing.T) {
	manager := &fakeManager{generation: "workerd-000001.capnp", routed: deployments(10001)}
	server := httptest.NewServer(NewServer(manager, "secret"))
	defer server.Close()

	if err := NewClient(server.URL, "wrong", &fakeTraefikWriter{}).Write(deployments(9001)); err == nil {
		t.Fatal("Write() error = nil, want authentication error")
	}
	if len(manager.events) != 0 {
		t.Fatalf("events = %#v, want no runtime operations", manager.events)
	}
}

func TestClientTransportsPrivateRuntimeTokenToRunner(t *testing.T) {
	manager := &fakeManager{generation: "workerd-000001.capnp", routed: deployments(10001)}
	server := httptest.NewServer(NewServer(manager, "secret"))
	defer server.Close()
	active := deployments(9001)
	active[0].App.RuntimeToken = "runtime-secret"
	active[0].Deployment.ObjectKey = "deployments/bundle.json"
	active[0].App.SecretValues = map[string]string{"SECRET": "frozen-value"}
	active[0].TrafficPercent = 35

	if err := NewClient(server.URL, "secret", &fakeTraefikWriter{}).Write(active); err != nil {
		t.Fatal(err)
	}
	if got := manager.prepared[0].App.SecretValues["SECRET"]; got != "frozen-value" {
		t.Fatalf("secret = %q", got)
	}
	if manager.prepared[0].Deployment.ObjectKey != "deployments/bundle.json" {
		t.Fatal("bundle object key lost")
	}
	if got := manager.prepared[0].TrafficPercent; got != 35 {
		t.Fatalf("traffic percent = %d", got)
	}
	if got := manager.prepared[0].App.RuntimeToken; got != "runtime-secret" {
		t.Fatalf("runtime token = %q, want private token transported to runner", got)
	}
}

type fakeManager struct {
	validated     []nanoflare.ActiveDeployment
	validationErr error
	generation    string
	routed        []nanoflare.ActiveDeployment
	prepared      []nanoflare.ActiveDeployment
	events        []string
}

func (m *fakeManager) ValidatePublication(active []nanoflare.ActiveDeployment) error {
	m.events = append(m.events, "validate")
	m.validated = active
	return m.validationErr
}

func (m *fakeManager) Prepare(deployments []nanoflare.ActiveDeployment) (string, []nanoflare.ActiveDeployment, error) {
	m.events = append(m.events, "prepare")
	m.prepared = deployments
	return m.generation, m.routed, nil
}

func (m *fakeManager) Commit(generation string) error {
	m.events = append(m.events, "commit:"+generation)
	return nil
}

func (m *fakeManager) Abort(generation string) error {
	m.events = append(m.events, "abort:"+generation)
	return nil
}

type fakeTraefikWriter struct {
	active []nanoflare.ActiveDeployment
	err    error
}

func (w *fakeTraefikWriter) WriteTraefik(active []nanoflare.ActiveDeployment) error {
	w.active = active
	return w.err
}

func deployments(port int) []nanoflare.ActiveDeployment {
	return []nanoflare.ActiveDeployment{{
		App:        nanoflare.App{ID: "hello", Hostname: "hello.example.com"},
		Deployment: nanoflare.Deployment{ID: "deployment", Port: port},
	}}
}

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
