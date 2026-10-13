package api

import (
	"errors"
	"net/http"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func (s *Server) updateVars(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "deployments:write") {
		return
	}
	var input nanoflare.UpdateVarsInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input.CreatedBy = controlActor(r)
	result, err := s.service.UpdateVarsForOrg(controlOrgID(r), r.PathValue("workerID"), input)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, nanoflare.ErrAppNotFound):
			status = http.StatusNotFound
		case errors.Is(err, nanoflare.ErrInvalidVarsUpdate):
			status = http.StatusBadRequest
		case errors.Is(err, nanoflare.ErrDeploymentConflict), errors.Is(err, nanoflare.ErrIdempotencyConflict):
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
