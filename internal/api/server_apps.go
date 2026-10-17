package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

var gatewayTracer = otel.Tracer("github.com/clas/nanoflare/internal/api/worker-gateway")

func (s *Server) registerAppRoutes() {
	base := "/v1/organizations/{orgID}/workers"
	s.mux.HandleFunc("GET "+base, s.listApps)
	s.mux.HandleFunc("GET "+base+"/analytics", s.organizationWorkerTraffic)
	s.mux.HandleFunc("POST "+base, s.createApp)
	s.mux.HandleFunc("PATCH "+base+"/{workerID}", s.updateApp)
	s.mux.HandleFunc("DELETE "+base+"/{workerID}", s.deleteApp)
	s.mux.HandleFunc("GET "+base+"/{workerID}", s.workerDetail)
	s.mux.HandleFunc("GET "+base+"/{workerID}/files", s.workerFiles)
	s.mux.HandleFunc("GET "+base+"/{workerID}/output", s.workerOutput)
	s.mux.HandleFunc("GET "+base+"/{workerID}/analytics", s.workerTraffic)
	s.mux.HandleFunc("GET "+base+"/{workerID}/deployments", s.workerDeployments)
	s.mux.HandleFunc("PUT "+base+"/{workerID}/deployment-traffic", s.setWorkerDeploymentTraffic)
	s.mux.HandleFunc("POST "+base+"/{workerID}/deployments", s.deploy)
	s.mux.HandleFunc("PUT "+base+"/{workerID}/vars", s.updateVars)
	s.mux.HandleFunc("GET "+base+"/{workerID}/secrets", s.listSecrets)
	s.mux.HandleFunc("PUT "+base+"/{workerID}/secrets/{name}", s.putSecret)
	s.mux.HandleFunc("DELETE "+base+"/{workerID}/secrets/{name}", s.deleteSecret)
	s.mux.HandleFunc("/internal/http/workers/", s.appGateway)
}

func (s *Server) workerDeployments(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "workers:read") {
		return
	}
	deployments, err := s.service.WorkerDeploymentsForOrg(controlOrgID(r), r.PathValue("workerID"))
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
	deployments, err := s.service.SetDeploymentTrafficForOrg(controlOrgID(r), r.PathValue("workerID"), input.Deployments)
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
	if !s.requireScope(w, r, "workers:read") {
		return
	}
	detail, err := s.service.WorkerDetailForOrg(controlOrgID(r), r.PathValue("workerID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) workerFiles(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "workers:read") {
		return
	}
	files, err := s.service.WorkerFilesForOrg(controlOrgID(r), r.PathValue("workerID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *Server) workerOutput(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "workers:read") {
		return
	}
	query, err := workerOutputQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	output, err := s.service.WorkerOutputQueryForOrg(r.Context(), controlOrgID(r), r.PathValue("workerID"), query)
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, output)
}

func workerOutputQuery(r *http.Request) (nanoflare.WorkerOutputQuery, error) {
	values := r.URL.Query()
	query := nanoflare.WorkerOutputQuery{DeploymentID: strings.TrimSpace(values.Get("deployment_id")), Level: strings.TrimSpace(values.Get("level")), Text: strings.TrimSpace(values.Get("q")), Limit: 500}
	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 1000 {
			return query, errors.New("limit must be between 1 and 1000")
		}
		query.Limit = limit
	}
	for _, item := range []struct {
		raw    string
		target *time.Time
	}{{values.Get("since"), &query.Since}, {values.Get("until"), &query.Until}} {
		if item.raw == "" {
			continue
		}
		value, err := time.Parse(time.RFC3339, item.raw)
		if err != nil {
			return query, errors.New("since and until must be RFC3339 timestamps")
		}
		*item.target = value
	}
	return query, nil
}

func (s *Server) workerTraffic(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "workers:read") {
		return
	}
	traffic, err := s.service.WorkerMetricsTimeseriesForOrg(controlOrgID(r), r.PathValue("workerID"))
	if err != nil {
		if errors.Is(err, nanoflare.ErrAppNotFound) {
			writeWorkerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, nanoflare.WorkerMetricsTimeseries{})
		return
	}
	writeJSON(w, http.StatusOK, traffic)
}

