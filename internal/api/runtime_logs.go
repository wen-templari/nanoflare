package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type RuntimeLogAppender interface {
	AppendScoped(string, string, string, string)
}
type RuntimeLogServer struct {
	service *nanoflare.Service
	output  RuntimeLogAppender
}

func NewRuntimeLogServer(service *nanoflare.Service, output RuntimeLogAppender) *RuntimeLogServer {
	return &RuntimeLogServer{service, output}
}
func (s *RuntimeLogServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r = authorizeRuntime(w, r, s.service, nanoflare.RuntimeLogs, "")
	if r == nil {
		return
	}
	if r.URL.Path != "/internal/runtime/logs" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("unsupported log operation"))
		return
	}
	defer r.Body.Close()
	var events []struct {
		Logs []struct {
			Level   string `json:"level"`
			Message []any  `json:"message"`
		} `json:"logs"`
		Exceptions []struct {
			Name    string `json:"name"`
			Message string `json:"message"`
		} `json:"exceptions"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&events); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid log payload"))
		return
	}
	appendLine := func(level, message string) {
		line, accepted, err := s.service.ProcessRuntimeLog(r.Context(), bearerToken(r), level, message)
		if err == nil && accepted {
			appendRuntimeLog(s.output, line)
		}
	}
	for _, event := range events {
		for _, log := range event.Logs {
			level := log.Level
			switch level {
			case "debug", "info", "warn", "error":
			default:
				level = "info"
			}
			parts := make([]string, 0, len(log.Message))
			for _, value := range log.Message {
				if text, ok := value.(string); ok {
					parts = append(parts, text)
				} else {
					data, _ := json.Marshal(value)
					parts = append(parts, string(data))
				}
			}
			appendLine(level, strings.Join(parts, " "))
		}
		for _, exception := range event.Exceptions {
			appendLine("error", fmt.Sprintf("%s: %s", exception.Name, exception.Message))
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func appendRuntimeLog(output RuntimeLogAppender, line nanoflare.WorkerOutputLine) {
	if output == nil {
		return
	}
	if appender, ok := output.(interface {
		AppendLine(nanoflare.WorkerOutputLine)
	}); ok {
		appender.AppendLine(line)
	} else {
		output.AppendScoped(line.AppID, line.DeploymentID, line.Level, line.Message)
	}
}
