package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/clas/nanoflare/internal/runtime"
)

type durationTelemetryRecorder interface {
	RecordBatch([]runtime.DurationTraceEvent)
}

type RuntimeDurationServer struct {
	recorder durationTelemetryRecorder
}

func NewRuntimeDurationServer(recorder durationTelemetryRecorder) *RuntimeDurationServer {
	return &RuntimeDurationServer{recorder: recorder}
}

func (s *RuntimeDurationServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("unsupported duration telemetry operation"))
		return
	}
	defer r.Body.Close()
	var events []runtime.DurationTraceEvent
	if err := json.NewDecoder(r.Body).Decode(&events); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid duration telemetry payload"))
		return
	}
	if s.recorder != nil {
		s.recorder.RecordBatch(events)
	}
	w.WriteHeader(http.StatusNoContent)
}
