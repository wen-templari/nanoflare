package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type controlContextKey string

const (
	controlUserKey   controlContextKey = "controlUser"
	controlOrgKey    controlContextKey = "controlOrg"
	controlOAuthKey  controlContextKey = "controlOAuth"
	controlPATKey    controlContextKey = "controlPAT"
	controlScopesKey controlContextKey = "controlScopes"
	orgHeaderName                      = "X-Nanoflare-Org-ID"
)

func isPublicControlPath(path string) bool {
	switch path {
	case "/v1/setup/signup", "/v1/auth/signup", "/v1/auth/login", "/v1/auth/refresh", "/v1/auth/pat/session", "/v1/auth/validate", "/v1/auth/userinfo", "/v1/auth/oidc/config", "/v1/auth/oidc/start", "/v1/auth/oidc/callback", "/v1/auth/oidc/cli", "/v1/auth/oidc/logout", "/v1/auth/oidc/session", "/v1/auth/cli/session", "/v1/oauth/authorize", "/v1/oauth/token", "/v1/oauth/revoke":
		return true
	default:
		if strings.HasPrefix(path, "/v1/invites/") {
			return true
		}
		return false
	}
}

func (s *Server) authenticateControlRequest(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	token := strings.TrimSpace(bearerToken(r))
	if token == "" {
		writeError(w, http.StatusUnauthorized, errors.New("bearer token is required"))
		return nil, false
	}
	if s.oauth != nil && !isUserSessionOnlyOAuthPath(r.URL.Path) {
		access, err := s.oauth.ValidateAccessToken(token)
		if err == nil {
			ctx := context.WithValue(r.Context(), controlOrgKey, access.OrgID)
			ctx = context.WithValue(ctx, controlOAuthKey, access)
			return r.WithContext(ctx), true
		}
	}
	if !isUserSessionOnlyControlPath(r.URL.Path) {
		access, err := s.controlAuth.ValidatePersonalAccessToken(token, r.Header.Get(orgHeaderName))
		if err == nil {
			ctx := context.WithValue(r.Context(), controlUserKey, access.User)
			ctx = context.WithValue(ctx, controlOrgKey, access.OrgID)
			ctx = context.WithValue(ctx, controlPATKey, access)
			ctx = context.WithValue(ctx, controlScopesKey, access.Scopes)
			return r.WithContext(ctx), true
		}
	}
	user, err := s.controlAuth.ValidateToken(token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return nil, false
	}
	orgID := strings.TrimSpace(r.Header.Get(orgHeaderName))
	if r.URL.Path != "/v1/auth/me" && !isNoOrgControlPath(r.URL.Path) {
		if orgID == "" {
			writeError(w, http.StatusBadRequest, errors.New("X-Nanoflare-Org-ID is required"))
			return nil, false
		}
		membership, err := s.controlAuth.Membership(user.ID, orgID)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, nanoflare.ErrMembershipNotFound) {
				status = http.StatusForbidden
			}
			writeError(w, status, err)
			return nil, false
		}
		ctx := context.WithValue(r.Context(), controlUserKey, user)
		ctx = context.WithValue(ctx, controlOrgKey, orgID)
		ctx = context.WithValue(ctx, controlScopesKey, membership.Scopes)
		return r.WithContext(ctx), true
	}
	ctx := context.WithValue(r.Context(), controlUserKey, user)
	ctx = context.WithValue(ctx, controlOrgKey, orgID)
	return r.WithContext(ctx), true
}

func isUserSessionOnlyOAuthPath(path string) bool {
	return path == "/v1/oauth/authorize" || path == "/v1/oauth/clients" || strings.HasPrefix(path, "/v1/oauth/clients/") || path == "/v1/oauth/connections" || strings.HasPrefix(path, "/v1/oauth/connections/")
}

func isUserSessionOnlyControlPath(path string) bool {
	return isUserSessionOnlyOAuthPath(path) || path == "/v1/auth/me" || path == "/v1/auth/cli/code" || path == "/v1/orgs" || path == "/v1/pats" || strings.HasPrefix(path, "/v1/pats/")
}

func isNoOrgControlPath(path string) bool {
	return path == "/v1/oauth/authorize" || path == "/v1/orgs" || path == "/v1/auth/cli/code" || path == "/v1/pats" || strings.HasPrefix(path, "/v1/pats/")
}

func controlOrgID(r *http.Request) string {
	value, _ := r.Context().Value(controlOrgKey).(string)
	return value
}

