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
	controlUserKey   controlContextKey = "controlUser"
	controlOrgKey    controlContextKey = "controlOrg"
	controlOAuthKey  controlContextKey = "controlOAuth"
	controlScopesKey controlContextKey = "controlScopes"
	orgHeaderName                      = "X-Nanoflare-Org-ID"
)

func isPublicControlPath(path string) bool {
	switch path {
	case "/v1/setup/signup", "/v1/auth/signup", "/v1/auth/login", "/v1/auth/validate", "/v1/auth/userinfo", "/v1/oauth/authorize", "/v1/oauth/token", "/v1/oauth/revoke":
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
	return path == "/v1/oauth/authorize" || path == "/v1/oauth/clients" || path == "/v1/oauth/connections" || strings.HasPrefix(path, "/v1/oauth/connections/")
}

func isNoOrgControlPath(path string) bool {
	return path == "/v1/oauth/authorize" || path == "/v1/orgs"
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
	s.mux.HandleFunc("GET /v1/auth/me", s.controlMe)
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
			writeError(w, http.StatusBadRequest, err)
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
		writeError(w, http.StatusBadRequest, err)
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
