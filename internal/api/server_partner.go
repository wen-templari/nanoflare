package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
)

type partnerConnectionResponse struct {
	ConnectionID string                 `json:"connection_id"`
	Organization nanoflare.Organization `json:"organization"`
	AccessToken  string                 `json:"access_token"`
	TokenType    string                 `json:"token_type"`
	ExpiresIn    int64                  `json:"expires_in"`
	RefreshToken string                 `json:"refresh_token"`
	Scope        string                 `json:"scope"`
}

type partnerRefreshRequest struct {
	ConnectionID string `json:"connection_id"`
	RefreshToken string `json:"refresh_token"`
}

func (s *Server) registerPartnerRoutes() {
	base := "/v1/organizations/{orgID}/partner-integrations"
	s.mux.HandleFunc("GET "+base, s.partnerIntegrations)
	s.mux.HandleFunc("POST "+base, s.createPartnerIntegration)
	s.mux.HandleFunc("POST "+base+"/{integrationID}/client-secrets", s.rotatePartnerIntegrationSecret)
	s.mux.HandleFunc("DELETE "+base+"/{integrationID}", s.disablePartnerIntegration)
	s.mux.HandleFunc("GET "+base+"/{integrationID}/connections", s.partnerConnections)
	s.mux.HandleFunc("POST "+base+"/{integrationID}/connections", s.provisionPartnerConnection)
	s.mux.HandleFunc("DELETE "+base+"/{integrationID}/connections/{connectionID}", s.revokePartnerConnection)
	s.mux.HandleFunc("POST "+base+"/{integrationID}/token", s.refreshPartnerConnection)
}

func (s *Server) rotatePartnerIntegrationSecret(w http.ResponseWriter, r *http.Request) {
	if !s.requireControlUser(w, r) || !s.requireScope(w, r, "orgs:write") {
		return
	}
	integration, err := s.partner.RotateIntegrationSecret(controlOrgID(r), r.PathValue("integrationID"))
	if err != nil {
		writePartnerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, integration)
}

func (s *Server) disablePartnerIntegration(w http.ResponseWriter, r *http.Request) {
	if !s.requireControlUser(w, r) || !s.requireScope(w, r, "orgs:write") {
		return
	}
	if err := s.partner.DisableIntegration(controlOrgID(r), r.PathValue("integrationID")); err != nil {
		writePartnerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) partnerIntegrations(w http.ResponseWriter, r *http.Request) {
	if !s.requireControlUser(w, r) || !s.requireScope(w, r, "orgs:read") {
		return
	}
	integrations, err := s.partner.Integrations(controlOrgID(r))
	if err != nil {
		writePartnerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, integrations)
}

func (s *Server) createPartnerIntegration(w http.ResponseWriter, r *http.Request) {
	if !s.requireControlUser(w, r) || !s.requireScope(w, r, "orgs:write") {
		return
	}
	var input nanoflare.CreatePartnerIntegrationInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input.OwnerOrgID = controlOrgID(r)
	integration, err := s.partner.CreateIntegration(input)
	if err != nil {
		writePartnerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, integration)
}

func (s *Server) partnerConnections(w http.ResponseWriter, r *http.Request) {
	if !s.requireControlUser(w, r) || !s.requireScope(w, r, "orgs:read") {
		return
	}
	if _, err := s.partner.Integration(controlOrgID(r), r.PathValue("integrationID")); err != nil {
		writePartnerError(w, err)
		return
	}
	connections, err := s.partnerConnectionList(r.PathValue("integrationID"))
	if err != nil {
		writePartnerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, connections)
}

func (s *Server) partnerConnectionList(integrationID string) ([]nanoflare.PartnerConnection, error) {
	return s.partner.Connections(integrationID)
}

func (s *Server) provisionPartnerConnection(w http.ResponseWriter, r *http.Request) {
	var input nanoflare.ProvisionPartnerConnectionInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	secret, err := partnerClientSecret(r)
	if err != nil {
		writePartnerError(w, err)
		return
	}
	if _, err := s.partner.Integration(r.PathValue("orgID"), r.PathValue("integrationID")); err != nil {
		writePartnerError(w, err)
		return
	}
	provisioned, err := s.partner.Provision(r.PathValue("integrationID"), secret, input)
	if err != nil {
		writePartnerError(w, err)
		return
	}
	token, err := s.oauth.IssuePartnerTokens(provisioned.Connection, input.RequestedScopes)
	if err != nil {
		writePartnerError(w, err)
		return
	}
	status := http.StatusOK
	if provisioned.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, partnerConnectionResponse{ConnectionID: provisioned.Connection.ID, Organization: provisioned.Organization, AccessToken: token.AccessToken, TokenType: token.TokenType, ExpiresIn: token.ExpiresIn, RefreshToken: token.RefreshToken, Scope: token.Scope})
}

func (s *Server) revokePartnerConnection(w http.ResponseWriter, r *http.Request) {
	secret, err := partnerClientSecret(r)
	if err != nil {
		writePartnerError(w, err)
		return
	}
	if _, err := s.partner.Integration(r.PathValue("orgID"), r.PathValue("integrationID")); err != nil {
		writePartnerError(w, err)
		return
	}
	if err := s.partner.Revoke(r.PathValue("integrationID"), secret, r.PathValue("connectionID")); err != nil {
		writePartnerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func partnerClientSecret(r *http.Request) (string, error) {
	clientID, secret, ok := r.BasicAuth()
	if !ok || strings.TrimSpace(secret) == "" || clientID != r.PathValue("integrationID") {
		return "", errors.New("partner client authentication is required")
	}
	return secret, nil
}

func (s *Server) refreshPartnerConnection(w http.ResponseWriter, r *http.Request) {
	secret, err := partnerClientSecret(r)
	if err != nil {
		writePartnerError(w, err)
		return
	}
	if _, err := s.partner.Integration(r.PathValue("orgID"), r.PathValue("integrationID")); err != nil {
		writePartnerError(w, err)
		return
	}
	if err := s.partner.AuthenticateIntegration(r.PathValue("integrationID"), secret); err != nil {
		writePartnerError(w, err)
		return
	}
	var input partnerRefreshRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	token, err := s.oauth.RefreshPartner(strings.TrimSpace(input.ConnectionID), strings.TrimSpace(input.RefreshToken))
	if err != nil {
		writePartnerError(w, err)
		return
	}
	connection, err := s.partner.Connection(input.ConnectionID)
	if err != nil {
		writePartnerError(w, err)
		return
	}
	organization, err := s.partner.Organization(connection.OrgID)
	if err != nil {
		writePartnerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, partnerConnectionResponse{ConnectionID: connection.ID, Organization: organization, AccessToken: token.AccessToken, TokenType: token.TokenType, ExpiresIn: token.ExpiresIn, RefreshToken: token.RefreshToken, Scope: token.Scope})
}

func writePartnerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, nanoflare.ErrPartnerIntegrationNotFound), errors.Is(err, nanoflare.ErrPartnerConnectionNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, nanoflare.ErrOAuthInvalidScope):
		writeError(w, http.StatusForbidden, err)
	case strings.Contains(err.Error(), "revoked"):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusBadRequest, err)
	}
}
