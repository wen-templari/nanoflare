package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type RuntimeDBServer struct {
	service *nanoflare.Service
}

func NewRuntimeDBServer(service *nanoflare.Service) *RuntimeDBServer {
	return &RuntimeDBServer{service: service}
}

func (s *RuntimeDBServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, errors.New("unsupported database operation"))
		return
	}
	databaseID := strings.TrimSpace(r.Header.Get("X-Nanoflare-Database-ID"))
	if databaseID == "" {
		writeError(w, http.StatusBadRequest, errors.New("database id header is required"))
		return
	}
	var request nanoflare.DBQueryRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	response, err := s.service.DBExecute(bearerToken(r), databaseID, request)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}
