package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

type fakeDurationRecorder struct {
	events []runtime.DurationTraceEvent
}

func (r *fakeDurationRecorder) RecordBatch(events []runtime.DurationTraceEvent) {
	r.events = append(r.events, events...)
}

func TestRuntimeDurationServerRecordsTelemetry(t *testing.T) {
	recorder := &fakeDurationRecorder{}
	service, item := telemetryFixture(t)
	server := NewRuntimeDurationServer(service, recorder)
	request := httptest.NewRequest(http.MethodPost, "/internal/runtime/durations", strings.NewReader(`[{"scriptName":"worker-a","eventTimestamp":1720612800000,"durationMs":12}]`))
	response := httptest.NewRecorder()
	request.Header.Set("Authorization", "Bearer "+nanoflare.RuntimeCapability(item, nanoflare.RuntimeDurations, ""))

	server.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if len(recorder.events) != 1 || recorder.events[0].ScriptName != item.App.ID || recorder.events[0].DurationMs != 12 {
		t.Fatalf("events = %#v", recorder.events)
	}
}

func TestRuntimeDurationServerRejectsInvalidPayload(t *testing.T) {
	service, item := telemetryFixture(t)
	server := NewRuntimeDurationServer(service, &fakeDurationRecorder{})
	request := httptest.NewRequest(http.MethodPost, "/internal/runtime/durations", strings.NewReader(`{`))
	response := httptest.NewRecorder()
	request.Header.Set("Authorization", "Bearer "+nanoflare.RuntimeCapability(item, nanoflare.RuntimeDurations, ""))

	server.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func telemetryFixture(t *testing.T) (*nanoflare.Service, nanoflare.ActiveDeployment) {
	t.Helper()
	store := nanoflare.NewStore()
	item := nanoflare.ActiveDeployment{App: nanoflare.App{ID: "actual-worker", RuntimeToken: "root-secret"}, Deployment: nanoflare.Deployment{ID: "revision", AppID: "actual-worker"}}
	if err := store.CreateApp(item.App); err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(item.Deployment); err != nil {
		t.Fatal(err)
	}
	return nanoflare.NewService(store, discardWriter{}), item
}
