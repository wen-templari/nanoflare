package runner

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

type RuntimeManager interface {
	Prepare([]nanoflare.ActiveDeployment) (string, []nanoflare.ActiveDeployment, error)
	Commit(string) error
	Abort(string) error
}

type Server struct {
	manager   RuntimeManager
	token     string
	mux       *http.ServeMux
	transport *http.Transport
	bootID    string
}

func NewServer(manager RuntimeManager, token string) *Server {
	server := &Server{manager: manager, token: token, mux: http.NewServeMux(), bootID: rand.Text()}
	server.transport = http.DefaultTransport.(*http.Transport).Clone()
	runtime.ConfigureTransport(server.transport)
	server.mux.HandleFunc("GET /healthz", server.health)
	server.mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, struct {
			BootID    string                     `json:"boot_id"`
			Resources *nanoflare.RunnerResources `json:"resources,omitempty"`
		}{server.bootID, readRunnerResources()})
	})
	server.mux.HandleFunc("POST /v1/generations/prepare", server.prepare)
	server.mux.HandleFunc("POST /v1/publications/validate", server.validatePublication)
	server.mux.HandleFunc("POST /v1/generations/{generation}/commit", server.commit)
	server.mux.HandleFunc("POST /v1/generations/{generation}/abort", server.abort)
	server.mux.HandleFunc("POST /v1/runtime-grants/check", server.checkRuntimeGrant)
	server.mux.HandleFunc("/v1/workers/{app}/{deployment}/{path...}", server.worker)
	return server
}

func (s *Server) checkRuntimeGrant(w http.ResponseWriter, r *http.Request) {
	checker, ok := s.manager.(interface {
		RuntimeDeploymentLive(context.Context, string, string) (bool, error)
	})
	if !ok {
		http.Error(w, "runtime authorization unavailable", http.StatusServiceUnavailable)
		return
	}
	var input struct {
		InstanceID   string `json:"instance_id,omitempty"`
		AppID        string `json:"app_id"`
		DeploymentID string `json:"deployment_id"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	live, err := checker.RuntimeDeploymentLive(r.Context(), input.AppID, input.DeploymentID)
	if input.InstanceID != "" {
		instanceChecker, ok := s.manager.(interface {
			RuntimeInstanceLive(context.Context, string, string, string) (bool, error)
		})
		if !ok {
			http.Error(w, "runtime instance authorization unavailable", 503)
			return
		}
		live, err = instanceChecker.RuntimeInstanceLive(r.Context(), input.AppID, input.DeploymentID, input.InstanceID)
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"live": live})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if strings.HasPrefix(r.URL.Path, "/v1/workers/") {
		token = r.Header.Get("X-Nanoflare-Runner-Token")
	}
	if r.URL.Path != "/healthz" && (s.token == "" || token != s.token) {
		writeError(w, http.StatusUnauthorized, errors.New("invalid runner token"))
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) worker(w http.ResponseWriter, r *http.Request) {
	manager, ok := s.manager.(interface {
		Ensure(context.Context, nanoflare.ActiveDeployment) (runtime.EnsuredWorker, error)
	})
	if !ok {
		http.Error(w, "runtime routing unavailable", http.StatusServiceUnavailable)
		return
	}
	active := nanoflare.ActiveDeployment{App: nanoflare.App{ID: r.PathValue("app")}, Deployment: nanoflare.Deployment{ID: r.PathValue("deployment")}}
	endpoint, err := manager.Ensure(r.Context(), active)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer func() {
		if r.Header.Get("X-Nanoflare-Dispatch-Root") != "" && r.Context().Err() == nil {
			time.AfterFunc(2*time.Second, endpoint.Release)
		} else {
			endpoint.Release()
		}
	}()
	prefix := "/v1/workers/" + active.App.ID + "/" + active.Deployment.ID
	proxy := &httputil.ReverseProxy{Transport: s.transport, FlushInterval: -1, Director: func(req *http.Request) {
		req.URL.Scheme = "http"
		req.URL.Host = runtime.SocketHost(endpoint.SocketPath)
		req.URL.Path = strings.TrimPrefix(req.URL.Path, prefix)
		if req.URL.RawPath != "" {
			req.URL.RawPath = strings.TrimPrefix(req.URL.RawPath, prefix)
		}
		for name := range req.Header {
			if strings.HasPrefix(strings.ToLower(name), "x-nanoflare-") && !strings.EqualFold(name, "X-Nanoflare-Dispatch-Root") {
				delete(req.Header, name)
			}
		}
		for name, values := range endpoint.Headers {
			req.Header[name] = append([]string(nil), values...)
		}
	}}
	proxy.ServeHTTP(w, r)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) validatePublication(w http.ResponseWriter, r *http.Request) {
	validator, ok := s.manager.(nanoflare.PublicationValidator)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, errors.New("publication validation unavailable"))
		return
	}
	var input PrepareRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validator.ValidatePublication(input.activeDeployments()); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) prepare(w http.ResponseWriter, r *http.Request) {
	var input PrepareRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.ArtifactProtocol != 1 {
		writeError(w, http.StatusConflict, errors.New("runner artifact protocol mismatch; upgrade control plane and runner together"))
		return
	}
	generation, deployments, err := s.manager.Prepare(input.activeDeployments())
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	for i := range deployments {
		deployments[i].SocketPath = ""
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
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<20))
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
