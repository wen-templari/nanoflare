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
	service      *platform.Service
	traefik      TraefikConfigReader
	traefikToken string
	mux          *http.ServeMux
}

type TraefikConfigReader interface {
	TraefikConfig() []byte
}

func NewServer(service *platform.Service) *Server {
	return NewServerWithTraefik(service, nil, "")
}

func NewServerWithTraefik(service *platform.Service, traefik TraefikConfigReader, token string) *Server {
	server := &Server{service: service, traefik: traefik, traefikToken: token, mux: http.NewServeMux()}
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
	s.mux.HandleFunc("GET /v1/apps/{appID}", s.workerDetail)
	s.mux.HandleFunc("GET /v1/apps/{appID}/files", s.workerFiles)
	s.mux.HandleFunc("GET /v1/apps/{appID}/output", s.workerOutput)
	s.mux.HandleFunc("GET /v1/apps/{appID}/traffic", s.workerTraffic)
	s.mux.HandleFunc("GET /v1/apps/{appID}/deployments", s.workerDeployments)
	s.mux.HandleFunc("POST /v1/apps/{appID}/deployments", s.deploy)
	s.mux.HandleFunc("GET /internal/auth/verify", s.verifyAuth)
	s.mux.HandleFunc("GET /internal/traefik/config", s.traefikConfig)
	s.mux.HandleFunc("POST /internal/runtime/objects/presign-upload", s.presignUpload)
	s.mux.HandleFunc("POST /internal/runtime/objects/presign-download", s.presignDownload)
	s.mux.HandleFunc("POST /internal/runtime/objects/delete", s.deleteObject)
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

func (s *Server) workerDeployments(w http.ResponseWriter, r *http.Request) {
	deployments, err := s.service.WorkerDeployments(r.PathValue("appID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, deployments)
}

func (s *Server) workerDetail(w http.ResponseWriter, r *http.Request) {
	detail, err := s.service.WorkerDetail(r.PathValue("appID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) workerFiles(w http.ResponseWriter, r *http.Request) {
	files, err := s.service.WorkerFiles(r.PathValue("appID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *Server) workerOutput(w http.ResponseWriter, r *http.Request) {
	output, err := s.service.WorkerOutput(r.PathValue("appID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, output)
}

func (s *Server) workerTraffic(w http.ResponseWriter, r *http.Request) {
	traffic, err := s.service.WorkerTraffic(r.PathValue("appID"))
	if err != nil {
		if errors.Is(err, platform.ErrAppNotFound) {
			writeWorkerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, platform.WorkerTraffic{})
		return
	}
	writeJSON(w, http.StatusOK, traffic)
}

func (s *Server) listApps(w http.ResponseWriter, _ *http.Request) {
	apps, err := s.service.ListApps()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, apps)
}

type objectRequest struct {
	Path string `json:"path"`
}

func (s *Server) presignUpload(w http.ResponseWriter, r *http.Request) {
	s.presignObject(w, r, s.service.PresignUpload)
}

func (s *Server) presignDownload(w http.ResponseWriter, r *http.Request) {
	s.presignObject(w, r, s.service.PresignDownload)
}

func (s *Server) presignObject(w http.ResponseWriter, r *http.Request, presign func(string, string) (string, error)) {
	var input objectRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.Path == "" {
		writeError(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	url, err := presign(bearerToken(r), input.Path)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

func (s *Server) deleteObject(w http.ResponseWriter, r *http.Request) {
	var input objectRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.Path == "" {
		writeError(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	if err := s.service.DeleteObject(bearerToken(r), input.Path); err != nil {
		writeRuntimeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

func writeRuntimeError(w http.ResponseWriter, err error) {
	if errors.Is(err, platform.ErrInvalidCapability) {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

func writeWorkerError(w http.ResponseWriter, err error) {
	if errors.Is(err, platform.ErrAppNotFound) {
		writeError(w, http.StatusNotFound, err)
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
