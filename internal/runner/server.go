package runner

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/clas/platform/internal/platform"
)

type RuntimeManager interface {
	Prepare([]platform.ActiveDeployment) (string, []platform.ActiveDeployment, error)
	Commit(string) error
	Abort(string) error
}

type Server struct {
	manager RuntimeManager
	token   string
	mux     *http.ServeMux
}

func NewServer(manager RuntimeManager, token string) *Server {
	server := &Server{manager: manager, token: token, mux: http.NewServeMux()}
	server.mux.HandleFunc("GET /healthz", server.health)
	server.mux.HandleFunc("POST /v1/generations/prepare", server.prepare)
	server.mux.HandleFunc("POST /v1/generations/{generation}/commit", server.commit)
	server.mux.HandleFunc("POST /v1/generations/{generation}/abort", server.abort)
	return server
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/healthz" && (s.token == "" || bearerToken(r) != s.token) {
		writeError(w, http.StatusUnauthorized, errors.New("invalid runner token"))
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) prepare(w http.ResponseWriter, r *http.Request) {
	var input PrepareRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	generation, deployments, err := s.manager.Prepare(input.Deployments)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusCreated, PrepareResponse{Generation: generation, Deployments: deployments})
}

func (s *Server) commit(w http.ResponseWriter, r *http.Request) {
	if err := s.manager.Commit(r.PathValue("generation")); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) abort(w http.ResponseWriter, r *http.Request) {
	if err := s.manager.Abort(r.PathValue("generation")); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
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
