package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func (s *Server) registerKVRoutes() {
	s.mux.HandleFunc("GET /v1/kv/namespaces", s.listKVNamespaces)
	s.mux.HandleFunc("POST /v1/kv/namespaces", s.createKVNamespace)
	s.mux.HandleFunc("GET /v1/kv/namespaces/{namespaceID}", s.getKVNamespace)
	s.mux.HandleFunc("PATCH /v1/kv/namespaces/{namespaceID}", s.updateKVNamespace)
	s.mux.HandleFunc("DELETE /v1/kv/namespaces/{namespaceID}", s.deleteKVNamespace)
	s.mux.HandleFunc("GET /v1/kv/namespaces/{namespaceID}/metrics", s.kvNamespaceMetrics)
	s.mux.HandleFunc("GET /v1/workers/{workerID}/kv/namespaces/{namespaceID}", s.workerKVList)
	s.mux.HandleFunc("GET /v1/workers/{workerID}/kv/namespaces/{namespaceID}/{key...}", s.workerKVGet)
	s.mux.HandleFunc("PUT /v1/workers/{workerID}/kv/namespaces/{namespaceID}/{key...}", s.workerKVPut)
	s.mux.HandleFunc("DELETE /v1/workers/{workerID}/kv/namespaces/{namespaceID}/{key...}", s.workerKVDelete)
}

func (s *Server) workerKVList(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "kv:read") {
		return
	}
	keys, err := s.service.WorkerKVListForOrg(controlOrgID(r), r.PathValue("workerID"), r.PathValue("namespaceID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *Server) workerKVGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "kv:read") {
		return
	}
	if r.PathValue("key") == "" {
		s.workerKVList(w, r)
		return
	}
	key, err := consoleKVKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	value, ok, err := s.service.WorkerKVGetForOrg(controlOrgID(r), r.PathValue("workerID"), r.PathValue("namespaceID"), key)
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(value)
}

func (s *Server) workerKVPut(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "kv:write") {
		return
	}
	key, err := consoleKVKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	defer r.Body.Close()
	value, err := io.ReadAll(io.LimitReader(r.Body, maxKVValueSize+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(value) > maxKVValueSize {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("KV value exceeds 25 MiB limit"))
		return
	}
	if err := s.service.WorkerKVPutForOrg(controlOrgID(r), r.PathValue("workerID"), r.PathValue("namespaceID"), key, value); err != nil {
		writeWorkerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) workerKVDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "kv:write") {
		return
	}
	key, err := consoleKVKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.service.WorkerKVDeleteForOrg(controlOrgID(r), r.PathValue("workerID"), r.PathValue("namespaceID"), key); err != nil {
		writeWorkerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listKVNamespaces(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "kv:read") {
		return
	}
	namespaces, err := s.service.ListKVNamespacesForOrg(controlOrgID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, namespaces)
}

func (s *Server) createKVNamespace(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "kv:write") {
		return
	}
	var input nanoflare.CreateKVNamespaceInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input.OrgID = controlOrgID(r)
	if access, ok := controlOAuthAccess(r); ok {
		input.OAuthClientID = access.ClientID
	}
	namespace, err := s.service.CreateKVNamespace(input)
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, namespace)
}

func (s *Server) getKVNamespace(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "kv:read") {
		return
	}
	namespace, err := s.service.GetKVNamespaceForOrg(controlOrgID(r), r.PathValue("namespaceID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, namespace)
}

func (s *Server) updateKVNamespace(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "kv:write") {
		return
	}
	var input nanoflare.UpdateKVNamespaceInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	namespace, err := s.service.UpdateKVNamespaceForOrg(controlOrgID(r), r.PathValue("namespaceID"), input)
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, namespace)
}

func (s *Server) deleteKVNamespace(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "kv:write") {
		return
	}
	if err := s.service.DeleteKVNamespaceForOrg(controlOrgID(r), r.PathValue("namespaceID")); err != nil {
		writeWorkerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) kvNamespaceMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.requireScope(w, r, "kv:read") {
		return
	}
	metrics, err := s.service.KVNamespaceMetricsForOrg(controlOrgID(r), r.PathValue("namespaceID"))
	if err != nil {
		writeWorkerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}

func consoleKVKey(r *http.Request) (string, error) {
	key := r.PathValue("key")
	if key == "" {
		return "", errors.New("KV key cannot be empty")
	}
	if key == "." || key == ".." {
		return "", errors.New(`KV keys "." and ".." are not allowed`)
	}
	if len([]byte(key)) > maxKVKeySize {
		return "", errors.New("KV key exceeds 512 byte limit")
	}
	return key, nil
}
