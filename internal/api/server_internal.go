package api

import (
	"errors"
	"net/http"
)

func (s *Server) registerInternalRoutes() {
	s.mux.HandleFunc("GET /internal/traefik/config", s.traefikConfig)
}

func (s *Server) traefikConfig(w http.ResponseWriter, r *http.Request) {
	if s.traefik == nil {
		http.NotFound(w, r)
		return
	}
	if s.traefikToken == "" || bearerToken(r) != s.traefikToken {
		writeError(w, http.StatusUnauthorized, errors.New("invalid Traefik token"))
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.traefik.TraefikConfig())
}