func controlUser(r *http.Request) nanoflare.User {
	value, _ := r.Context().Value(controlUserKey).(nanoflare.User)
	return value
}

func controlOAuthAccess(r *http.Request) (nanoflare.OAuthAccess, bool) {
	value, ok := r.Context().Value(controlOAuthKey).(nanoflare.OAuthAccess)
	return value, ok
}

func controlPATAccess(r *http.Request) (nanoflare.PATAccess, bool) {
	value, ok := r.Context().Value(controlPATKey).(nanoflare.PATAccess)
	return value, ok
}

func (s *Server) requireScope(w http.ResponseWriter, r *http.Request, scope string) bool {
	if s.controlAuth == nil {
		return true
	}
	access, ok := controlOAuthAccess(r)
	if ok {
		for _, candidate := range access.Scopes {
			if candidate == scope {
				return true
			}
		}
		writeError(w, http.StatusForbidden, errors.New("oauth scope is required: "+scope))
		return false
	}
	if _, ok := controlPATAccess(r); ok {
		scopes, _ := r.Context().Value(controlScopesKey).([]string)
		for _, candidate := range scopes {
			if candidate == scope {
				return true
			}
		}
		writeError(w, http.StatusForbidden, errors.New("personal access token scope is required: "+scope))
		return false
	}
	scopes, _ := r.Context().Value(controlScopesKey).([]string)
	for _, candidate := range scopes {
		if candidate == scope {
			return true
		}
	}
	writeError(w, http.StatusForbidden, errors.New("scope is required: "+scope))
	return false
}

func (s *Server) registerControlAuthRoutes() {
	s.mux.HandleFunc("POST /v1/setup/signup", s.controlSignup)
	s.mux.HandleFunc("POST /v1/auth/signup", s.controlSignup)
	s.mux.HandleFunc("POST /v1/auth/login", s.controlLogin)
	s.mux.HandleFunc("POST /v1/auth/refresh", s.controlRefresh)
	s.mux.HandleFunc("POST /v1/auth/pat/session", s.controlPATSession)
	s.mux.HandleFunc("GET /v1/auth/oidc/config", s.controlOIDCConfig)
	s.mux.HandleFunc("GET /v1/auth/oidc/start", s.controlOIDCStart)
	s.mux.HandleFunc("GET /v1/auth/oidc/callback", s.controlOIDCCallback)
	s.mux.HandleFunc("GET /v1/auth/oidc/cli", s.controlOIDCCLI)
	s.mux.HandleFunc("GET /v1/auth/oidc/logout", s.controlOIDCLogout)
	s.mux.HandleFunc("POST /v1/auth/oidc/session", s.controlOIDCSession)
	s.mux.HandleFunc("POST /v1/auth/cli/code", s.controlCLICode)
	s.mux.HandleFunc("POST /v1/auth/cli/session", s.controlCLISession)
	s.mux.HandleFunc("GET /v1/auth/me", s.controlMe)
	s.mux.HandleFunc("GET /v1/pats", s.personalAccessTokens)
	s.mux.HandleFunc("POST /v1/pats", s.createPersonalAccessToken)
	s.mux.HandleFunc("DELETE /v1/pats/{patID}", s.revokePersonalAccessToken)
	s.mux.HandleFunc("POST /v1/orgs", s.createOrganization)
	s.mux.HandleFunc("GET /v1/orgs/{orgID}/members", s.organizationMembers)
	s.mux.HandleFunc("PATCH /v1/orgs/{orgID}/members/{userID}", s.updateOrganizationMember)
	s.mux.HandleFunc("DELETE /v1/orgs/{orgID}/members/{userID}", s.deleteOrganizationMember)
	s.mux.HandleFunc("POST /v1/orgs/{orgID}/invites", s.createOrganizationInvite)
	s.mux.HandleFunc("GET /v1/orgs/{orgID}/invites", s.organizationInvites)
	s.mux.HandleFunc("DELETE /v1/orgs/{orgID}/invites/{inviteID}", s.revokeOrganizationInvite)
	s.mux.HandleFunc("GET /v1/invites/{token}", s.inviteInfo)
	s.mux.HandleFunc("POST /v1/invites/{token}/accept", s.acceptInvite)
}

func (s *Server) controlOIDCConfig(w http.ResponseWriter, r *http.Request) {
	enabled := s.controlAuth != nil && s.controlOIDC != nil && s.controlOIDC.BrowserFlowEnabled()
	writeJSON(w, http.StatusOK, map[string]bool{
		"enabled":      enabled,
		"direct_login": enabled && s.controlOIDCDirectLogin,
	})
}

