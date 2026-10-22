package runner

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
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

	if err := NewClient(server.URL, "secret", &fakeTraefikWriter{}).Write(active); err != nil {
		t.Fatal(err)
	}
	if got := manager.prepared[0].App.RuntimeToken; got != "runtime-secret" {
		t.Fatalf("runtime token = %q, want private token transported to runner", got)
	}
}

type fakeManager struct {
	generation string
	routed     []nanoflare.ActiveDeployment
	prepared   []nanoflare.ActiveDeployment
	events     []string
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

func TestPublicationTransportsPinnedSecretsAndTrafficWeights(t *testing.T) {
	manager := &fakeManager{generation: "workerd-000001.capnp", routed: deployments(10001)}
	server := httptest.NewServer(NewServer(manager, "secret"))
	defer server.Close()
	client := NewClient(server.URL, "secret", &fakeTraefikWriter{})
	active := deployments(9001)
	active[0].TrafficPercent = 40
	active[0].App.SecretValues = map[string]string{"TOKEN": "pinned-revision-value"}
	prepared, err := client.PreparePublication(active)
	if err != nil {
		t.Fatal(err)
	}
	if manager.prepared[0].TrafficPercent != 40 || manager.prepared[0].App.SecretValues["TOKEN"] != "pinned-revision-value" {
		t.Fatal("runner lost the revision secret snapshot or traffic weight")
	}
	data, err := json.Marshal(PrepareResponse{Generation: prepared.Generation, Deployments: manager.prepared})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "pinned-revision-value") {
		t.Fatal("runner response exposed private secret values")
	}
	if err = client.CommitPublication(prepared); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerRejectsLegacyArtifactProtocolBeforePreparation(t *testing.T) {
	manager := &fakeManager{}
	server := NewServer(manager, "secret")
	request := httptest.NewRequest("POST", "/v1/generations/prepare", strings.NewReader(`{"deployments":[]}`))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != 409 || len(manager.events) != 0 {
		t.Fatal("legacy protocol prepared an incomplete snapshot")
	}
}
