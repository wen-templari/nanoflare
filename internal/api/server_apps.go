package api

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type proxiedResponse struct {
	statusCode int
	header     http.Header
	body       []byte
}

func (s *Server) registerAppRoutes() {
	s.mux.HandleFunc("GET /v1/apps", s.listApps)
	s.mux.HandleFunc("POST /v1/apps", s.createApp)
	s.mux.HandleFunc("PATCH /v1/apps/{appID}", s.updateApp)
	s.mux.HandleFunc("DELETE /v1/apps/{appID}", s.deleteApp)
	s.mux.HandleFunc("GET /v1/apps/{appID}", s.workerDetail)
	s.mux.HandleFunc("GET /v1/apps/{appID}/files", s.workerFiles)
	s.mux.HandleFunc("GET /v1/apps/{appID}/output", s.workerOutput)
	s.mux.HandleFunc("GET /v1/apps/{appID}/traffic", s.workerTraffic)
	s.mux.HandleFunc("GET /v1/apps/{appID}/deployments", s.workerDeployments)
	s.mux.HandleFunc("POST /v1/apps/{appID}/deployments", s.deploy)
	s.mux.HandleFunc("/internal/http/apps/", s.appGateway)
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
		if errors.Is(err, nanoflare.ErrAppNotFound) {
			writeWorkerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, nanoflare.WorkerTraffic{})
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

func (s *Server) createApp(w http.ResponseWriter, r *http.Request) {
	var input nanoflare.CreateAppInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	app, err := s.service.CreateApp(input)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, nanoflare.ErrAppExists) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusCreated, app)
}

func (s *Server) updateApp(w http.ResponseWriter, r *http.Request) {
	var input nanoflare.UpdateAppInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	app, err := s.service.UpdateApp(r.PathValue("appID"), input)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, nanoflare.ErrAppNotFound) {
			status = http.StatusNotFound
		}
		if errors.Is(err, nanoflare.ErrAppExists) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, app)
}

func (s *Server) deleteApp(w http.ResponseWriter, r *http.Request) {
	if err := s.service.DeleteApp(r.PathValue("appID")); err != nil {
		writeWorkerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deploy(w http.ResponseWriter, r *http.Request) {
	var input nanoflare.DeployInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	deployment, err := s.service.Deploy(r.PathValue("appID"), input)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, nanoflare.ErrAppNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusCreated, deployment)
}

func (s *Server) appGateway(w http.ResponseWriter, r *http.Request) {
	appID, runtimePort, requestPath, ok := appGatewayPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	port, runWorkerFirst, err := s.service.WorkerPort(appID, requestPath)
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	if runtimePort != 0 {
		port = runtimePort
	}
	if port == 0 {
		writeWorkerError(w, nanoflare.ErrAppNotFound)
		return
	}
	if !runWorkerFirst {
		response, handled, err := s.service.PublicAsset(appID, requestPath)
		if err != nil {
			writeWorkerError(w, err)
			return
		}
		if handled && response.StatusCode == http.StatusOK {
			writeAssetResponse(w, r, response)
			return
		}
		proxied, err := s.proxyWorker(r, port, requestPath)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		if handled && proxied.statusCode == http.StatusNotFound {
			writeAssetResponse(w, r, response)
			return
		}
		writeProxiedResponse(w, proxied)
		return
	}
	proxied, err := s.proxyWorker(r, port, requestPath)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeProxiedResponse(w, proxied)
}

func appGatewayPath(requestPath string) (string, int, string, bool) {
	const prefix = "/internal/http/apps/"
	if !strings.HasPrefix(requestPath, prefix) {
		return "", 0, "", false
	}
	trimmed := strings.TrimPrefix(requestPath, prefix)
	appID, rest, _ := strings.Cut(trimmed, "/")
	if appID == "" {
		return "", 0, "", false
	}
	port := 0
	if value, remainder, ok := strings.Cut(rest, "/"); ok {
		if parsed, err := strconv.Atoi(value); err == nil {
			port = parsed
			rest = remainder
		}
	}
	if rest == "" {
		return appID, port, "/", true
	}
	return appID, port, "/" + rest, true
}

func writeAssetResponse(w http.ResponseWriter, r *http.Request, response nanoflare.AssetResponse) {
	if response.ContentType != "" {
		w.Header().Set("Content-Type", response.ContentType)
	}
	w.WriteHeader(response.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(response.Body)
}

func (s *Server) proxyWorker(r *http.Request, port int, requestPath string) (proxiedResponse, error) {
	target := &url.URL{
		Scheme:   "http",
		Host:     "127.0.0.1:" + strconv.Itoa(port),
		Path:     requestPath,
		RawQuery: r.URL.RawQuery,
	}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		return proxiedResponse{}, err
	}
	request.Header = r.Header.Clone()
	request.Host = r.Host
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return proxiedResponse{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return proxiedResponse{}, err
	}
	return proxiedResponse{statusCode: response.StatusCode, header: response.Header.Clone(), body: body}, nil
}

func writeProxiedResponse(w http.ResponseWriter, response proxiedResponse) {
	for key, values := range response.header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.statusCode)
	_, _ = w.Write(response.body)
}
