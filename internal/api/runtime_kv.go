package api

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
)

const (
	maxKVKeySize   = 512
	maxKVValueSize = 25 << 20
)

type RuntimeKVServer struct {
	service *nanoflare.Service
}

func NewRuntimeKVServer(service *nanoflare.Service) *RuntimeKVServer {
	return &RuntimeKVServer{service: service}
}

func (s *RuntimeKVServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || strings.HasPrefix(strings.TrimPrefix(r.URL.Path, "/"), "bulk/") {
		writeError(w, http.StatusNotImplemented, errors.New("KV list, bulk, metadata, and expiration options are not supported"))
		return
	}
	key, err := runtimeKVKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if unsupportedKVOptions(r) {
		writeError(w, http.StatusNotImplemented, errors.New("KV list, bulk, metadata, and expiration options are not supported"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.get(w, r, key)
	case http.MethodPut:
		s.put(w, r, key)
	case http.MethodDelete:
		s.delete(w, r, key)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeError(w, http.StatusMethodNotAllowed, errors.New("unsupported KV operation"))
	}
}

func (s *RuntimeKVServer) get(w http.ResponseWriter, r *http.Request, key string) {
	namespaceID, err := runtimeKVNamespaceID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	value, ok, err := s.service.KVGet(bearerToken(r), namespaceID, key)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	_ = s.service.RecordRuntimeKVRead(namespaceID)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(value)
}

func (s *RuntimeKVServer) put(w http.ResponseWriter, r *http.Request, key string) {
	namespaceID, err := runtimeKVNamespaceID(r)
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
	if err := s.service.KVPut(bearerToken(r), namespaceID, key, value); err != nil {
		writeRuntimeError(w, err)
		return
	}
	_ = s.service.RecordRuntimeKVWrite(namespaceID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *RuntimeKVServer) delete(w http.ResponseWriter, r *http.Request, key string) {
	namespaceID, err := runtimeKVNamespaceID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.service.KVDelete(bearerToken(r), namespaceID, key); err != nil {
		writeRuntimeError(w, err)
		return
	}
	_ = s.service.RecordRuntimeKVWrite(namespaceID)
	w.WriteHeader(http.StatusNoContent)
}

func runtimeKVNamespaceID(r *http.Request) (string, error) {
	namespaceID := strings.TrimSpace(r.Header.Get("X-Nanoflare-KV-Namespace-ID"))
	if namespaceID == "" {
		return "", errors.New("kv namespace id header is required")
	}
	return namespaceID, nil
}

func runtimeKVKey(r *http.Request) (string, error) {
	escaped := strings.TrimPrefix(r.URL.EscapedPath(), "/")
	if escaped == "" {
		return "", errors.New("KV list is not supported")
	}
	key, err := url.PathUnescape(escaped)
	if err != nil {
		return "", errors.New("invalid KV key encoding")
	}
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

func unsupportedKVOptions(r *http.Request) bool {
	query := r.URL.Query()
	for name := range query {
		if name != "urlencoded" {
			return true
		}
	}
	return r.Header.Get("CF-KV-Metadata") != ""
}
