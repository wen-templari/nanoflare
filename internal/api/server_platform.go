package api

import (
	"errors"
	"github.com/clas/nanoflare/internal/nanoflare"
	"net/http"
)

func (s *Server) registerPlatformRoutes() {
	base := "/v1/organizations/{orgID}/dispatch-namespaces"
	s.mux.HandleFunc("GET "+base, s.listDispatchNamespaces)
	s.mux.HandleFunc("POST "+base, s.createDispatchNamespace)
	s.mux.HandleFunc("GET "+base+"/{namespaceID}", s.getDispatchNamespace)
	s.mux.HandleFunc("PATCH "+base+"/{namespaceID}", s.renameDispatchNamespace)
	s.mux.HandleFunc("DELETE "+base+"/{namespaceID}", s.deleteDispatchNamespace)
	s.mux.HandleFunc("PUT "+base+"/{namespaceID}/grants", s.setNamespaceGrants)
	s.mux.HandleFunc("GET "+base+"/{namespaceID}/policies", s.namespacePolicies)
	s.mux.HandleFunc("GET "+base+"/{namespaceID}/scripts", s.namespaceScripts)
	s.mux.HandleFunc("POST "+base+"/{namespaceID}/scripts", s.createNamespaceScript)
	s.mux.HandleFunc("GET "+base+"/{namespaceID}/scripts/{scriptName}", s.getNamespaceScript)
	s.mux.HandleFunc("DELETE "+base+"/{namespaceID}/scripts/{scriptName}", s.deleteNamespaceScript)
}
func writePlatformError(w http.ResponseWriter, err error) {
	status := 400
	switch {
	case errors.Is(err, nanoflare.ErrUsageLimitExceeded):
		status = 402
	case errors.Is(err, nanoflare.ErrDispatchNamespaceNotFound), errors.Is(err, nanoflare.ErrAppNotFound), errors.Is(err, nanoflare.ErrKVNamespaceNotFound), errors.Is(err, nanoflare.ErrDatabaseNotFound), errors.Is(err, nanoflare.ErrObjectStorageBucketNotFound):
		status = 404
	case errors.Is(err, nanoflare.ErrNamespaceGrantDenied):
		status = 403
	case errors.Is(err, nanoflare.ErrDispatchNamespaceExists), errors.Is(err, nanoflare.ErrDispatchNamespaceInUse), errors.Is(err, nanoflare.ErrNamespacePolicyConflict):
		status = 409
	case errors.Is(err, nanoflare.ErrUnsupportedExecutionProfile), errors.Is(err, nanoflare.ErrDispatchUnavailable):
		status = 422
	}
	writeError(w, status, err)
}
func (s *Server) listDispatchNamespaces(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "namespaces:read") {
		return
	}
	out, err := s.service.DispatchNamespacesForOrg(controlOrgID(r))
	if err != nil {
		writePlatformError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) createDispatchNamespace(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "namespaces:write") {
		return
	}
	var in nanoflare.CreateDispatchNamespaceInput
	if err := decodeJSON(r, &in); err != nil {
		writePlatformError(w, err)
		return
	}
	out, err := s.service.CreateDispatchNamespace(controlOrgID(r), in)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	writeJSON(w, 201, out)
}
func (s *Server) getDispatchNamespace(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "namespaces:read") {
		return
	}
	out, err := s.service.DispatchNamespaceForOrg(controlOrgID(r), r.PathValue("namespaceID"))
	if err != nil {
		writePlatformError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) renameDispatchNamespace(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "namespaces:write") {
		return
	}
	var in nanoflare.RenameDispatchNamespaceInput
	if err := decodeJSON(r, &in); err != nil {
		writePlatformError(w, err)
		return
	}
	out, err := s.service.RenameDispatchNamespace(controlOrgID(r), r.PathValue("namespaceID"), in.Name)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) deleteDispatchNamespace(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "namespaces:write") {
		return
	}
	if err := s.service.DeleteDispatchNamespace(controlOrgID(r), r.PathValue("namespaceID")); err != nil {
		writePlatformError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) setNamespaceGrants(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "namespaces:dispatch") {
		return
	}
	var in nanoflare.NamespaceGrantsInput
	if err := decodeJSON(r, &in); err != nil {
		writePlatformError(w, err)
		return
	}
	out, err := s.service.SetNamespaceGrants(controlOrgID(r), r.PathValue("namespaceID"), in)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) namespacePolicies(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "namespaces:read") {
		return
	}
	out, err := s.service.NamespacePoliciesForOrg(controlOrgID(r), r.PathValue("namespaceID"))
	if err != nil {
		writePlatformError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) namespaceScripts(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "workers:read") {
		return
	}
	out, err := s.service.NamespaceScripts(controlOrgID(r), r.PathValue("namespaceID"))
	if err != nil {
		writePlatformError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) createNamespaceScript(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "workers:write") {
		return
	}
	var in nanoflare.CreateNamespaceScriptInput
	if err := decodeJSON(r, &in); err != nil {
		writePlatformError(w, err)
		return
	}
	if in.Kind == "platform" && !s.requireScope(w, r, "namespaces:dispatch") {
		return
	}
	out, err := s.service.CreateNamespaceScript(controlOrgID(r), r.PathValue("namespaceID"), in)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	writeJSON(w, 201, out)
}
func (s *Server) getNamespaceScript(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "workers:read") {
		return
	}
	out, err := s.service.NamespaceScript(controlOrgID(r), r.PathValue("namespaceID"), r.PathValue("scriptName"))
	if err != nil {
		writePlatformError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) deleteNamespaceScript(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "workers:write") {
		return
	}
	out, err := s.service.NamespaceScript(controlOrgID(r), r.PathValue("namespaceID"), r.PathValue("scriptName"))
	if err == nil {
		err = s.service.DeleteAppForOrg(controlOrgID(r), out.ID)
	}
	if err != nil {
		writePlatformError(w, err)
		return
	}
	w.WriteHeader(204)
}