func (s *Server) organizationWorkerTraffic(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "workers:read") {
		return
	}
	traffic, err := s.service.OrganizationWorkerMetricsTimeseries(controlOrgID(r))
	if err != nil {
		writeJSON(w, http.StatusOK, nanoflare.WorkerMetricsTimeseries{})
		return
	}
	writeJSON(w, http.StatusOK, traffic)
}

func (s *Server) listApps(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "workers:read") {
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
	if !s.requireScope(w, r, "workers:write") {
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
	if !s.requireScope(w, r, "workers:write") {
		return
	}
	var input nanoflare.UpdateAppInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	app, err := s.service.UpdateAppForOrg(controlOrgID(r), r.PathValue("workerID"), input)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, nanoflare.ErrInvalidAppUpdate) {
			status = http.StatusBadRequest
		}
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
	if !s.requireScope(w, r, "workers:write") {
		return
	}
	if err := s.service.DeleteAppForOrg(controlOrgID(r), r.PathValue("workerID")); err != nil {
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
	deployment, err := s.service.DeployForOrg(controlOrgID(r), r.PathValue("workerID"), input)
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
	secrets, err := s.service.ListSecretsForOrg(controlOrgID(r), r.PathValue("workerID"))
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
	if err := s.service.PutSecretForOrg(controlOrgID(r), r.PathValue("workerID"), r.PathValue("name"), input.Value); err != nil {
		writeWorkerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteSecret(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "secrets:write") {
		return
	}
	if err := s.service.DeleteSecretForOrg(controlOrgID(r), r.PathValue("workerID"), r.PathValue("name")); err != nil {
		writeWorkerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) appGateway(w http.ResponseWriter, r *http.Request) {
	parent := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	ctx, gatewaySpan := gatewayTracer.Start(parent, "worker.gateway", trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(
		attribute.String("http.request.method", r.Method),
	))
	defer gatewaySpan.End()
	r = r.WithContext(ctx)

	appID, runtimePort, requestPath, ok := appGatewayPath(r.URL.Path)
	if !ok {
		gatewaySpan.SetStatus(codes.Error, "invalid worker gateway path")
		http.NotFound(w, r)
		return
	}
	gatewaySpan.SetAttributes(
		attribute.String("nanoflare.worker.id", appID),
		attribute.String("http.route", "/internal/http/workers/{worker_id}/{path}"),
		attribute.Bool("nanoflare.runtime.explicit_port", runtimePort != 0),
	)
	_, _, escapedRequestPath, escapedOK := appGatewayPath(r.URL.EscapedPath())
	if !escapedOK {
		escapedRequestPath = requestPath
	}
	resolveCtx, resolveSpan := gatewayTracer.Start(r.Context(), "worker.resolve_deployment")
	active, runWorkerFirst, ok, err := s.service.WorkerRuntimeDeploymentWithPreferenceContext(resolveCtx, appID, requestPath, stickyDeploymentID(r, appID))
	if err != nil {
		recordSpanError(resolveSpan, err)
	}
	resolveSpan.SetAttributes(
		attribute.Bool("nanoflare.deployment.found", ok),
		attribute.Bool("nanoflare.worker.run_first", runWorkerFirst),
	)
	resolveSpan.End()
	if err != nil {
		gatewaySpan.SetStatus(codes.Error, err.Error())
		writeWorkerError(w, err)
		return
	}
	if !ok {
		gatewaySpan.SetStatus(codes.Error, "worker deployment not found")
		writeWorkerError(w, nanoflare.ErrAppNotFound)
		return
	}
	gatewaySpan.SetAttributes(attribute.String("nanoflare.deployment.id", active.Deployment.ID))
	setStickyDeploymentCookie(w, appID, active.Deployment.ID)
	if !runWorkerFirst {
		_, assetSpan := gatewayTracer.Start(r.Context(), "worker.asset_lookup")
		response, handled, err := s.service.PublicAssetForDeployment(active, requestPath)
		if err != nil {
			recordSpanError(assetSpan, err)
		}
		assetSpan.SetAttributes(attribute.Bool("nanoflare.asset.handled", handled))
		assetSpan.End()
		if err != nil {
			gatewaySpan.SetStatus(codes.Error, err.Error())
			writeWorkerError(w, err)
			return
		}
		if handled && response.StatusCode == http.StatusOK {
			_, assetWriteSpan := gatewayTracer.Start(r.Context(), "worker.response_copy")
			writeAssetResponse(w, r, response)
			assetWriteSpan.End()
			return
		}
		endpoint, err := s.ensureWorkerTraced(r, active, runtimePort)
		if err != nil {
			gatewaySpan.SetStatus(codes.Error, err.Error())
			writeError(w, http.StatusBadGateway, err)
			return
		}
		defer endpoint.Release()
		workerResponse, err := s.workerResponse(r, endpoint, requestPath, escapedRequestPath)
		if err != nil {
			gatewaySpan.SetStatus(codes.Error, err.Error())
			writeError(w, http.StatusBadGateway, err)
			return
		}
		defer workerResponse.Body.Close()
		if handled && workerResponse.StatusCode == http.StatusNotFound {
			_, assetWriteSpan := gatewayTracer.Start(r.Context(), "worker.response_copy")
			writeAssetResponse(w, r, response)
			assetWriteSpan.End()
			return
		}
		writeWorkerResponse(r.Context(), w, workerResponse)
		return
	}
	endpoint, err := s.ensureWorkerTraced(r, active, runtimePort)
	if err != nil {
		gatewaySpan.SetStatus(codes.Error, err.Error())
		writeError(w, http.StatusBadGateway, err)
		return
	}
	defer endpoint.Release()
	workerResponse, err := s.workerResponse(r, endpoint, requestPath, escapedRequestPath)
	if err != nil {
		gatewaySpan.SetStatus(codes.Error, err.Error())
		writeError(w, http.StatusBadGateway, err)
		return
	}
	defer workerResponse.Body.Close()
	writeWorkerResponse(r.Context(), w, workerResponse)
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
	const prefix = "/internal/http/workers/"
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

func (s *Server) ensureWorker(r *http.Request, active nanoflare.ActiveDeployment, runtimePort int) (runtime.EnsuredWorker, error) {
	if runtimePort != 0 && s.runtime == nil {
		return runtime.EnsuredWorker{Port: runtimePort, Release: func() {}}, nil
	}
	if s.runtime == nil {
		if active.Deployment.Port == 0 {
			return runtime.EnsuredWorker{}, nanoflare.ErrAppNotFound
		}
		return runtime.EnsuredWorker{Port: active.Deployment.Port, Release: func() {}}, nil
	}
	return s.runtime.Ensure(r.Context(), active)
}

func (s *Server) ensureWorkerTraced(r *http.Request, active nanoflare.ActiveDeployment, runtimePort int) (runtime.EnsuredWorker, error) {
	ctx, span := gatewayTracer.Start(r.Context(), "worker.runtime_ensure", trace.WithAttributes(
		attribute.Bool("nanoflare.runtime.explicit_port", runtimePort != 0),
	))
	endpoint, err := s.ensureWorker(r.WithContext(ctx), active, runtimePort)
	if err != nil {
		recordSpanError(span, err)
	} else {
		span.SetAttributes(
			attribute.Bool("nanoflare.runtime.unix_socket", endpoint.SocketPath != ""),
		)
	}
	span.End()
	return endpoint, err
}

func (s *Server) workerResponse(r *http.Request, endpoint runtime.EnsuredWorker, requestPath, escapedRequestPath string) (*http.Response, error) {
	ctx, span := gatewayTracer.Start(r.Context(), "worker.upstream_ttfb", trace.WithAttributes(
		attribute.Bool("nanoflare.runtime.unix_socket", endpoint.SocketPath != ""),
	))
	defer span.End()
	target := &url.URL{
		Scheme:   "http",
		Host:     "127.0.0.1:" + strconv.Itoa(endpoint.Port),
		Path:     requestPath,
		RawPath:  escapedRequestPath,
		RawQuery: r.URL.RawQuery,
	}
	if endpoint.SocketPath != "" {
		target.Host = runtime.SocketHost(endpoint.SocketPath)
	}
	if endpoint.URL != "" {
		base, err := url.Parse(endpoint.URL)
		if err != nil {
			return nil, err
		}
		target.Scheme, target.Host = base.Scheme, base.Host
		target.Path = base.Path + requestPath
		if escapedRequestPath != "" {
			target.RawPath = base.EscapedPath() + escapedRequestPath
		}
	}
	request, err := http.NewRequestWithContext(ctx, r.Method, target.String(), r.Body)
	if err != nil {
		recordSpanError(span, err)
		return nil, err
	}
	s.workerGatewayMetrics.requests.Add(1)
	var getConnAt time.Time
	clientTrace := &httptrace.ClientTrace{
		GetConn: func(string) {
			getConnAt = time.Now()
		},
		GotConn: func(info httptrace.GotConnInfo) {
			s.workerGatewayMetrics.connections.Add(1)
			if info.Reused {
				s.workerGatewayMetrics.reused.Add(1)
			}
			if info.WasIdle {
				s.workerGatewayMetrics.idle.Add(1)
			}
			attributes := []attribute.KeyValue{
				attribute.Bool("network.connection.reused", info.Reused),
				attribute.Bool("network.connection.was_idle", info.WasIdle),
			}
			if !getConnAt.IsZero() {
				attributes = append(attributes, attribute.Float64("network.connection.wait_ms", float64(time.Since(getConnAt))/float64(time.Millisecond)))
			}
			span.AddEvent("connection.acquired", trace.WithAttributes(attributes...))
		},
		GotFirstResponseByte: func() { span.AddEvent("first_response_byte") },
	}
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), clientTrace))
	request.Header = r.Header.Clone()
	for name := range request.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-nanoflare-") {
			delete(request.Header, name)
		}
	}
	for name, values := range endpoint.Headers {
		request.Header[name] = append([]string(nil), values...)
	}
	if endpoint.Token != "" {
		request.Header.Set("X-Nanoflare-Runner-Token", endpoint.Token)
	}
	request.Host = r.Host
	otel.GetTextMapPropagator().Inject(request.Context(), propagation.HeaderCarrier(request.Header))
	response, err := s.workerClient.Do(request)
	if err != nil {
		s.workerGatewayMetrics.errors.Add(1)
		recordSpanError(span, err)
		return nil, err
	}
	span.SetAttributes(attribute.Int("http.response.status_code", response.StatusCode))
	return response, nil
}

