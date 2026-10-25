package api

import (
	"net/http"
	"sort"
	"strconv"
)

func (s *Server) cfKVCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		cfError(w, 404, "unsupported KV compatibility route")
		return
	}
	if len(r.Header.Values("X-Nanoflare-Customer-ID")) > 0 {
		cfError(w, 422, "KV listing does not accept customer identity headers")
		return
	}
	if !s.cfScopes(w, r, "kv:read") {
		return
	}
	query := r.URL.Query()
	page, size := 1, 100
	var err error
	if value := query.Get("page"); value != "" {
		page, err = strconv.Atoi(value)
		if err != nil || page < 1 || page > 1000000 {
			cfError(w, 422, "invalid page")
			return
		}
	}
	if value := query.Get("per_page"); value != "" {
		size, err = strconv.Atoi(value)
		if err != nil || size < 1 || size > 1000 {
			cfError(w, 422, "invalid per_page")
			return
		}
	}
	if (query.Get("order") != "" && query.Get("order") != "title") || (query.Get("direction") != "" && query.Get("direction") != "asc") {
		cfError(w, 422, "only ascending title ordering is supported")
		return
	}
	namespaces, err := s.service.ListKVNamespacesForOrg(controlOrgID(r))
	if err != nil {
		cfServiceError(w, err)
		return
	}
	sort.Slice(namespaces, func(i, j int) bool {
		if namespaces[i].Name == namespaces[j].Name {
			return namespaces[i].ID < namespaces[j].ID
		}
		return namespaces[i].Name < namespaces[j].Name
	})
	start := (page - 1) * size
	if start > len(namespaces) {
		start = len(namespaces)
	}
	end := start + size
	if end > len(namespaces) {
		end = len(namespaces)
	}
	result := []map[string]string{}
	for _, namespace := range namespaces[start:end] {
		result = append(result, map[string]string{"id": namespace.ID, "title": namespace.Name})
	}
	cfJSON(w, result)
}
