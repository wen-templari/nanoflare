package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
)

const maxJSONBodySize = 10 << 20

func writeRuntimeError(w http.ResponseWriter, err error) {
	if errors.Is(err, nanoflare.ErrUsageLimitExceeded) {
		writeError(w, http.StatusPaymentRequired, err)
		return
	}
	if errors.Is(err, nanoflare.ErrInvalidCapability) {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if errors.Is(err, nanoflare.ErrKVNamespaceNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, nanoflare.ErrDatabaseNotFound) {
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
	if errors.Is(err, nanoflare.ErrUsageLimitExceeded) {
		writeError(w, http.StatusPaymentRequired, err)
		return
	}
	if errors.Is(err, nanoflare.ErrAppNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, nanoflare.ErrKVNamespaceNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, nanoflare.ErrDatabaseNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, nanoflare.ErrObjectStorageBucketNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, nanoflare.ErrSecretNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, nanoflare.ErrKVNamespaceExists) || errors.Is(err, nanoflare.ErrKVNamespaceInUse) || errors.Is(err, nanoflare.ErrKVNamespaceNotBound) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if errors.Is(err, nanoflare.ErrDatabaseExists) || errors.Is(err, nanoflare.ErrDatabaseInUse) || errors.Is(err, nanoflare.ErrDatabaseNotBound) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if errors.Is(err, nanoflare.ErrObjectStorageBucketExists) || errors.Is(err, nanoflare.ErrObjectStorageBucketInUse) || errors.Is(err, nanoflare.ErrObjectStorageBucketNotBound) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.Contains(err.Error(), "binding") || strings.Contains(err.Error(), "NANOFLARE_SECRET_KEY") || strings.Contains(err.Error(), "secret name is required") {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxJSONBodySize))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func bearerToken(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) < len("Bearer ") || !strings.EqualFold(value[:len("Bearer ")], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(value[len("Bearer "):])
}

func writeError(w http.ResponseWriter, status int, err error) {
	requestID := w.Header().Get("Request-Id")
	if requestID == "" {
		requestID = newRequestID()
		w.Header().Set("Request-Id", requestID)
	}
	writeJSON(w, status, map[string]any{
		"type":     "about:blank",
		"title":    http.StatusText(status),
		"status":   status,
		"detail":   err.Error(),
		"instance": requestID,
	})
}

func newRequestID() string {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "request-id-unavailable"
	}
	return hex.EncodeToString(value)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
