package api

import (
	"github.com/clas/nanoflare/internal/nanoflare"
	"net/http"
)

// RuntimeAccessServer resolves verified Nanoflare browser identity for the
// requesting platform Worker. Identity assertions are normal signed user JWTs;
// a runtime capability alone cannot invent a user or cross organizations.
type RuntimeAccessServer struct {
	service *nanoflare.Service
	auth    *nanoflare.ControlAuthService
}

func NewRuntimeAccessServer(service *nanoflare.Service, auth *nanoflare.ControlAuthService) *RuntimeAccessServer {
	return &RuntimeAccessServer{service, auth}
}
func (s *RuntimeAccessServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != "GET" || s.auth == nil {
		http.Error(w, "identity unavailable", 403)
		return
	}
	principal, err := s.service.AuthorizeRuntime(r.Context(), bearerToken(r), nanoflare.RuntimeAccess, "")
	if err != nil {
		http.Error(w, "identity unavailable", 403)
		return
	}
	assertion := r.Header.Get("X-Nanoflare-User-JWT")
	if len(r.Header.Values("X-Nanoflare-User-JWT")) != 1 {
		http.Error(w, "identity unavailable", 403)
		return
	}
	session, err := s.auth.Me(assertion)
	if err != nil {
		http.Error(w, "identity unavailable", 403)
		return
	}
	if _, err = s.auth.Membership(session.User.ID, principal.App.OrgID); err != nil {
		http.Error(w, "identity unavailable", 403)
		return
	}
	writeJSON(w, 200, map[string]string{"email": session.User.Email, "user_uuid": session.User.ID})
}

// RuntimeAccessHandler is mounted only on the daemon's private runtime listener.
func (s *Server) RuntimeAccessHandler() http.Handler {
	return NewRuntimeAccessServer(s.service, s.controlAuth)
}
