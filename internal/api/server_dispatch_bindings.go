package api

import (
	"errors"
	"net/http"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func (s *Server) updateDispatchBindings(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "deployments:write") || !s.requireScope(w, r, "namespaces:dispatch") {
		return
	}
	var input nanoflare.UpdateDispatchBindingsInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input.CreatedBy = controlActor(r)
	result, err := s.service.UpdateDispatchBindingsForOrg(controlOrgID(r), r.PathValue("workerID"), input)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, nanoflare.ErrAppNotFound):
			status = http.StatusNotFound
		case errors.Is(err, nanoflare.ErrInvalidDispatchBindingsUpdate):
			status = http.StatusBadRequest
		case errors.Is(err, nanoflare.ErrDeploymentConflict), errors.Is(err, nanoflare.ErrIdempotencyConflict):
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
