package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type RuntimeAssetServer struct {
	service *nanoflare.Service
}

func NewRuntimeAssetServer(service *nanoflare.Service) *RuntimeAssetServer {
	return &RuntimeAssetServer{service: service}
}

func (s *RuntimeAssetServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, errors.New("unsupported asset operation"))
		return
	}
	requestPath := strings.TrimPrefix(r.URL.EscapedPath(), "/internal/runtime/assets")
	if requestPath == r.URL.EscapedPath() {
		requestPath = r.URL.EscapedPath()
	}
	if requestPath == "" {
		requestPath = "/"
	}
	response, err := s.service.AssetFetch(bearerToken(r), requestPath)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	writeAssetResponse(w, r, response)
}
