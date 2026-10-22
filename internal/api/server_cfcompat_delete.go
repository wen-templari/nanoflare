package api

import (
	"net/http"

	"github.com/clas/nanoflare/internal/cfcompat"
	"github.com/clas/nanoflare/internal/nanoflare"
)

func (s *Server) cfDeleteScriptsByTag(w http.ResponseWriter, r *http.Request, ns nanoflare.DispatchNamespace) {
	if !s.cfScopes(w, r, "workers:write") {
		return
	}
	if len(r.Header.Values("Idempotency-Key")) > 0 || len(r.Header.Values("If-Match")) > 0 || len(r.Header.Values("X-Nanoflare-Customer-ID")) > 0 {
		cfError(w, 422, "bulk deletion requires an explicit tag filter; per-script identity/precondition headers are unsupported")
		return
	}
	value, exists := r.URL.Query()["tags"]
	if !exists {
		cfError(w, 422, "bulk deletion requires an explicit tag filter")
		return
	}
	name, present, err := cfcompat.ParseTagFilter(value[0])
	if err != nil {
		cfError(w, 422, err.Error())
		return
	}
	result, err := s.service.DeleteNamespaceScriptsByTag(controlOrgID(r), ns.ID, name, present)
	if err != nil {
		if result.State == "not_started" {
			cfServiceError(w, err)
			return
		}
		writeJSON(w, 503, cfEnvelope{Success: false, Errors: []cfAPIError{{Code: 10000, Message: "deletion needs reconciliation; inspect the reported state and retry the current tag selection"}}, Messages: []cfAPIError{}, Result: result})
		return
	}
	cfJSON(w, result)
}
