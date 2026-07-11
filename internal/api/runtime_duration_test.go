package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	server := NewRuntimeDurationServer(recorder)
	request := httptest.NewRequest(http.MethodPost, "/internal/runtime/durations", strings.NewReader(`[{"scriptName":"worker-a","eventTimestamp":1720612800000,"durationMs":12}]`))
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if len(recorder.events) != 1 || recorder.events[0].ScriptName != "worker-a" || recorder.events[0].DurationMs != 12 {
		t.Fatalf("events = %#v", recorder.events)
	}
}

func TestRuntimeDurationServerRejectsInvalidPayload(t *testing.T) {
	server := NewRuntimeDurationServer(&fakeDurationRecorder{})
	request := httptest.NewRequest(http.MethodPost, "/internal/runtime/durations", strings.NewReader(`{`))
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}
