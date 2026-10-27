package nanoflare

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExecutionDiagnosticsRequireOwningLiveSessionAndRedactFrozenSecrets(t *testing.T) {
	service := NewService(NewStore(), nil)
	registry := service.Executions()
	registry.RenewResourceSession("owner", time.Now().Add(time.Minute))
	registry.RenewResourceSession("other", time.Now().Add(time.Minute))
	snapshot := executionFixture(t)
	id, err := registry.PrepareResourceGrant(snapshot, "owner", "diagnostic")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.ProcessExecutionDiagnostic(context.Background(), "other", id, "old"); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("foreign session logged: %v", err)
	}
	line, accepted, err := service.ProcessExecutionDiagnostic(context.Background(), "owner", id, "initialization: old")
	if err != nil || !accepted || line.AppID != "a" || line.DeploymentID != "d" || !strings.Contains(line.Message, "[redacted]") {
		t.Fatalf("diagnostic identity/redaction: %#v %v %v", line, accepted, err)
	}
	registry.RenewResourceSession("owner", time.Now().Add(-time.Second))
	if _, _, err = service.ProcessExecutionDiagnostic(context.Background(), "owner", id, "old"); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("expired session logged: %v", err)
	}
}
