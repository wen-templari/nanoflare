package api

import (
	"net/http"
	"sort"
	"strconv"
)

// D1 discovery exposes native IDs for bindings. Provisioning and SQL management
// remain native operations.
func (s *Server) cfD1Collection(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		cfError(w, 404, "unsupported D1 compatibility route")
		return
	}
	if len(r.Header.Values("X-Nanoflare-Customer-ID")) > 0 {
		cfError(w, 422, "D1 listing does not accept customer identity headers")
		return
	}
	if !s.cfScopes(w, r, "db:read") {
		return
	}
	page, size := 1, 100
	for name, target := range map[string]*int{"page": &page, "per_page": &size} {
		if raw := r.URL.Query().Get(name); raw != "" {
			value, err := strconv.Atoi(raw)
			limit := 1000000
			if name == "per_page" {
				limit = 1000
			}
			if err != nil || value < 1 || value > limit {
				cfError(w, 422, "invalid "+name)
				return
			}
			*target = value
		}
	}
	databases, err := s.service.ListDatabasesForOrg(controlOrgID(r))
	if err != nil {
		cfServiceError(w, err)
		return
	}
	sort.Slice(databases, func(i, j int) bool { return databases[i].ID < databases[j].ID })
	start := min((page-1)*size, len(databases))
	end := min(start+size, len(databases))
	result := []map[string]any{}
	for _, db := range databases[start:end] {
		result = append(result, map[string]any{"uuid": db.ID, "name": db.Name, "created_at": db.CreatedAt})
	}
	cfJSON(w, result)
}