func writeWorkerResponse(ctx context.Context, w http.ResponseWriter, response *http.Response) {
	_, span := gatewayTracer.Start(ctx, "worker.response_copy")
	defer span.End()
	if response.StatusCode == http.StatusSwitchingProtocols {
		upstream, ok := response.Body.(io.ReadWriteCloser)
		hijacker, canHijack := w.(http.Hijacker)
		if !ok || !canHijack {
			http.Error(w, "WebSocket upgrade unavailable", http.StatusBadGateway)
			return
		}
		conn, buffered, err := hijacker.Hijack()
		if err != nil {
			recordSpanError(span, err)
			return
		}
		defer conn.Close()
		if _, err := fmt.Fprintf(buffered, "HTTP/1.1 101 Switching Protocols\r\n"); err != nil {
			recordSpanError(span, err)
			return
		}
		if err := response.Header.Write(buffered); err != nil {
			recordSpanError(span, err)
			return
		}
		if _, err := buffered.WriteString("\r\n"); err != nil {
			recordSpanError(span, err)
			return
		}
		if err := buffered.Flush(); err != nil {
			recordSpanError(span, err)
			return
		}
		done := make(chan struct{})
		go func() { io.Copy(upstream, buffered); upstream.Close(); close(done) }()
		io.Copy(conn, upstream)
		conn.Close()
		<-done
		return
	}
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	written, err := io.Copy(flushingResponseWriter{w}, response.Body)
	span.SetAttributes(attribute.Int64("http.response.body.size", written))
	if err != nil {
		recordSpanError(span, err)
	}
}

type flushingResponseWriter struct{ http.ResponseWriter }

func (w flushingResponseWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
	return n, err
}

func recordSpanError(span trace.Span, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
