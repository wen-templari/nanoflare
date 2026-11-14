package api

import (
	"context"
	"github.com/clas/nanoflare/internal/nanoflare"
	"net/http"
)

type cfEnvironmentKey struct{}
type cfEnvironmentTarget struct{ Service, Name string }

func (s *Server) cfOrdinaryTarget(r *http.Request, name string) (nanoflare.App, error) {
	if target, ok := r.Context().Value(cfEnvironmentKey{}).(cfEnvironmentTarget); ok {
		return s.service.OrdinaryServiceEnvironmentForOrg(controlOrgID(r), target.Service, target.Name)
	}
	return s.service.OrdinaryScriptForOrg(controlOrgID(r), name)
}
func (s *Server) cfEnsureOrdinaryTarget(r *http.Request, name string) (nanoflare.App, error) {
	if target, ok := r.Context().Value(cfEnvironmentKey{}).(cfEnvironmentTarget); ok {
		return s.service.EnsureOrdinaryServiceEnvironmentForOrg(controlOrgID(r), target.Service, target.Name, controlActor(r))
	}
	return s.service.EnsureOrdinaryScriptForOrg(controlOrgID(r), name, controlActor(r))
}
func (s *Server) cfServiceEnvironment(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 6 && parts[5] == "environments" && r.Method == "GET" {
		if !s.cfScopes(w, r, "workers:read") {
			return
		}
		service, err := s.service.OrdinaryScriptForOrg(controlOrgID(r), parts[4])
		if err != nil {
			cfServiceError(w, err)
			return
		}
		items, err := s.service.ServiceEnvironmentsForOrg(controlOrgID(r), service.ID)
		if err != nil {
			cfServiceError(w, err)
			return
		}
		cfJSON(w, items)
		return
	}
	if len(parts) < 7 || parts[5] != "environments" {
		cfError(w, 404, "unsupported service environment route")
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), cfEnvironmentKey{}, cfEnvironmentTarget{parts[4], parts[6]}))
	tail := []string{parts[4]}
	tail = append(tail, parts[7:]...)
	if r.Method == "GET" && ((len(tail) == 2 && tail[1] == "bindings") || (len(tail) == 3 && tail[1] == "content" && tail[2] == "v2")) {
		if !s.cfScopes(w, r, "workers:read") {
			return
		}
		app, err := s.cfOrdinaryTarget(r, parts[4])
		if err != nil {
			cfServiceError(w, err)
			return
		}
		snapshot, found, err := s.service.ManagementUploadForOrg(controlOrgID(r), app.ID, len(tail) == 3)
		if err != nil {
			cfServiceError(w, err)
			return
		}
		if !found {
			cfError(w, 404, "script has no uploaded revision")
			return
		}
		if len(tail) == 3 {
			s.cfScriptContent(w, snapshot)
		} else {
			cfJSON(w, s.cfSettings(snapshot)["bindings"])
		}
		return
	}
	if len(tail) == 1 && r.Method == "GET" {
		if !s.cfScopes(w, r, "workers:read") {
			return
		}
		app, err := s.cfOrdinaryTarget(r, parts[4])
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
		cfJSON(w, map[string]any{"environment": parts[6], "script": s.cfScriptResult(app, snapshot)})
		return
	}
	s.cfOrdinaryScript(w, r, tail)
}
