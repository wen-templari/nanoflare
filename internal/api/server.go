package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/clas/platform/internal/platform"
)

type Server struct {
	service *platform.Service
	mux     *http.ServeMux
}

func NewServer(service *platform.Service) *Server {
	server := &Server{service: service, mux: http.NewServeMux()}
	server.routes()
	return server
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	s.mux.HandleFunc("GET /v1/apps", s.listApps)
	s.mux.HandleFunc("POST /v1/apps", s.createApp)
	s.mux.HandleFunc("POST /v1/apps/{appID}/deployments", s.deploy)
	s.mux.HandleFunc("GET /internal/auth/verify", s.verifyAuth)
	s.mux.HandleFunc("POST /internal/runtime/kv/get", s.kvGet)
	s.mux.HandleFunc("POST /internal/runtime/kv/put", s.kvPut)
	s.mux.HandleFunc("POST /internal/runtime/kv/delete", s.kvDelete)
}

func (s *Server) listApps(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.service.ListApps())
}

func (s *Server) createApp(w http.ResponseWriter, r *http.Request) {
	var input platform.CreateAppInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	app, err := s.service.CreateApp(input)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, platform.ErrAppExists) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusCreated, app)
}

func (s *Server) deploy(w http.ResponseWriter, r *http.Request) {
	var input platform.DeployInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	deployment, err := s.service.Deploy(r.PathValue("appID"), input)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, platform.ErrAppNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusCreated, deployment)
}

// verifyAuth is intentionally a placeholder until an OIDC provider is selected.
// It preserves a clean Traefik ForwardAuth integration point for the first runtime slice.
func (s *Server) verifyAuth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("X-Platform-Context", `{"subject":"development-user"}`)
	w.WriteHeader(http.StatusOK)
}

type kvRequest struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value,omitempty"`
}

func (s *Server) kvGet(w http.ResponseWriter, r *http.Request) {
	var input kvRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	value, ok, err := s.service.KVGet(bearerToken(r), input.Key)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"value": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]json.RawMessage{"value": value})
}

func (s *Server) kvPut(w http.ResponseWriter, r *http.Request) {
	var input kvRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.Key == "" || len(input.Value) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("key and value are required"))
		return
	}
	if err := s.service.KVPut(bearerToken(r), input.Key, input.Value); err != nil {
		writeRuntimeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) kvDelete(w http.ResponseWriter, r *http.Request) {
	var input kvRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.service.KVDelete(bearerToken(r), input.Key); err != nil {
		writeRuntimeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeRuntimeError(w http.ResponseWriter, err error) {
	if errors.Is(err, platform.ErrInvalidCapability) {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func bearerToken(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
