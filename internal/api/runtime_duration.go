package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

type durationTelemetryRecorder interface {
	RecordBatch([]runtime.DurationTraceEvent)
}

type RuntimeDurationServer struct {
	service  *nanoflare.Service
	recorder durationTelemetryRecorder
}

func NewRuntimeDurationServer(service *nanoflare.Service, recorder durationTelemetryRecorder) *RuntimeDurationServer {
	return &RuntimeDurationServer{service: service, recorder: recorder}
}

func (s *RuntimeDurationServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r = authorizeRuntime(w, r, s.service, nanoflare.RuntimeDurations, "")
	if r == nil {
		return
	}
	if r.URL.Path != "/internal/runtime/durations" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("unsupported duration telemetry operation"))
		return
	}
	defer r.Body.Close()
	var events []runtime.DurationTraceEvent
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&events); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid duration telemetry payload"))
		return
	}
	for i := range events {
		events[i].ScriptName = runtimePrincipal(r).App.ID
	}
	if s.recorder != nil {
		s.recorder.RecordBatch(events)
	}
	w.WriteHeader(http.StatusNoContent)
}
