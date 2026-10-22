package api

import (
	"errors"
	"net/http"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func (s *Server) registerArtifactRoutes() {
	base := "/v1/organizations/{orgID}/workers/{workerID}"
	s.mux.HandleFunc("GET "+base+"/capabilities", s.workerCapabilities)
	s.mux.HandleFunc("GET "+base+"/publications", s.workerPublications)
	s.mux.HandleFunc("GET "+base+"/deployments/{deploymentID}/artifact", s.workerArtifact)
	s.mux.HandleFunc("GET "+base+"/publications/{key}", s.workerPublication)
	s.mux.HandleFunc("GET /v1/organizations/{orgID}/dispatch-namespaces/{namespaceID}/scripts/{scriptName}/artifact", s.namespaceArtifact)
}
func writeArtifactError(w http.ResponseWriter, err error) {
	if errors.Is(err, nanoflare.ErrArtifactNotFound) {
		writeError(w, 404, err)
		return
	}
	writePlatformError(w, err)
}
func (s *Server) workerArtifact(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "workers:read") {
		return
	}
	out, err := s.service.ArtifactForOrg(controlOrgID(r), r.PathValue("workerID"), r.PathValue("deploymentID"))
	if err != nil {
		writeArtifactError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) workerPublication(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "workers:read") {
		return
	}
	out, err := s.service.PublicationForOrg(controlOrgID(r), r.PathValue("workerID"), r.PathValue("key"))
	if err != nil {
		writeArtifactError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) namespaceArtifact(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "namespaces:dispatch") {
		return
	}
	out, err := s.service.ResolveNamespaceArtifactForOrg(controlOrgID(r), r.PathValue("namespaceID"), r.PathValue("scriptName"), "")
	if err != nil {
		writeArtifactError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) workerCapabilities(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "workers:read") {
		return
	}
	result, err := s.service.RuntimeCapabilitiesForOrg(controlOrgID(r), r.PathValue("workerID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) workerPublications(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "workers:read") {
		return
	}
	result, err := s.service.PublicationsForOrg(controlOrgID(r), r.PathValue("workerID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
