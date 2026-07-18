package api

import (
	"errors"
	"html"
	"net/http"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type oauthTokenRequest struct {
	GrantType    string `json:"grant_type"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Code         string `json:"code,omitempty"`
	RedirectURI  string `json:"redirect_uri,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

type oauthRevokeRequest struct {
	Token string `json:"token"`
}

func (s *Server) registerOAuthRoutes() {
	s.mux.HandleFunc("GET /v1/oauth/clients", s.oauthClients)
	s.mux.HandleFunc("POST /v1/oauth/clients", s.createOAuthClient)
	s.mux.HandleFunc("GET /v1/oauth/clients/{clientID}", s.oauthClient)
	s.mux.HandleFunc("GET /v1/oauth/clients/{clientID}/connections", s.oauthClientConnections)
	s.mux.HandleFunc("PATCH /v1/oauth/clients/{clientID}", s.updateOAuthClient)
	s.mux.HandleFunc("POST /v1/oauth/clients/{clientID}/secret", s.rotateOAuthClientSecret)
	s.mux.HandleFunc("POST /v1/oauth/clients/{clientID}/restore", s.restoreOAuthClient)
	s.mux.HandleFunc("DELETE /v1/oauth/clients/{clientID}", s.disableOAuthClient)
	s.mux.HandleFunc("GET /v1/oauth/authorize", s.oauthAuthorizeInfo)
	s.mux.HandleFunc("POST /v1/oauth/authorize", s.oauthAuthorize)
	s.mux.HandleFunc("POST /v1/oauth/token", s.oauthToken)
	s.mux.HandleFunc("POST /v1/oauth/revoke", s.oauthRevoke)
	s.mux.HandleFunc("GET /v1/oauth/connections", s.oauthConnections)
	s.mux.HandleFunc("DELETE /v1/oauth/connections/{clientID}", s.oauthDisconnect)
}

func (s *Server) oauthClients(w http.ResponseWriter, r *http.Request) {
	if !s.requireControlUser(w, r) || !s.requireScope(w, r, "orgs:read") {
		return
	}
	clients, err := s.oauth.Clients(controlOrgID(r))
	if err != nil {
		writeOAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, clients)
}

func (s *Server) createOAuthClient(w http.ResponseWriter, r *http.Request) {
	if !s.requireControlUser(w, r) || !s.requireScope(w, r, "orgs:write") {
		return
	}
	var input nanoflare.CreateOAuthClientInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input.OwnerOrgID = controlOrgID(r)
	client, err := s.oauth.CreateClient(input)
	if err != nil {
		writeOAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, client)
}

func (s *Server) oauthClient(w http.ResponseWriter, r *http.Request) {
	if !s.requireControlUser(w, r) || !s.requireScope(w, r, "orgs:read") {
		return
	}
	client, err := s.oauth.Client(controlOrgID(r), r.PathValue("clientID"))
	if err != nil {
		writeOAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, client)
}

func (s *Server) oauthClientConnections(w http.ResponseWriter, r *http.Request) {
	if !s.requireControlUser(w, r) || !s.requireScope(w, r, "orgs:read") {
		return
	}
	connections, err := s.oauth.ClientConnections(controlOrgID(r), r.PathValue("clientID"))
	if err != nil {
		writeOAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, connections)
}

func (s *Server) updateOAuthClient(w http.ResponseWriter, r *http.Request) {
	if !s.requireControlUser(w, r) || !s.requireScope(w, r, "orgs:write") {
		return
	}
	var input nanoflare.UpdateOAuthClientInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	client, err := s.oauth.UpdateClient(controlOrgID(r), r.PathValue("clientID"), input)
	if err != nil {
		writeOAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, client)
}

func (s *Server) rotateOAuthClientSecret(w http.ResponseWriter, r *http.Request) {
	if !s.requireControlUser(w, r) || !s.requireScope(w, r, "orgs:write") {
		return
	}
	client, err := s.oauth.RotateClientSecret(controlOrgID(r), r.PathValue("clientID"))
	if err != nil {
		writeOAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, client)
}

func (s *Server) restoreOAuthClient(w http.ResponseWriter, r *http.Request) {
	if !s.requireControlUser(w, r) || !s.requireScope(w, r, "orgs:write") {
		return
	}
	client, err := s.oauth.RestoreClient(controlOrgID(r), r.PathValue("clientID"))
	if err != nil {
		writeOAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, client)
}

func (s *Server) disableOAuthClient(w http.ResponseWriter, r *http.Request) {
	if !s.requireControlUser(w, r) || !s.requireScope(w, r, "orgs:write") {
		return
	}
	if err := s.oauth.DisableClient(controlOrgID(r), r.PathValue("clientID")); err != nil {
		writeOAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) oauthAuthorizeInfo(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("client_id") != "" {
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			query := r.URL.Query()
			info, err := s.oauth.AuthorizeInfo(
				query.Get("client_id"),
				query.Get("redirect_uri"),
				strings.Fields(query.Get("scope")),
			)
			if err != nil {
				writeOAuthError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, info)
			return
		}
		s.oauthAuthorizePage(w, r, "")
		return
	}
	user, ok := s.oauthBearerUser(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": user,
	})
}

func (s *Server) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		s.oauthAuthorizeForm(w, r)
		return
	}
	var input nanoflare.OAuthAuthorizeInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	user, ok := s.oauthBearerUser(w, r)
	if !ok {
		return
	}
	response, err := s.oauth.Authorize(user, input)
	if err != nil {
		writeOAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) oauthBearerUser(w http.ResponseWriter, r *http.Request) (nanoflare.User, bool) {
	user := controlUser(r)
	if user.ID != "" {
		return user, true
	}
	token := strings.TrimSpace(bearerToken(r))
	if token == "" {
		writeError(w, http.StatusUnauthorized, errors.New("bearer token is required"))
		return nanoflare.User{}, false
	}
	user, err := s.controlAuth.ValidateToken(token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return nanoflare.User{}, false
	}
	return user, true
}

