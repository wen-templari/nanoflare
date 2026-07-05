package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

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

func (s *Server) registerAuthRoutes() {
	s.mux.HandleFunc("POST /v1/auth/validate", s.validateAuthToken)
	s.mux.HandleFunc("POST /v1/auth/userinfo", s.authUserInfo)
	s.mux.HandleFunc("GET /internal/auth/verify", s.verifyAuth)
	s.mux.HandleFunc("GET /internal/auth/callback", s.authCallback)
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
