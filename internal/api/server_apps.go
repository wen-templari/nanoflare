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
	s.mux.HandleFunc("PUT /v1/apps/{appID}/deployments/traffic", s.setWorkerDeploymentTraffic)
	s.mux.HandleFunc("POST /v1/apps/{appID}/deployments", s.deploy)
	s.mux.HandleFunc("GET /v1/apps/{appID}/secrets", s.listSecrets)
	s.mux.HandleFunc("PUT /v1/apps/{appID}/secrets/{name}", s.putSecret)
	s.mux.HandleFunc("DELETE /v1/apps/{appID}/secrets/{name}", s.deleteSecret)
	s.mux.HandleFunc("/internal/http/apps/", s.appGateway)
}

func (s *Server) workerDeployments(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "apps:read") {
		return
	}
	deployments, err := s.service.WorkerDeploymentsForOrg(controlOrgID(r), r.PathValue("appID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, deployments)
}

type deploymentTrafficRequest struct {
	Deployments []nanoflare.DeploymentTraffic `json:"deployments"`
}

func (s *Server) setWorkerDeploymentTraffic(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "deployments:write") {
		return
	}
	var input deploymentTrafficRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	deployments, err := s.service.SetDeploymentTrafficForOrg(controlOrgID(r), r.PathValue("appID"), input.Deployments)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, nanoflare.ErrAppNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, deployments)
}

func (s *Server) workerDetail(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "apps:read") {
		return
	}
	detail, err := s.service.WorkerDetailForOrg(controlOrgID(r), r.PathValue("appID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) workerFiles(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "apps:read") {
		return
	}
	files, err := s.service.WorkerFilesForOrg(controlOrgID(r), r.PathValue("appID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *Server) workerOutput(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "apps:read") {
		return
	}
	output, err := s.service.WorkerOutputForOrg(controlOrgID(r), r.PathValue("appID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, output)
}

func (s *Server) workerTraffic(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "apps:read") {
		return
	}
	traffic, err := s.service.WorkerTrafficForOrg(controlOrgID(r), r.PathValue("appID"))
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

func (s *Server) listApps(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "apps:read") {
		return
	}
	apps, err := s.service.ListAppsForOrg(controlOrgID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, apps)
}

func (s *Server) createApp(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "apps:write") {
		return
	}
	var input nanoflare.CreateAppInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input.OrgID = controlOrgID(r)
	if access, ok := controlOAuthAccess(r); ok {
		input.OAuthClientID = access.ClientID
	}
	input.CreatedBy = controlActor(r)
	app, err := s.service.CreateApp(input)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, nanoflare.ErrAppExists) {
			status = http.StatusConflict
		}
		if errors.Is(err, nanoflare.ErrUsageLimitExceeded) {
			status = http.StatusPaymentRequired
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusCreated, app)
}

func (s *Server) updateApp(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "apps:write") {
		return
	}
	var input nanoflare.UpdateAppInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	app, err := s.service.UpdateAppForOrg(controlOrgID(r), r.PathValue("appID"), input)
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
	if !s.requireScope(w, r, "apps:write") {
		return
	}
	if err := s.service.DeleteAppForOrg(controlOrgID(r), r.PathValue("appID")); err != nil {
		writeWorkerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deploy(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "deployments:write") {
		return
	}
	var input nanoflare.DeployInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input.CreatedBy = controlActor(r)
	deployment, err := s.service.DeployForOrg(controlOrgID(r), r.PathValue("appID"), input)
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

func (s *Server) listSecrets(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "secrets:write") {
		return
	}
	secrets, err := s.service.ListSecretsForOrg(controlOrgID(r), r.PathValue("appID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, secrets)
}

func (s *Server) putSecret(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "secrets:write") {
		return
	}
	var input nanoflare.PutSecretInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.service.PutSecretForOrg(controlOrgID(r), r.PathValue("appID"), r.PathValue("name"), input.Value); err != nil {
		writeWorkerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteSecret(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "secrets:write") {
		return
	}
	if err := s.service.DeleteSecretForOrg(controlOrgID(r), r.PathValue("appID"), r.PathValue("name")); err != nil {
		writeWorkerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) appGateway(w http.ResponseWriter, r *http.Request) {
	appID, runtimePort, requestPath, ok := appGatewayPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	active, runWorkerFirst, ok, err := s.service.WorkerRuntimeDeploymentWithPreference(appID, requestPath, stickyDeploymentID(r, appID))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	if !ok {
		writeWorkerError(w, nanoflare.ErrAppNotFound)
		return
	}
	setStickyDeploymentCookie(w, appID, active.Deployment.ID)
	if !runWorkerFirst {
		response, handled, err := s.service.PublicAssetForDeployment(active, requestPath)
		if err != nil {
			writeWorkerError(w, err)
			return
		}
		if handled && response.StatusCode == http.StatusOK {
			writeAssetResponse(w, r, response)
			return
		}
		port, release, err := s.ensureWorker(r, active, runtimePort)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		defer release()
		workerResponse, err := s.workerResponse(r, port, requestPath)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		defer workerResponse.Body.Close()
		if handled && workerResponse.StatusCode == http.StatusNotFound {
			writeAssetResponse(w, r, response)
			return
		}
		writeWorkerResponse(w, workerResponse)
		return
	}
	port, release, err := s.ensureWorker(r, active, runtimePort)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	defer release()
	workerResponse, err := s.workerResponse(r, port, requestPath)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	defer workerResponse.Body.Close()
	writeWorkerResponse(w, workerResponse)
}

func stickyDeploymentID(r *http.Request, appID string) string {
	cookie, err := r.Cookie(stickyDeploymentCookieName(appID))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func setStickyDeploymentCookie(w http.ResponseWriter, appID, deploymentID string) {
	http.SetCookie(w, &http.Cookie{
		Name:     stickyDeploymentCookieName(appID),
		Value:    deploymentID,
		Path:     "/",
		MaxAge:   86400,
		SameSite: http.SameSiteLaxMode,
	})
}

func stickyDeploymentCookieName(appID string) string {
	return "nf_deployment_" + appID
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

func (s *Server) ensureWorker(r *http.Request, active nanoflare.ActiveDeployment, runtimePort int) (int, func(), error) {
	if runtimePort != 0 {
		return runtimePort, func() {}, nil
	}
	if s.runtime == nil {
		if active.Deployment.Port == 0 {
			return 0, nil, nanoflare.ErrAppNotFound
		}
		return active.Deployment.Port, func() {}, nil
	}
	ensured, err := s.runtime.Ensure(r.Context(), active)
	if err != nil {
		return 0, nil, err
	}
	return ensured.Port, ensured.Release, nil
}

func (s *Server) workerResponse(r *http.Request, port int, requestPath string) (*http.Response, error) {
	target := &url.URL{
		Scheme:   "http",
		Host:     "127.0.0.1:" + strconv.Itoa(port),
		Path:     requestPath,
		RawQuery: r.URL.RawQuery,
	}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		return nil, err
	}
	request.Header = r.Header.Clone()
	request.Host = r.Host
	return http.DefaultClient.Do(request)
}

func writeWorkerResponse(w http.ResponseWriter, response *http.Response) {
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}
