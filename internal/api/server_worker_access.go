package api

import (
	"errors"
	"net/http"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func (s *Server) issueWorkerAccessToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.controlAuth == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("control authentication is required"))
		return
	}
	if !s.requireScope(w, r, "workers:invoke") || !requirePathOrg(w, r) {
		return
	}
	var input nanoflare.WorkerAccessRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := nanoflare.ValidateWorkerAccessRequest(input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	app, err := s.service.AppForOrg(controlOrgID(r), r.PathValue("workerID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	result, err := s.controlAuth.IssueWorkerAccessToken(app, controlActor(r), input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) workerAccess(r *http.Request, host, method, uri string) (nanoflare.WorkerAccessClaims, error) {
	invalid := errors.New("invalid or expired worker access token")
	if s.controlAuth == nil || len(r.Header.Values(nanoflare.WorkerAccessHeader)) != 1 {
		return nanoflare.WorkerAccessClaims{}, invalid
	}
	claims, err := s.controlAuth.ValidateWorkerAccessToken(r.Header.Get(nanoflare.WorkerAccessHeader))
	if err != nil {
		return claims, err
	}
	if claims.Hostname != host || claims.Method != method || claims.Path != uri {
		return nanoflare.WorkerAccessClaims{}, invalid
	}
	app, err := s.service.AppForOrg(claims.OrgID, claims.WorkerID)
	if err != nil || app.Hostname != host || app.IsPrivate() {
		return nanoflare.WorkerAccessClaims{}, invalid
	}
	return claims, nil
}
