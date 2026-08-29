package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
	"github.com/danielgtaylor/huma/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Server struct {
	service                *nanoflare.Service
	traefik                TraefikConfigReader
	traefikToken           string
	auth                   Authenticator
	controlAuth            *nanoflare.ControlAuthService
	controlOIDC            ControlOIDCAuthenticator
	controlOIDCDirectLogin bool
	controlOIDCMu          sync.Mutex
	controlOIDCCodes       map[string]controlOIDCCode
	controlCLICodes        map[string]controlCLICode
	oauth                  *nanoflare.OAuthService
	partner                *nanoflare.PartnerService
	runtime                RuntimeEnsurer
	workerClient           *http.Client
	workerGatewayMetrics   workerGatewayMetrics
	durationTelemetry      durationStatsReader
	metricsHandler         http.Handler
	mux                    *http.ServeMux
	openAPI                huma.API
}

type durationStatsReader interface {
	Stats(string) runtime.DurationStats
}

type workerGatewayMetrics struct {
	requests    atomic.Int64
	errors      atomic.Int64
	connections atomic.Int64
	reused      atomic.Int64
	idle        atomic.Int64
}

type RuntimeEnsurer interface {
	Ensure(context.Context, nanoflare.ActiveDeployment) (runtime.EnsuredWorker, error)
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

type ControlOIDCAuthenticator interface {
	BrowserFlowEnabled() bool
	BeginConsoleAuth(http.ResponseWriter, *http.Request, string) error
	HandleConsoleCallback(*http.Request) (AuthResult, string, error)
	ConsoleLogoutURL(context.Context, string) (string, error)
	Issuer() string
}

type AuthResult struct {
	Valid     bool           `json:"valid"`
	Subject   string         `json:"subject,omitempty"`
	Email     string         `json:"email,omitempty"`
	ExpiresAt *time.Time     `json:"expires_at,omitempty"`
	Claims    map[string]any `json:"claims,omitempty"`
}

type controlOIDCCode struct {
	Result    AuthResult
	ExpiresAt time.Time
}

type controlCLICode struct {
	UserID      string
	ActiveOrgID string
	ExpiresAt   time.Time
}

var errAuthDisabled = errors.New("oidc authentication is not configured")

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
	server := newServer(service, traefik, token, auth, nil, nil, nil)
	server.routes()
	return server
}

func NewServerWithControlAuth(service *nanoflare.Service, traefik TraefikConfigReader, token string, auth Authenticator, controlAuth *nanoflare.ControlAuthService) *Server {
	server := newServer(service, traefik, token, auth, controlAuth, nil, nil)
	server.routes()
	return server
}

func NewServerWithRuntime(service *nanoflare.Service, traefik TraefikConfigReader, token string, auth Authenticator, controlAuth *nanoflare.ControlAuthService, runtime RuntimeEnsurer) *Server {
	server := newServer(service, traefik, token, auth, controlAuth, runtime, nil)
	server.routes()
	return server
}

func NewServerWithRuntimeAndOAuth(service *nanoflare.Service, traefik TraefikConfigReader, token string, auth Authenticator, controlAuth *nanoflare.ControlAuthService, oauth *nanoflare.OAuthService, runtime RuntimeEnsurer) *Server {
	server := newServer(service, traefik, token, auth, controlAuth, runtime, oauth)
	server.routes()
	return server
}

func (s *Server) SetPartnerService(partner *nanoflare.PartnerService) {
	s.partner = partner
	if partner != nil {
		s.registerPartnerRoutes()
	}
}

// SetDurationTelemetry makes worker execution durations available to the Prometheus exporter.
func (s *Server) SetDurationTelemetry(telemetry durationStatsReader) { s.durationTelemetry = telemetry }

func newServer(service *nanoflare.Service, traefik TraefikConfigReader, token string, auth Authenticator, controlAuth *nanoflare.ControlAuthService, runtime RuntimeEnsurer, oauth *nanoflare.OAuthService) *Server {
	mux := http.NewServeMux()
	server := &Server{
		service:      service,
		traefik:      traefik,
		traefikToken: token,
		auth:         auth,
		controlAuth:  controlAuth,
		oauth:        oauth,
		runtime:      runtime,
		workerClient: newWorkerGatewayClient(),
		mux:          mux,
	}
	registry := prometheus.NewRegistry()
	registry.MustRegister(serverMetricsCollector{server: server})
	server.metricsHandler = promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	server.openAPI = newOpenAPI(mux)
	return server
}

func newWorkerGatewayClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 1024
	transport.MaxIdleConnsPerHost = 128
	transport.MaxConnsPerHost = 128
	transport.IdleConnTimeout = 90 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.ExpectContinueTimeout = time.Second
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (s *Server) SetControlOIDC(auth ControlOIDCAuthenticator) {
	s.controlOIDC = auth
}

func (s *Server) SetControlOIDCDirectLogin(enabled bool) {
	s.controlOIDCDirectLogin = enabled
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Request-Id", newRequestID())
	if s.controlAuth != nil && strings.HasPrefix(r.URL.Path, "/v1/") && !isPublicControlPath(r.URL.Path) && !isPartnerMachineRequest(r) {
		next, ok := s.authenticateControlRequest(w, r)
		if !ok {
			return
		}
		r = next
	}
	s.mux.ServeHTTP(w, r)
}

func randomControlCode() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	s.mux.Handle("GET /metrics", s.metricsHandler)
	s.registerAppRoutes()
	s.registerKVRoutes()
	s.registerDBRoutes()
	s.registerObjectRoutes()
	s.registerAuthRoutes()
	if s.controlAuth != nil {
		s.registerControlAuthRoutes()
	}
	if s.oauth != nil {
		s.registerOAuthRoutes()
	}
	s.registerInternalRoutes()
	s.registerOpenAPIOperations()
}
