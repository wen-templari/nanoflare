package runner

import (
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/clas/platform/internal/platform"
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

type fakeManager struct {
	generation string
	routed     []platform.ActiveDeployment
	events     []string
}

func (m *fakeManager) Prepare([]platform.ActiveDeployment) (string, []platform.ActiveDeployment, error) {
	m.events = append(m.events, "prepare")
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
	active []platform.ActiveDeployment
	err    error
}

func (w *fakeTraefikWriter) WriteTraefik(active []platform.ActiveDeployment) error {
	w.active = active
	return w.err
}

func deployments(port int) []platform.ActiveDeployment {
	return []platform.ActiveDeployment{{
		App:        platform.App{ID: "hello", Hostname: "hello.example.com"},
		Deployment: platform.Deployment{ID: "deployment", Port: port},
	}}
}
