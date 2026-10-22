package api

import (
	"net/http"

	"github.com/clas/nanoflare/internal/nanoflare"
)

// Ordinary uploads use account-scoped script identity rather than namespace
// membership. The shared upload path preserves native publication/retry rules.
func (s *Server) cfOrdinaryScript(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(r.Header.Values("X-Nanoflare-Customer-ID")) > 0 {
		cfError(w, 422, "ordinary Workers do not accept customer identity headers")
		return
	}
	if len(parts) == 1 && r.Method == "PUT" {
		s.cfUploadWorker(w, r, nanoflare.DispatchNamespace{}, parts[0], true)
		return
	}
	if len(parts) != 2 || parts[1] != "settings" || r.Method != "GET" {
		cfError(w, 404, "unsupported ordinary Worker compatibility route")
		return
	}
	if !s.cfScopes(w, r, "workers:read") {
		return
	}
	app, err := s.service.OrdinaryScriptForOrg(controlOrgID(r), parts[0])
	if err != nil {
		cfServiceError(w, err)
		return
	}
	snapshot, found, err := s.service.ManagementUploadForOrg(controlOrgID(r), app.ID, false)
	if err != nil {
		cfServiceError(w, err)
		return
	}
	if !found {
		cfError(w, 404, "script has no uploaded revision")
		return
	}
	w.Header().Set("ETag", `"`+snapshot.DeploymentID+`"`)
	cfJSON(w, s.cfSettings(snapshot))
}
