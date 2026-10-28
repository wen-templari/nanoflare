package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/clas/nanoflare/internal/config"
	"github.com/clas/nanoflare/internal/egress"
	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtimeassets"
)

type RuntimeDispatchServer struct {
	service  *nanoflare.Service
	objects  *Server
	mux      *http.ServeMux
	outbound http.RoundTripper
	output   RuntimeLogAppender
	dns      http.Handler
}

func NewRuntimeDispatchServer(service *nanoflare.Service, objects *Server, transports ...http.RoundTripper) *RuntimeDispatchServer {
	server := &RuntimeDispatchServer{service: service, objects: objects, mux: http.NewServeMux()}
	if len(transports) > 0 && transports[0] != nil {
		server.outbound = transports[0]
	} else {
		server.outbound = defaultDispatchOutbound
	}
	server.mux.HandleFunc("POST /internal/runtime/dispatch/dns", server.lookupDNS)
	server.mux.HandleFunc("POST /internal/runtime/dispatch/log", server.log)
	server.mux.HandleFunc("/internal/runtime/dispatch/outbound", server.forwardOutbound)
	server.mux.HandleFunc("POST /internal/runtime/dispatch/acquire", server.acquire)
	server.mux.HandleFunc("POST /internal/runtime/dispatch/finish", server.finish)
	server.mux.HandleFunc("POST /internal/runtime/dispatch/nested", server.nested)
	server.mux.HandleFunc("/internal/runtime/dispatch/effect/{kind}/{binding}/{path...}", server.effect)
	return server
}
func (s *RuntimeDispatchServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}
func (s *RuntimeDispatchServer) acquire(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Script string `json:"script"`
		Root   string `json:"root"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	if input.Root == "" {
		writeRuntimeError(w, nanoflare.ErrInvocationExpired)
		return
	}
	artifact, err := s.service.AcquireDispatchWithRoot(r.Context(), bearerToken(r), r.Header.Get("X-Nanoflare-Dispatch-Namespace-ID"), input.Script, input.Root)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeDispatchArtifact(w, r, artifact)
}
func (s *RuntimeDispatchServer) finish(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Lease  string `json:"lease"`
		Cancel bool   `json:"cancel"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	if err := s.service.FinishDispatch(r.Context(), bearerToken(r), input.Lease, input.Cancel); err != nil {
		writeRuntimeError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *RuntimeDispatchServer) effect(w http.ResponseWriter, r *http.Request) {
	kind, binding := r.PathValue("kind"), r.PathValue("binding")
	if kind != "kv" && kind != "db" && kind != "objects" && kind != "assets" {
		writeRuntimeError(w, nanoflare.ErrRuntimeGrantDenied)
		return
	}
	lease := r.Header.Get("X-Nanoflare-Invocation")
	resource, err := s.service.DispatchResource(r.Context(), lease, kind, binding)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	cost := 1
	// Statement counts come from the trusted decoder, never a caller header.
	if kind == nanoflare.RuntimeDatabase {
		payload, e := io.ReadAll(io.LimitReader(r.Body, maxJSONBodySize+1))
		if e != nil || len(payload) > maxJSONBodySize {
			http.Error(w, "database request too large", 413)
			return
		}
		var query nanoflare.DBQueryRequest
		if e = json.Unmarshal(payload, &query); e != nil {
			writeError(w, 400, e)
			return
		}
		cost = 0
		for _, statement := range query.Statements {
			count, e := nanoflare.InspectSQL(statement.SQL)
			if e != nil {
				writeRuntimeError(w, e)
				return
			}
			cost += count
		}
		if cost < 1 {
			writeRuntimeError(w, nanoflare.ErrSQLPolicy)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(payload))
	}
	if _, err = s.service.DispatchEffect(r.Context(), bearerToken(r), lease, kind, resource, cost); err != nil {
		writeRuntimeError(w, err)
		return
	}
	effectContext, cancel, err := s.service.DispatchEffectContext(r.Context(), lease)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	defer cancel()
	r = r.Clone(effectContext)
	r.Header = r.Header.Clone()
	r.Header.Set("Authorization", "Bearer "+lease)
	r.URL.RawPath = ""
	switch kind {
	case nanoflare.RuntimeKV:
		r.URL.Path = "/" + r.PathValue("path")
		r.Header.Set("X-Nanoflare-KV-Namespace-ID", resource)
		NewRuntimeKVServer(s.service).ServeHTTP(w, r)
	case nanoflare.RuntimeDatabase:
		r.URL.Path = "/internal/runtime/db"
		r.Header.Set("X-Nanoflare-Database-ID", resource)
		NewRuntimeDBServer(s.service).ServeHTTP(w, r)
	case nanoflare.RuntimeAssets:
		r.URL.Path = "/internal/runtime/assets/" + strings.TrimPrefix(r.PathValue("path"), "/")
		r.Header.Set("X-Nanoflare-Deployment-ID", resource)
		NewRuntimeAssetServer(s.service).ServeHTTP(w, r)
	case nanoflare.RuntimeObjects:
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "PUT" && r.Method != "DELETE" {
			http.Error(w, "unsupported object effect", 405)
			return
		}
		if s.objects == nil {
			http.Error(w, "object runtime unavailable", 503)
			return
		}
		objectPath := strings.TrimPrefix(r.PathValue("path"), "internal/runtime/objects/")
		r.URL.Path = "/internal/runtime/objects/" + strings.TrimPrefix(objectPath, "/")
		r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
		r.Header.Set("X-Nanoflare-Object-Bucket-ID", resource)
		s.objects.mux.ServeHTTP(w, r)
	}
}

func (s *RuntimeDispatchServer) writeDispatchArtifact(w http.ResponseWriter, r *http.Request, artifact nanoflare.DispatchArtifact) {
	modules, err := config.NamespaceWorkerModules(artifact.Deployment)
	if err != nil {
		_ = s.service.FinishDispatch(r.Context(), bearerToken(r), artifact.Lease, true)
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, 200, struct {
		nanoflare.DispatchArtifact
		Wrapper         string                             `json:"wrapper"`
		RuntimeBindings string                             `json:"runtime_bindings"`
		DynamicEnv      string                             `json:"dynamic_env"`
		Modules         map[string]config.WorkerCodeModule `json:"modules"`
	}{artifact, config.DynamicEntrypoint(artifact.Deployment), runtimeassets.BindingsModule, runtimeassets.DynamicEnvModule, modules})
}
func (s *RuntimeDispatchServer) nested(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Lease   string `json:"lease"`
		Binding string `json:"binding"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	artifact, err := s.service.AcquireServiceDispatch(r.Context(), bearerToken(r), input.Lease, input.Binding)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeDispatchArtifact(w, r, artifact)
}

func (s *RuntimeDispatchServer) forwardOutbound(w http.ResponseWriter, r *http.Request) {
	lease := r.Header.Get("X-Nanoflare-Invocation")
	if _, err := s.service.DispatchEffect(r.Context(), bearerToken(r), lease, nanoflare.RuntimeOutbound, "", 1); err != nil {
		writeRuntimeError(w, err)
		return
	}
	ctx, cancel, err := s.service.DispatchEffectContext(r.Context(), lease)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	defer cancel()
	target, err := url.Parse(r.Header.Get("X-Nanoflare-Outbound-URL"))
	if err != nil || target == nil || target.Host == "" || target.User != nil || (target.Scheme != "https" && target.Scheme != "http") || r.Method == http.MethodConnect || r.Header.Get("Upgrade") != "" {
		http.Error(w, "unsupported outbound request", 422)
		return
	}
	authorization := r.Header.Get("X-Nanoflare-Outbound-Authorization")
	out := r.Clone(ctx)
	out.URL = target
	out.RequestURI = ""
	out.Host = target.Host
	out.Header = r.Header.Clone()
	for name := range out.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-nanoflare-") {
			out.Header.Del(name)
		}
	}
	out.Header.Del("Authorization")
	if authorization != "" {
		out.Header.Set("Authorization", authorization)
	}
	out.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	response, err := s.outbound.RoundTrip(out)
	if err != nil {
		if errors.Is(err, egress.ErrDestinationDenied) {
			http.Error(w, "outbound destination denied", 403)
		} else {
			http.Error(w, "outbound request failed", 502)
		}
		return
	}
	defer response.Body.Close()
	if response.StatusCode == 101 {
		http.Error(w, "outbound upgrade unavailable", 501)
		return
	}
	for name, values := range response.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	io.Copy(dispatchFlushWriter{w}, response.Body)
}

var defaultDispatchOutbound = func() http.RoundTripper { transport, _ := egress.NewHTTPTransport(egress.Config{}); return transport }()

type dispatchFlushWriter struct{ http.ResponseWriter }

func (w dispatchFlushWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
	return n, err
}

func (s *RuntimeDispatchServer) SetLogOutput(output RuntimeLogAppender) *RuntimeDispatchServer {
	s.output = output
	return s
}
func (s *RuntimeDispatchServer) log(w http.ResponseWriter, r *http.Request) {
	lease := r.Header.Get("X-Nanoflare-Invocation")
	if err := s.service.AuthorizeDispatchTransport(r.Context(), bearerToken(r), lease); err != nil {
		writeRuntimeError(w, err)
		return
	}
	var input struct {
		Level   string `json:"level"`
		Message string `json:"message"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	line, accepted, err := s.service.ProcessRuntimeLog(r.Context(), lease, input.Level, input.Message)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	if accepted {
		appendRuntimeLog(s.output, line)
	}
	w.WriteHeader(204)
}

// SetDNSHandler supplies the platform resolver, including its default profile.
// No caller headers or URL parameters are forwarded to the resolver.
func (s *RuntimeDispatchServer) SetDNSHandler(handler http.Handler) *RuntimeDispatchServer {
	s.dns = handler
	return s
}
func (s *RuntimeDispatchServer) lookupDNS(w http.ResponseWriter, r *http.Request) {
	if s.dns == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "EAI_AGAIN", "error": "platform DNS unavailable"})
		return
	}
	lease := r.Header.Get("X-Nanoflare-Invocation")
	if _, err := s.service.DispatchEffect(r.Context(), bearerToken(r), lease, nanoflare.RuntimeDNS, "", 1); err != nil {
		writeDNSBrokerError(w, err)
		return
	}
	ctx, cancel, err := s.service.DispatchEffectContext(r.Context(), lease)
	if err != nil {
		writeDNSBrokerError(w, err)
		return
	}
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://nanoflare.dns/lookup", http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeError(w, 400, err)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	s.dns.ServeHTTP(w, request)
}

