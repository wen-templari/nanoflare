package runner

import (
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"
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