func (s *Server) controlOIDCStart(w http.ResponseWriter, r *http.Request) {
	if s.controlOIDC == nil || !s.controlOIDC.BrowserFlowEnabled() {
		writeAuthError(w, errAuthDisabled)
		return
	}
	if err := s.controlOIDC.BeginConsoleAuth(w, r, r.URL.Query().Get("next")); err != nil {
		writeAuthError(w, err)
	}
}

func (s *Server) controlOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.controlOIDC == nil || !s.controlOIDC.BrowserFlowEnabled() {
		writeAuthError(w, errAuthDisabled)
		return
	}
	result, next, err := s.controlOIDC.HandleConsoleCallback(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	code, err := randomControlCode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.controlOIDCMu.Lock()
	if s.controlOIDCCodes == nil {
		s.controlOIDCCodes = make(map[string]controlOIDCCode)
	}
	s.controlOIDCCodes[code] = controlOIDCCode{Result: result, ExpiresAt: time.Now().UTC().Add(5 * time.Minute)}
	s.controlOIDCMu.Unlock()

	values := url.Values{}
	values.Set("oidc_code", code)
	next = safeControlNext(next)
	if next == "/v1/auth/oidc/cli" {
		http.Redirect(w, r, next+"?"+values.Encode(), http.StatusFound)
		return
	}
	values.Set("next", next)
	http.Redirect(w, r, "/login?"+values.Encode(), http.StatusFound)
}

