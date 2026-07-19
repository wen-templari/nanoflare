package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/runtime"
)

type Server struct {
	service          *nanoflare.Service
	traefik          TraefikConfigReader
	traefikToken     string
	auth             Authenticator
	controlAuth      *nanoflare.ControlAuthService
	controlOIDC      ControlOIDCAuthenticator
	controlOIDCMu    sync.Mutex
	controlOIDCCodes map[string]controlOIDCCode
	controlCLICodes  map[string]controlCLICode
	oauth            *nanoflare.OAuthService
	runtime          RuntimeEnsurer
	mux              *http.ServeMux
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
	server := &Server{service: service, traefik: traefik, traefikToken: token, auth: auth, mux: http.NewServeMux()}
	server.routes()
	return server
}

func NewServerWithControlAuth(service *nanoflare.Service, traefik TraefikConfigReader, token string, auth Authenticator, controlAuth *nanoflare.ControlAuthService) *Server {
	server := &Server{service: service, traefik: traefik, traefikToken: token, auth: auth, controlAuth: controlAuth, mux: http.NewServeMux()}
	server.routes()
	return server
}

func NewServerWithRuntime(service *nanoflare.Service, traefik TraefikConfigReader, token string, auth Authenticator, controlAuth *nanoflare.ControlAuthService, runtime RuntimeEnsurer) *Server {
	server := &Server{service: service, traefik: traefik, traefikToken: token, auth: auth, controlAuth: controlAuth, runtime: runtime, mux: http.NewServeMux()}
	server.routes()
	return server
}

func NewServerWithRuntimeAndOAuth(service *nanoflare.Service, traefik TraefikConfigReader, token string, auth Authenticator, controlAuth *nanoflare.ControlAuthService, oauth *nanoflare.OAuthService, runtime RuntimeEnsurer) *Server {
	server := &Server{service: service, traefik: traefik, traefikToken: token, auth: auth, controlAuth: controlAuth, oauth: oauth, runtime: runtime, mux: http.NewServeMux()}
	server.routes()
	return server
}

func (s *Server) SetControlOIDC(auth ControlOIDCAuthenticator) {
	s.controlOIDC = auth
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.controlAuth != nil && strings.HasPrefix(r.URL.Path, "/v1/") && !isPublicControlPath(r.URL.Path) {
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
	s.mux.HandleFunc("GET /metrics", s.prometheusMetrics)
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
}