// Broker admission failures are terminal invocation errors, not resolver retries.
func writeDNSBrokerError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "ERR_NANOFLARE_DNS_BROKER"
	switch {
	case errors.Is(err, nanoflare.ErrInvocationExpired):
		status, code = http.StatusGone, "ERR_NANOFLARE_INVOCATION_EXPIRED"
	case errors.Is(err, nanoflare.ErrSubrequestLimit), errors.Is(err, nanoflare.ErrDispatchDepth):
		status, code = http.StatusTooManyRequests, "ERR_NANOFLARE_EFFECT_LIMIT"
	case errors.Is(err, nanoflare.ErrRuntimeGrantDenied), errors.Is(err, nanoflare.ErrNamespaceGrantDenied):
		status, code = http.StatusForbidden, "ERR_NANOFLARE_ACCESS_DENIED"
	case errors.Is(err, nanoflare.ErrInvalidCapability):
		status, code = http.StatusUnauthorized, "ERR_NANOFLARE_ACCESS_DENIED"
	case errors.Is(err, nanoflare.ErrDispatchUnavailable):
		status, code = http.StatusServiceUnavailable, "ERR_NANOFLARE_DISPATCH_UNAVAILABLE"
	case errors.Is(err, nanoflare.ErrDispatchOverloaded):
		status, code = http.StatusTooManyRequests, "ERR_NANOFLARE_DISPATCH_OVERLOADED"
	}
	writeJSON(w, status, map[string]any{"code": code, "error": err.Error(), "detail": err.Error(), "status": status})
}