func (s *Server) controlOIDCCLI(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.URL.Query().Get("oidc_code"))
	if code == "" {
		writeError(w, http.StatusBadRequest, errors.New("oidc_code is required"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><title>Nanoflare CLI Login</title><style>body{font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:3rem;line-height:1.5;color:#111827}code{display:inline-block;padding:.75rem 1rem;border:1px solid #d1d5db;border-radius:.5rem;background:#f9fafb;font-size:1.1rem}</style></head><body><h1>Nanoflare CLI login</h1><p>Copy this one-time code back into your terminal:</p><p><code>` + code + `</code></p><p>You can close this tab after the CLI confirms login.</p></body></html>`))
}

func (s *Server) controlOIDCLogout(w http.ResponseWriter, r *http.Request) {
	next := safeControlNext(r.URL.Query().Get("next"))
	if next == "/" {
		next = "/login?sso_logged_out=1"
	}
	if s.controlOIDC == nil || !s.controlOIDC.BrowserFlowEnabled() {
		http.Redirect(w, r, next, http.StatusFound)
		return
	}
	logoutURL, err := s.controlOIDC.ConsoleLogoutURL(r.Context(), next)
	if err != nil {
		http.Redirect(w, r, next, http.StatusFound)
		return
	}
	http.Redirect(w, r, logoutURL, http.StatusFound)
}

func (s *Server) controlOIDCSession(w http.ResponseWriter, r *http.Request) {
	if s.controlOIDC == nil || !s.controlOIDC.BrowserFlowEnabled() {
		writeAuthError(w, errAuthDisabled)
		return
	}
	var input struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	code := strings.TrimSpace(input.Code)
	if code == "" {
		writeError(w, http.StatusBadRequest, errors.New("code is required"))
		return
	}
	s.controlOIDCMu.Lock()
	pending, ok := s.controlOIDCCodes[code]
	if ok {
		delete(s.controlOIDCCodes, code)
	}
	s.controlOIDCMu.Unlock()
	if !ok || !pending.ExpiresAt.After(time.Now().UTC()) {
		writeError(w, http.StatusUnauthorized, errors.New("oidc login code is invalid or expired"))
		return
	}
	session, err := s.controlAuth.LoginOIDC(nanoflare.OIDCLoginInput{
		Issuer:  s.controlOIDC.Issuer(),
		Subject: pending.Result.Subject,
		Email:   pending.Result.Email,
	})
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) controlCLICode(w http.ResponseWriter, r *http.Request) {
	user := controlUser(r)
	if user.ID == "" {
		writeError(w, http.StatusUnauthorized, errors.New("signed-in Nanoflare user is required"))
		return
	}
	code, err := randomControlCode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.controlOIDCMu.Lock()
	if s.controlCLICodes == nil {
		s.controlCLICodes = make(map[string]controlCLICode)
	}
	s.controlCLICodes[code] = controlCLICode{
		UserID:      user.ID,
		ActiveOrgID: strings.TrimSpace(controlOrgID(r)),
		ExpiresAt:   time.Now().UTC().Add(5 * time.Minute),
	}
	s.controlOIDCMu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]string{"code": code})
}

func (s *Server) controlCLISession(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	code := strings.TrimSpace(input.Code)
	if code == "" {
		writeError(w, http.StatusBadRequest, errors.New("code is required"))
		return
	}
	s.controlOIDCMu.Lock()
	pending, ok := s.controlCLICodes[code]
	if ok {
		delete(s.controlCLICodes, code)
	}
	s.controlOIDCMu.Unlock()
	if !ok || !pending.ExpiresAt.After(time.Now().UTC()) {
		writeError(w, http.StatusUnauthorized, errors.New("web login code is invalid or expired"))
		return
	}
	session, err := s.controlAuth.SessionForUserID(pending.UserID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if pending.ActiveOrgID != "" {
		for _, org := range session.Organizations {
			if org.ID == pending.ActiveOrgID {
				session.ActiveOrgID = pending.ActiveOrgID
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) controlRefresh(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session, err := s.controlAuth.Refresh(input.RefreshToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) controlPATSession(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session, err := s.controlAuth.SessionForPersonalAccessToken(input.Token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func safeControlNext(next string) string {
	next = strings.TrimSpace(next)
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
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
		if errors.Is(err, nanoflare.ErrUserExists) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	if r.URL.Path == "/v1/setup/signup" && strings.TrimSpace(input.OrganizationName) != "" {
		org, err := s.controlAuth.CreateOrganization(session.User.ID, nanoflare.CreateOrganizationInput{Name: input.OrganizationName})
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, nanoflare.ErrUsageLimitExceeded) {
				status = http.StatusPaymentRequired
			}
			writeError(w, status, err)
			return
		}
		session.Organizations = []nanoflare.Organization{org}
		session.ActiveOrgID = org.ID
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

func (s *Server) personalAccessTokens(w http.ResponseWriter, r *http.Request) {
	user := controlUser(r)
	if user.ID == "" {
		writeError(w, http.StatusUnauthorized, errors.New("signed-in Nanoflare user is required"))
		return
	}
	tokens, err := s.controlAuth.PersonalAccessTokens(user.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (s *Server) createPersonalAccessToken(w http.ResponseWriter, r *http.Request) {
	user := controlUser(r)
	if user.ID == "" {
		writeError(w, http.StatusUnauthorized, errors.New("signed-in Nanoflare user is required"))
		return
	}
	var input nanoflare.CreatePersonalAccessTokenInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	token, err := s.controlAuth.CreatePersonalAccessToken(user.ID, input)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, nanoflare.ErrMembershipNotFound) {
			status = http.StatusForbidden
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusCreated, token)
}

func (s *Server) revokePersonalAccessToken(w http.ResponseWriter, r *http.Request) {
	user := controlUser(r)
	if user.ID == "" {
		writeError(w, http.StatusUnauthorized, errors.New("signed-in Nanoflare user is required"))
		return
	}
	if err := s.controlAuth.RevokePersonalAccessToken(user.ID, r.PathValue("patID")); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, nanoflare.ErrPersonalAccessTokenNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createOrganization(w http.ResponseWriter, r *http.Request) {
	user := controlUser(r)
	if user.ID == "" {
		writeError(w, http.StatusUnauthorized, errors.New("signed-in Nanoflare user is required"))
		return
	}
	var input nanoflare.CreateOrganizationInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	org, err := s.controlAuth.CreateOrganization(user.ID, input)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, nanoflare.ErrUsageLimitExceeded) {
			status = http.StatusPaymentRequired
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusCreated, org)
}

func (s *Server) organizationMembers(w http.ResponseWriter, r *http.Request) {
	if !requirePathOrg(w, r) {
		return
	}
	if !s.requireScope(w, r, "members:read") {
		return
	}
	members, err := s.controlAuth.Members(r.PathValue("orgID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, members)
}

func (s *Server) updateOrganizationMember(w http.ResponseWriter, r *http.Request) {
	if !requirePathOrg(w, r) {
		return
	}
	if !s.requireScope(w, r, "members:write") {
		return
	}
	var input nanoflare.UpdateMembershipInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	current, err := s.controlAuth.Membership(r.PathValue("userID"), r.PathValue("orgID"))
	if err != nil {
		writeInviteError(w, err)
		return
	}
	nextRole := nanoflare.NormalizeRole(input.Role)
	if current.Role == nanoflare.RoleOwner || nextRole == nanoflare.RoleOwner {
		if !s.requireScope(w, r, "members:owner") {
			return
		}
	}
	member, err := s.controlAuth.UpdateMembership(r.PathValue("orgID"), r.PathValue("userID"), input)
	if err != nil {
		writeInviteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, member)
}

func (s *Server) deleteOrganizationMember(w http.ResponseWriter, r *http.Request) {
	if !requirePathOrg(w, r) {
		return
	}
	if !s.requireScope(w, r, "members:write") {
		return
	}
	current, err := s.controlAuth.Membership(r.PathValue("userID"), r.PathValue("orgID"))
	if err != nil {
		writeInviteError(w, err)
		return
	}
	if current.Role == nanoflare.RoleOwner && !s.requireScope(w, r, "members:owner") {
		return
	}
	if err := s.controlAuth.DeleteMembership(r.PathValue("orgID"), r.PathValue("userID")); err != nil {
		writeInviteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createOrganizationInvite(w http.ResponseWriter, r *http.Request) {
	if !requirePathOrg(w, r) {
		return
	}
	if !s.requireScope(w, r, "members:write") {
		return
	}
	var input nanoflare.CreateInviteInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	role := nanoflare.NormalizeRole(input.Role)
	if role == nanoflare.RoleOwner && !s.requireScope(w, r, "members:owner") {
		return
	}
	invite, err := s.controlAuth.CreateInvite(controlUser(r), r.PathValue("orgID"), input, requestBaseURL(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, invite)
}

func (s *Server) organizationInvites(w http.ResponseWriter, r *http.Request) {
	if !requirePathOrg(w, r) {
		return
	}
	if !s.requireScope(w, r, "members:read") {
		return
	}
	invites, err := s.controlAuth.Invites(r.PathValue("orgID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, invites)
}

func (s *Server) revokeOrganizationInvite(w http.ResponseWriter, r *http.Request) {
	if !requirePathOrg(w, r) {
		return
	}
	if !s.requireScope(w, r, "members:write") {
		return
	}
	if err := s.controlAuth.RevokeInvite(r.PathValue("orgID"), r.PathValue("inviteID")); err != nil {
		writeInviteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) inviteInfo(w http.ResponseWriter, r *http.Request) {
	invite, err := s.controlAuth.Invite(r.PathValue("token"))
	if err != nil {
		writeInviteError(w, err)
		return
	}
	invite.TokenHash = ""
	writeJSON(w, http.StatusOK, invite)
}

func (s *Server) acceptInvite(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	user, ok := s.optionalInviteUser(r)
	var session *nanoflare.AuthSession
	if !ok {
		var input nanoflare.AcceptInviteInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		nextSession, err := s.controlAuth.Signup(nanoflare.SignupInput{Email: input.Email, Password: input.Password})
		if err != nil {
			writeInviteError(w, err)
			return
		}
		session = &nextSession
		user = nextSession.User
	}
	membership, err := s.controlAuth.AcceptInvite(token, user)
	if err != nil {
		writeInviteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nanoflare.AcceptInviteResponse{Membership: membership, Session: session})
}

func (s *Server) optionalInviteUser(r *http.Request) (nanoflare.User, bool) {
	token := strings.TrimSpace(bearerToken(r))
	if token == "" {
		return nanoflare.User{}, false
	}
	user, err := s.controlAuth.ValidateToken(token)
	if err != nil {
		return nanoflare.User{}, false
	}
	return user, true
}

func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Host
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwarded != "" {
		host = forwarded
	}
	return scheme + "://" + host
}

func requirePathOrg(w http.ResponseWriter, r *http.Request) bool {
	if r.PathValue("orgID") == controlOrgID(r) {
		return true
	}
	writeError(w, http.StatusForbidden, nanoflare.ErrMembershipNotFound)
	return false
}

func writeInviteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, nanoflare.ErrInviteNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, nanoflare.ErrMembershipNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, nanoflare.ErrLastOwner):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, nanoflare.ErrInviteExpired), errors.Is(err, nanoflare.ErrInviteUsed), errors.Is(err, nanoflare.ErrInviteRevoked), errors.Is(err, nanoflare.ErrInviteEmailMismatch):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, nanoflare.ErrUserExists):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusBadRequest, err)
	}
}
