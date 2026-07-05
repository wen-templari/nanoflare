package api

import (
	"context"
	"errors"
	"net/http"
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
	s.registerAppRoutes()
	s.registerKVRoutes()
	s.registerObjectRoutes()
	s.registerAuthRoutes()
	s.registerInternalRoutes()
}
