package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type Server struct {
	service      *nanoflare.Service
	traefik      TraefikConfigReader
	traefikToken string
	auth         Authenticator
	mux          *http.ServeMux
}

type Authenticator interface {
	ValidateToken(context.Context, string) (AuthResult, error)
	UserInfo(context.Context, string) (AuthResult, map[string]any, error)
}

type BrowserAuthenticator interface {
	Authenticator
	Session(*http.Request) (AuthResult, string, bool)
	BeginAuth(http.ResponseWriter, *http.Request) error
	HandleCallback(http.ResponseWriter, *http.Request) error
}

type AuthResult struct {
	Valid     bool           `json:"valid"`
	Subject   string         `json:"subject,omitempty"`
	Email     string         `json:"email,omitempty"`
	ExpiresAt *time.Time     `json:"expires_at,omitempty"`
	Claims    map[string]any `json:"claims,omitempty"`
}

var errAuthDisabled = errors.New("oidc authentication is not configured")

type proxiedResponse struct {
	statusCode int
	header     http.Header
	body       []byte
}

type TraefikConfigReader interface {
	TraefikConfig() []byte
}

func NewServer(service *nanoflare.Service) *Server {
	return NewServerWithAuth(service, nil, "", nil)
}

func NewServerWithTraefik(service *nanoflare.Service, traefik TraefikConfigReader, token string) *Server {
	return NewServerWithAuth(service, traefik, token, nil)
}

func NewServerWithAuth(service *nanoflare.Service, traefik TraefikConfigReader, token string, auth Authenticator) *Server {
	server := &Server{service: service, traefik: traefik, traefikToken: token, auth: auth, mux: http.NewServeMux()}
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
	s.mux.HandleFunc("GET /v1/kv/namespaces", s.listKVNamespaces)
	s.mux.HandleFunc("POST /v1/kv/namespaces", s.createKVNamespace)
	s.mux.HandleFunc("DELETE /v1/kv/namespaces/{namespaceID}", s.deleteKVNamespace)
	s.mux.HandleFunc("PATCH /v1/apps/{appID}", s.updateApp)
	s.mux.HandleFunc("DELETE /v1/apps/{appID}", s.deleteApp)
	s.mux.HandleFunc("GET /v1/apps/{appID}", s.workerDetail)
	s.mux.HandleFunc("GET /v1/apps/{appID}/files", s.workerFiles)
	s.mux.HandleFunc("GET /v1/apps/{appID}/output", s.workerOutput)
	s.mux.HandleFunc("GET /v1/apps/{appID}/traffic", s.workerTraffic)
	s.mux.HandleFunc("GET /v1/apps/{appID}/deployments", s.workerDeployments)
	s.mux.HandleFunc("POST /v1/apps/{appID}/deployments", s.deploy)
	s.mux.HandleFunc("GET /v1/apps/{appID}/kv/namespaces/{namespaceID}", s.workerKVList)
	s.mux.HandleFunc("GET /v1/apps/{appID}/kv/namespaces/{namespaceID}/{key...}", s.workerKVGet)
	s.mux.HandleFunc("PUT /v1/apps/{appID}/kv/namespaces/{namespaceID}/{key...}", s.workerKVPut)
	s.mux.HandleFunc("DELETE /v1/apps/{appID}/kv/namespaces/{namespaceID}/{key...}", s.workerKVDelete)
	s.mux.HandleFunc("POST /v1/auth/validate", s.validateAuthToken)
	s.mux.HandleFunc("POST /v1/auth/userinfo", s.authUserInfo)
	s.mux.HandleFunc("GET /internal/auth/verify", s.verifyAuth)
	s.mux.HandleFunc("GET /internal/auth/callback", s.authCallback)
	s.mux.HandleFunc("GET /internal/traefik/config", s.traefikConfig)
	s.mux.HandleFunc("/internal/http/apps/", s.appGateway)
	s.mux.HandleFunc("GET /internal/runtime/objects/{key...}", s.runtimeObjectGet)
	s.mux.HandleFunc("HEAD /internal/runtime/objects/{key...}", s.runtimeObjectHead)
	s.mux.HandleFunc("PUT /internal/runtime/objects/{key...}", s.runtimeObjectPut)
	s.mux.HandleFunc("DELETE /internal/runtime/objects/{key...}", s.runtimeObjectDelete)
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
		if errors.Is(err, nanoflare.ErrAppNotFound) {
			writeWorkerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, nanoflare.WorkerTraffic{})
		return
	}
	writeJSON(w, http.StatusOK, traffic)
}

