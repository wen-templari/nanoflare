package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type controlContextKey string

const (
	controlUserKey controlContextKey = "controlUser"
	controlOrgKey  controlContextKey = "controlOrg"
	orgHeaderName                    = "X-Nanoflare-Org-ID"
)

func isPublicControlPath(path string) bool {
	switch path {
	case "/v1/setup/signup", "/v1/auth/login", "/v1/auth/validate", "/v1/auth/userinfo":
		return true
	default:
		return false
	}
}

func (s *Server) authenticateControlRequest(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	token := strings.TrimSpace(bearerToken(r))
	if token == "" {
		writeError(w, http.StatusUnauthorized, errors.New("bearer token is required"))
		return nil, false
	}
	user, err := s.controlAuth.ValidateToken(token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return nil, false
	}
	orgID := strings.TrimSpace(r.Header.Get(orgHeaderName))
	if r.URL.Path != "/v1/auth/me" {
		if orgID == "" {
			writeError(w, http.StatusBadRequest, errors.New("X-Nanoflare-Org-ID is required"))
			return nil, false
		}
		ok, err := s.controlAuth.UserBelongsToOrganization(user.ID, orgID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return nil, false
		}
		if !ok {
			writeError(w, http.StatusForbidden, nanoflare.ErrMembershipNotFound)
			return nil, false
		}
	}
	ctx := context.WithValue(r.Context(), controlUserKey, user)
	ctx = context.WithValue(ctx, controlOrgKey, orgID)
	return r.WithContext(ctx), true
}

func controlOrgID(r *http.Request) string {
	value, _ := r.Context().Value(controlOrgKey).(string)
	return value
}

func controlUser(r *http.Request) nanoflare.User {
	value, _ := r.Context().Value(controlUserKey).(nanoflare.User)
	return value
}

func (s *Server) registerControlAuthRoutes() {
	s.mux.HandleFunc("POST /v1/setup/signup", s.controlSignup)
	s.mux.HandleFunc("POST /v1/auth/login", s.controlLogin)
	s.mux.HandleFunc("GET /v1/auth/me", s.controlMe)
}

func (s *Server) controlSignup(w http.ResponseWriter, r *http.Request) {
	var input nanoflare.SignupInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session, err := s.controlAuth.Signup(input)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "already complete") {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusCreated, session)
}

func (s *Server) controlLogin(w http.ResponseWriter, r *http.Request) {
	var input nanoflare.LoginInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session, err := s.controlAuth.Login(input)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, nanoflare.ErrInvalidCredentials) {
			status = http.StatusUnauthorized
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) controlMe(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(bearerToken(r))
	session, err := s.controlAuth.Me(token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if active := strings.TrimSpace(controlOrgID(r)); active != "" {
		session.ActiveOrgID = active
	}
	_ = controlUser(r)
	writeJSON(w, http.StatusOK, session)
}
