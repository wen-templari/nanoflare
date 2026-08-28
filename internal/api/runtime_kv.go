package api

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
)

const (
	maxKVKeySize       = 512
	maxKVValueSize     = 25 << 20
	maxKVListKeyCount  = 1000
	minKVExpirationTTL = 60
)

var errUnsupportedRuntimeKVOptions = errors.New("KV bulk, metadata, and cache options are not supported")

type RuntimeKVServer struct {
	service *nanoflare.Service
}

func NewRuntimeKVServer(service *nanoflare.Service) *RuntimeKVServer {
	return &RuntimeKVServer{service: service}
}

func (s *RuntimeKVServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(strings.TrimPrefix(r.URL.Path, "/"), "bulk/") {
		writeError(w, http.StatusNotImplemented, errUnsupportedRuntimeKVOptions)
		return
	}
	if r.URL.Path == "/" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, errors.New("unsupported KV operation"))
			return
		}
		s.list(w, r)
		return
	}
	key, err := runtimeKVKey(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if err := validateRuntimeKVQuery(r.URL.Query(), "urlencoded"); err != nil {
			writeError(w, http.StatusNotImplemented, err)
			return
		}
		s.get(w, r, key)
	case http.MethodPut:
		options, err := runtimeKVPutOptions(r)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errUnsupportedRuntimeKVOptions) {
				status = http.StatusNotImplemented
			}
			writeError(w, status, err)
			return
		}
		s.put(w, r, key, options)
	case http.MethodDelete:
		if err := validateRuntimeKVQuery(r.URL.Query(), "urlencoded"); err != nil {
			writeError(w, http.StatusNotImplemented, err)
			return
		}
		s.delete(w, r, key)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeError(w, http.StatusMethodNotAllowed, errors.New("unsupported KV operation"))
	}
}

func (s *RuntimeKVServer) list(w http.ResponseWriter, r *http.Request) {
	options, err := runtimeKVListOptions(r)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errUnsupportedRuntimeKVOptions) {
			status = http.StatusNotImplemented
		}
		writeError(w, status, err)
		return
	}
	namespaceID, err := runtimeKVNamespaceID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.service.KVListPage(bearerToken(r), namespaceID, options)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	_ = s.service.RecordRuntimeKVRead(namespaceID)
	writeJSON(w, http.StatusOK, result)
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

func (s *RuntimeKVServer) put(w http.ResponseWriter, r *http.Request, key string, options nanoflare.RuntimeKVPutOptions) {
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
	if err := s.service.KVPutWithOptions(bearerToken(r), namespaceID, key, value, options); err != nil {
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

func runtimeKVPutOptions(r *http.Request) (nanoflare.RuntimeKVPutOptions, error) {
	if strings.TrimSpace(r.Header.Get("CF-KV-Metadata")) != "" {
		return nanoflare.RuntimeKVPutOptions{}, errUnsupportedRuntimeKVOptions
	}
	query := r.URL.Query()
	if err := validateRuntimeKVQuery(query, "urlencoded", "expiration", "expiration_ttl"); err != nil {
		return nanoflare.RuntimeKVPutOptions{}, err
	}
	hasExpiration := query.Has("expiration")
	hasTTL := query.Has("expiration_ttl")
	if hasExpiration && hasTTL {
		return nanoflare.RuntimeKVPutOptions{}, errors.New("expiration and expiration_ttl are mutually exclusive")
	}
	if !hasExpiration && !hasTTL {
		return nanoflare.RuntimeKVPutOptions{}, nil
	}
	now := time.Now().UTC()
	if hasTTL {
		ttl, err := strconv.ParseInt(query.Get("expiration_ttl"), 10, 64)
		if err != nil || ttl < minKVExpirationTTL || ttl > 253402300799-now.Unix() {
			return nanoflare.RuntimeKVPutOptions{}, errors.New("expiration_ttl must be an integer of at least 60 seconds")
		}
		expiration := time.Unix(now.Unix()+ttl, 0).UTC()
		return nanoflare.RuntimeKVPutOptions{Expiration: &expiration}, nil
	}
	unix, err := strconv.ParseInt(query.Get("expiration"), 10, 64)
	if err != nil || unix < now.Unix()+minKVExpirationTTL || unix > 253402300799 {
		return nanoflare.RuntimeKVPutOptions{}, errors.New("expiration must be an integer at least 60 seconds in the future")
	}
	expiration := time.Unix(unix, 0).UTC()
	return nanoflare.RuntimeKVPutOptions{Expiration: &expiration}, nil
}

func runtimeKVListOptions(r *http.Request) (nanoflare.RuntimeKVListOptions, error) {
	query := r.URL.Query()
	if err := validateRuntimeKVQuery(query, "prefix", "key_count_limit", "cursor"); err != nil {
		return nanoflare.RuntimeKVListOptions{}, err
	}
	options := nanoflare.RuntimeKVListOptions{Prefix: query.Get("prefix"), Cursor: query.Get("cursor"), Limit: maxKVListKeyCount}
	if raw := query.Get("key_count_limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > maxKVListKeyCount {
			return nanoflare.RuntimeKVListOptions{}, errors.New("key_count_limit must be an integer between 1 and 1000")
		}
		options.Limit = limit
	}
	if _, err := nanoflare.DecodeRuntimeKVCursor(options.Cursor); err != nil {
		return nanoflare.RuntimeKVListOptions{}, err
	}
	return options, nil
}

func validateRuntimeKVQuery(query url.Values, allowed ...string) error {
	allowedNames := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedNames[name] = struct{}{}
	}
	for name := range query {
		if _, ok := allowedNames[name]; !ok {
			return errUnsupportedRuntimeKVOptions
		}
	}
	return nil
}