func (s *Server) workerKVList(w http.ResponseWriter, r *http.Request) {
	keys, err := s.service.WorkerKVList(r.PathValue("appID"), r.PathValue("namespaceID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *Server) workerKVGet(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("key") == "" {
		s.workerKVList(w, r)
		return
	}
	key, err := consoleKVKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	value, ok, err := s.service.WorkerKVGet(r.PathValue("appID"), r.PathValue("namespaceID"), key)
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(value)
}

func (s *Server) workerKVPut(w http.ResponseWriter, r *http.Request) {
	key, err := consoleKVKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	defer r.Body.Close()
	value, err := io.ReadAll(io.LimitReader(r.Body, maxKVValueSize+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(value) > maxKVValueSize {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("KV value exceeds 25 MiB limit"))
		return
	}
	if err := s.service.WorkerKVPut(r.PathValue("appID"), r.PathValue("namespaceID"), key, value); err != nil {
		writeWorkerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) workerKVDelete(w http.ResponseWriter, r *http.Request) {
	key, err := consoleKVKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.service.WorkerKVDelete(r.PathValue("appID"), r.PathValue("namespaceID"), key); err != nil {
		writeWorkerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listKVNamespaces(w http.ResponseWriter, _ *http.Request) {
	namespaces, err := s.service.ListKVNamespaces()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, namespaces)
}

func (s *Server) createKVNamespace(w http.ResponseWriter, r *http.Request) {
	var input nanoflare.CreateKVNamespaceInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	namespace, err := s.service.CreateKVNamespace(input)
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, namespace)
}

func (s *Server) deleteKVNamespace(w http.ResponseWriter, r *http.Request) {
	if err := s.service.DeleteKVNamespace(r.PathValue("namespaceID")); err != nil {
		writeWorkerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func consoleKVKey(r *http.Request) (string, error) {
	key := r.PathValue("key")
	if key == "" {
		return "", errors.New("KV key cannot be empty")
	}
	if key == "." || key == ".." {
		return "", errors.New(`KV keys "." and ".." are not allowed`)
	}
	if len([]byte(key)) > maxKVKeySize {
		return "", errors.New("KV key exceeds 512 byte limit")
	}
	return key, nil
}

func runtimeObjectKey(r *http.Request) (string, error) {
	key, err := url.PathUnescape(r.PathValue("key"))
	if err != nil {
		return "", errors.New("invalid object key encoding")
	}
	if key == "" {
		return "", errors.New("object key cannot be empty")
	}
	if key == "." || key == ".." {
		return "", errors.New(`object keys "." and ".." are not allowed`)
	}
	if strings.Contains(key, "..") {
		return "", errors.New(`object keys must not contain ".."`)
	}
	clean := strings.TrimPrefix(path.Clean("/"+key), "/")
	if clean == "." || clean == "" {
		return "", errors.New("object key cannot be empty")
	}
	return clean, nil
}

func writeRuntimeObjectHeaders(header http.Header, object nanoflare.ObjectInfo) {
	if object.HTTPMetadata.ContentType != "" {
		header.Set("Content-Type", object.HTTPMetadata.ContentType)
	} else {
		header.Set("Content-Type", "application/octet-stream")
	}
	header.Set("Content-Length", strconv.FormatInt(object.Size, 10))
	if object.HTTPETag != "" {
		header.Set("ETag", object.HTTPETag)
	}
	if !object.Uploaded.IsZero() {
		header.Set("Last-Modified", object.Uploaded.UTC().Format(http.TimeFormat))
	}
	header.Set("X-Nanoflare-Object-Key", object.Key)
	header.Set("X-Nanoflare-Object-Etag", object.ETag)
	header.Set("X-Nanoflare-Object-Uploaded", object.Uploaded.UTC().Format(time.RFC3339Nano))
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

func (s *Server) runtimeObjectGet(w http.ResponseWriter, r *http.Request) {
	key, err := runtimeObjectKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	object, ok, err := s.service.ObjectGet(bearerToken(r), key)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeRuntimeObjectHeaders(w.Header(), object.ObjectInfo)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(object.Body)
}

func (s *Server) runtimeObjectHead(w http.ResponseWriter, r *http.Request) {
	key, err := runtimeObjectKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	object, ok, err := s.service.ObjectHead(bearerToken(r), key)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeRuntimeObjectHeaders(w.Header(), object)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) runtimeObjectPut(w http.ResponseWriter, r *http.Request) {
	key, err := runtimeObjectKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	object, err := s.service.ObjectPut(bearerToken(r), key, r.Header.Get("Content-Type"), body)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, object)
}

func (s *Server) runtimeObjectDelete(w http.ResponseWriter, r *http.Request) {
	key, err := runtimeObjectKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.service.DeleteObject(bearerToken(r), key); err != nil {
		writeRuntimeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

func (s *Server) verifyAuth(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(bearerToken(r))
	if token == "" {
		if browserAuth, ok := s.auth.(BrowserAuthenticator); ok {
			if result, sessionToken, ok := browserAuth.Session(r); ok {
				w.Header().Set("X-Nanoflare-User-JWT", sessionToken)
				w.Header().Set("X-Nanoflare-User-Email", result.Email)
				w.WriteHeader(http.StatusOK)
				return
			}
			if shouldRedirectToOIDC(r) {
				if err := browserAuth.BeginAuth(w, r); err != nil {
					writeAuthError(w, err)
				}
				return
			}
		}
		writeError(w, http.StatusUnauthorized, errors.New("bearer token is required"))
		return
	}
	result, _, err := s.userInfoForRequest(r.Context(), token)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	w.Header().Set("X-Nanoflare-User-JWT", token)
	w.Header().Set("X-Nanoflare-User-Email", result.Email)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) authCallback(w http.ResponseWriter, r *http.Request) {
	browserAuth, ok := s.auth.(BrowserAuthenticator)
	if !ok {
		writeAuthError(w, errAuthDisabled)
		return
	}
	if err := browserAuth.HandleCallback(w, r); err != nil {
		writeAuthError(w, err)
	}
}

type authTokenRequest struct {
	Token string `json:"token"`
}

type authUserInfoResponse struct {
	Valid     bool           `json:"valid"`
	Subject   string         `json:"subject,omitempty"`
	Email     string         `json:"email,omitempty"`
	ExpiresAt *time.Time     `json:"expires_at,omitempty"`
	Claims    map[string]any `json:"claims,omitempty"`
	Raw       map[string]any `json:"raw,omitempty"`
}

func (s *Server) validateAuthToken(w http.ResponseWriter, r *http.Request) {
	token, err := decodeAuthTokenRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.validateRequestToken(r.Context(), token)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) authUserInfo(w http.ResponseWriter, r *http.Request) {
	token, err := decodeAuthTokenRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, raw, err := s.userInfoForRequest(r.Context(), token)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, authUserInfoResponse{
		Valid:     result.Valid,
		Subject:   result.Subject,
		Email:     result.Email,
		ExpiresAt: result.ExpiresAt,
		Claims:    result.Claims,
		Raw:       raw,
	})
}

func writeRuntimeError(w http.ResponseWriter, err error) {
	if errors.Is(err, nanoflare.ErrInvalidCapability) {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if errors.Is(err, nanoflare.ErrKVNamespaceNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

func writeWorkerError(w http.ResponseWriter, err error) {
	if errors.Is(err, nanoflare.ErrAppNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, nanoflare.ErrKVNamespaceNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, nanoflare.ErrKVNamespaceExists) || errors.Is(err, nanoflare.ErrKVNamespaceInUse) || errors.Is(err, nanoflare.ErrKVNamespaceNotBound) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

func writeAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errAuthDisabled):
		writeError(w, http.StatusServiceUnavailable, err)
	case strings.Contains(strings.ToLower(err.Error()), "bearer token is required"),
		strings.Contains(strings.ToLower(err.Error()), "invalid token"),
		strings.Contains(strings.ToLower(err.Error()), "token expired"),
		strings.Contains(strings.ToLower(err.Error()), "email"):
		writeError(w, http.StatusUnauthorized, err)
	default:
		writeError(w, http.StatusBadGateway, err)
	}
}

func decodeAuthTokenRequest(r *http.Request) (string, error) {
	var input authTokenRequest
	if err := decodeJSON(r, &input); err != nil {
		return "", err
	}
	token := strings.TrimSpace(input.Token)
	if token == "" {
		return "", errors.New("token is required")
	}
	return token, nil
}

func (s *Server) validateRequestToken(ctx context.Context, token string) (AuthResult, error) {
	if s.auth == nil {
		return AuthResult{}, errAuthDisabled
	}
	return s.auth.ValidateToken(ctx, token)
}

func (s *Server) userInfoForRequest(ctx context.Context, token string) (AuthResult, map[string]any, error) {
	if s.auth == nil {
		return AuthResult{}, nil, errAuthDisabled
	}
	return s.auth.UserInfo(ctx, token)
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func shouldRedirectToOIDC(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	if strings.Contains(accept, "application/json") {
		return false
	}
	if strings.EqualFold(r.Header.Get("X-Requested-With"), "XMLHttpRequest") {
		return false
	}
	return true
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