func (s *Server) requireControlUser(w http.ResponseWriter, r *http.Request) bool {
	user := controlUser(r)
	if user.ID == "" {
		writeError(w, http.StatusUnauthorized, errors.New("signed-in Nanoflare user is required"))
		return false
	}
	return true
}

func (s *Server) oauthAuthorizePage(w http.ResponseWriter, r *http.Request, message string) {
	query := r.URL.Query()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Connect Nanoflare</title>
<style>
:root{color-scheme:dark;--bg:#11130f;--panel:#f2ead8;--ink:#19160f;--muted:#6d6659;--line:#3e382d;--accent:#d7ff64;--red:#ff6b5f}
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;background:radial-gradient(circle at 20% 10%,#2f3d23 0 20%,transparent 32%),linear-gradient(135deg,#11130f,#201b14);font-family:Georgia,"Times New Roman",serif;color:var(--panel);padding:24px}
main{width:min(680px,100%);background:var(--panel);color:var(--ink);border:1px solid #fff3;box-shadow:0 24px 80px #0009;padding:34px}
.mark{font:700 12px ui-monospace,monospace;letter-spacing:.16em;text-transform:uppercase;color:var(--muted)}
h1{font-size:clamp(34px,7vw,66px);line-height:.88;margin:14px 0 18px;letter-spacing:0}
p{color:var(--muted);font-size:17px;line-height:1.5}.scopes{display:flex;flex-wrap:wrap;gap:8px;margin:22px 0}.scope{border:1px solid var(--line);padding:7px 10px;font:700 12px ui-monospace,monospace}
label{display:block;font:700 12px ui-monospace,monospace;text-transform:uppercase;margin:16px 0 6px;color:var(--line)}
input{width:100%;height:44px;border:1px solid var(--line);background:#fff9;color:var(--ink);padding:0 12px;font:16px ui-monospace,monospace}
button{margin-top:22px;height:48px;border:0;background:var(--ink);color:var(--accent);font:800 13px ui-monospace,monospace;text-transform:uppercase;padding:0 18px;cursor:pointer}
.error{color:var(--red);font:700 13px ui-monospace,monospace;margin-top:12px}
</style>
</head>
<body><main>
<div class="mark">Nanoflare authorization</div>
<h1>Connect this external app.</h1>
<p>The app is requesting access to the selected Nanoflare organization. Sign in to approve and return to the external app.</p>
<div class="scopes">`))
	for _, scope := range strings.Fields(query.Get("scope")) {
		_, _ = w.Write([]byte(`<span class="scope">` + html.EscapeString(scope) + `</span>`))
	}
	if message != "" {
		_, _ = w.Write([]byte(`<div class="error">` + html.EscapeString(message) + `</div>`))
	}
	_, _ = w.Write([]byte(`<form method="post" action="/v1/oauth/authorize">
<input type="hidden" name="client_id" value="` + html.EscapeString(query.Get("client_id")) + `">
<input type="hidden" name="redirect_uri" value="` + html.EscapeString(query.Get("redirect_uri")) + `">
<input type="hidden" name="scope" value="` + html.EscapeString(query.Get("scope")) + `">
<input type="hidden" name="state" value="` + html.EscapeString(query.Get("state")) + `">
<label>Email</label><input name="email" type="email" autocomplete="username" required>
<label>Password</label><input name="password" type="password" autocomplete="current-password" required>
<label>Organization ID</label><input name="org_id" placeholder="Leave blank to use your first organization">
<button type="submit">Approve connection</button>
</form></main></body></html>`))
}

func (s *Server) oauthAuthorizeForm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session, err := s.controlAuth.Login(nanoflare.LoginInput{Email: r.FormValue("email"), Password: r.FormValue("password")})
	if err != nil {
		s.oauthAuthorizePage(w, r, "Invalid Nanoflare credentials")
		return
	}
	orgID := strings.TrimSpace(r.FormValue("org_id"))
	if orgID == "" {
		orgID = session.ActiveOrgID
	}
	response, err := s.oauth.Authorize(session.User, nanoflare.OAuthAuthorizeInput{
		ClientID:    r.FormValue("client_id"),
		RedirectURI: r.FormValue("redirect_uri"),
		Scopes:      strings.Fields(r.FormValue("scope")),
		State:       r.FormValue("state"),
		OrgID:       orgID,
	})
	if err != nil {
		s.oauthAuthorizePage(w, r, err.Error())
		return
	}
	http.Redirect(w, r, response.RedirectTo, http.StatusFound)
}

func (s *Server) oauthToken(w http.ResponseWriter, r *http.Request) {
	var input oauthTokenRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	switch strings.TrimSpace(input.GrantType) {
	case "authorization_code":
		response, err := s.oauth.ExchangeAuthorizationCode(input.ClientID, input.ClientSecret, input.Code, input.RedirectURI)
		if err != nil {
			writeOAuthError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	case "refresh_token":
		response, err := s.oauth.Refresh(input.ClientID, input.ClientSecret, input.RefreshToken)
		if err != nil {
			writeOAuthError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	default:
		writeError(w, http.StatusBadRequest, errors.New("unsupported grant_type"))
	}
}

func (s *Server) oauthRevoke(w http.ResponseWriter, r *http.Request) {
	var input oauthRevokeRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(input.Token) == "" {
		writeError(w, http.StatusBadRequest, errors.New("token is required"))
		return
	}
	if err := s.oauth.Revoke(input.Token); err != nil {
		writeOAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) oauthConnections(w http.ResponseWriter, r *http.Request) {
	user := controlUser(r)
	if user.ID == "" {
		writeError(w, http.StatusUnauthorized, errors.New("signed-in Nanoflare user is required"))
		return
	}
	connections, err := s.oauth.Connections(user.ID, controlOrgID(r))
	if err != nil {
		writeOAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, connections)
}

func (s *Server) oauthDisconnect(w http.ResponseWriter, r *http.Request) {
	user := controlUser(r)
	if user.ID == "" {
		writeError(w, http.StatusUnauthorized, errors.New("signed-in Nanoflare user is required"))
		return
	}
	if err := s.oauth.Disconnect(user.ID, controlOrgID(r), r.PathValue("clientID")); err != nil {
		writeOAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeOAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, nanoflare.ErrUsageLimitExceeded):
		writeError(w, http.StatusPaymentRequired, err)
	case errors.Is(err, nanoflare.ErrOAuthClientNotFound), errors.Is(err, nanoflare.ErrOAuthInvalidGrant), errors.Is(err, nanoflare.ErrOAuthTokenNotFound):
		writeError(w, http.StatusUnauthorized, err)
	case errors.Is(err, nanoflare.ErrOAuthInvalidScope), errors.Is(err, nanoflare.ErrMembershipNotFound):
		writeError(w, http.StatusForbidden, err)
	default:
		writeError(w, http.StatusBadRequest, err)
	}
}
