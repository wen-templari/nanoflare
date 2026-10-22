package nanoflare

import (
	"context"
	"errors"
	"testing"
	"time"
)

type unsafeOutputReader struct {
	lines []WorkerOutputLine
	calls int
}

func (r *unsafeOutputReader) Output(string) []WorkerOutputLine { return r.lines }
func (r *unsafeOutputReader) Query(context.Context, string, WorkerOutputQuery) ([]WorkerOutputLine, error) {
	r.calls++
	return r.lines, nil
}
func TestWorkerOutputCannotExpandAuthorizedWorkerScope(t *testing.T) {
	repo := NewStore()
	service := NewService(repo, &recordingWriter{})
	for _, org := range []string{"org", "foreign"} {
		if err := repo.CreateOrganization(Organization{ID: org, Name: org, UsageLevel: UsageLevelPaid}); err != nil {
			t.Fatal(err)
		}
	}
	a, err := service.CreateApp(CreateAppInput{OrgID: "org", Name: "a", Hostname: "a.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := service.CreateApp(CreateAppInput{OrgID: "org", Name: "b", Hostname: "b.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	input := DeployInput{Files: []WorkerFile{{Path: "worker.js", Content: "export default {}"}}, Format: "modules", CompatibilityDate: "2025-12-10"}
	ad, err := service.Deploy(a.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	bd, err := service.Deploy(b.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-time.Second)
	reader := &unsafeOutputReader{lines: []WorkerOutputLine{
		{AppID: b.ID, DeploymentID: bd.ID, Message: "other customer", Timestamp: now, Level: "info"},
		{AppID: a.ID, DeploymentID: bd.ID, Message: "wrong revision", Timestamp: now, Level: "info"},
		{AppID: a.ID, DeploymentID: ad.ID, Message: "allowed", Timestamp: now, Level: "info"},
	}}
	service.output = reader
	result, err := service.WorkerOutputQueryForOrg(context.Background(), "org", a.ID, WorkerOutputQuery{})
	if err != nil || len(result) != 1 || result[0].Message != "allowed" {
		t.Fatalf("scoping: %#v %v", result, err)
	}
	calls := reader.calls
	if _, err = service.WorkerOutputQueryForOrg(context.Background(), "foreign", a.ID, WorkerOutputQuery{}); !errors.Is(err, ErrAppNotFound) {
		t.Fatalf("foreign org accepted: %v", err)
	}
	if _, err = service.WorkerOutputQueryForOrg(context.Background(), "org", a.ID, WorkerOutputQuery{DeploymentID: bd.ID}); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("foreign revision accepted: %v", err)
	}
	if reader.calls != calls {
		t.Fatal("unauthorized query reached telemetry backend")
	}
	for _, q := range []WorkerOutputQuery{{Limit: 1001}, {Text: string(make([]byte, 513))}, {Level: "debug\"} |= secret"}, {Since: now.Add(-72 * time.Hour), Until: now}} {
		if _, err = service.WorkerOutputQueryForOrg(context.Background(), "org", a.ID, q); !errors.Is(err, ErrTelemetryQuery) {
			t.Fatalf("invalid query: %v", err)
		}
	}
}
