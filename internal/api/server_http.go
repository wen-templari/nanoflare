package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
)

func writeRuntimeError(w http.ResponseWriter, err error) {
	if errors.Is(err, nanoflare.ErrInvalidCapability) {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if errors.Is(err, nanoflare.ErrKVNamespaceNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, nanoflare.ErrObjectStorageBucketNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

func writeWorkerError(w http.ResponseWriter, err error) {
	if errors.Is(err, nanoflare.ErrAppNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, nanoflare.ErrKVNamespaceNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, nanoflare.ErrObjectStorageBucketNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, nanoflare.ErrKVNamespaceExists) || errors.Is(err, nanoflare.ErrKVNamespaceInUse) || errors.Is(err, nanoflare.ErrKVNamespaceNotBound) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if errors.Is(err, nanoflare.ErrObjectStorageBucketExists) || errors.Is(err, nanoflare.ErrObjectStorageBucketInUse) || errors.Is(err, nanoflare.ErrObjectStorageBucketNotBound) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func bearerToken(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
