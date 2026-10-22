package runner

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

type authorizationManager struct {
	fakeManager
	live bool
	err  error
}

func (m *authorizationManager) RuntimeDeploymentLive(_ context.Context, app, dep string) (bool, error) {
	return m.live && app == "app" && dep == "v1", m.err
}
func TestRemoteRuntimeAuthorizationFailsClosed(t *testing.T) {
	manager := &authorizationManager{live: true}
	server := httptest.NewServer(NewServer(manager, "secret"))
	defer server.Close()
	client := NewClient(server.URL, "secret", &fakeTraefikWriter{})
	got, err := client.RuntimeDeploymentLive(context.Background(), "app", "v1")
	if err != nil || !got {
		t.Fatalf("live=%t err=%v", got, err)
	}
	got, err = client.RuntimeDeploymentLive(context.Background(), "app", "other")
	if err != nil || got {
		t.Fatalf("unknown live=%t err=%v", got, err)
	}
	got, err = NewClient(server.URL, "wrong", &fakeTraefikWriter{}).RuntimeDeploymentLive(context.Background(), "app", "v1")
	if err == nil || got {
		t.Fatal("unauthenticated runner query succeeded")
	}
	manager.err = errors.New("unavailable")
	got, err = client.RuntimeDeploymentLive(context.Background(), "app", "v1")
	if err == nil || got {
		t.Fatal("runner failure authorized caller")
	}
	unsupported := httptest.NewServer(NewServer(&fakeManager{}, "secret"))
	defer unsupported.Close()
	got, err = NewClient(unsupported.URL, "secret", &fakeTraefikWriter{}).RuntimeDeploymentLive(context.Background(), "app", "v1")
	if err == nil || got {
		t.Fatal("unsupported runner authorized caller")
	}
}
