package api

import (
	"errors"
	"net/http"

	"github.com/clas/nanoflare/internal/cfcompat"
	"github.com/clas/nanoflare/internal/nanoflare"
)

// Wrangler probes service metadata before first upload. Report only the native
// ordinary identity; no service environments or migrations are synthesized.
func (s *Server) cfOrdinaryService(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != "GET" && r.Method != "DELETE" {
		cfError(w, 404, "unsupported service compatibility route")
		return
	}
	if len(r.Header.Values("X-Nanoflare-Customer-ID")) > 0 {
		cfError(w, 422, "ordinary Workers do not accept customer identity headers")
		return
	}
	scope := "workers:read"
	if r.Method == "DELETE" {
		scope = "workers:write"
	}
	if !s.cfScopes(w, r, scope) {
		return
	}
	app, err := s.service.OrdinaryScriptForOrg(controlOrgID(r), name)
	if errors.Is(err, nanoflare.ErrAppNotFound) {
		writeJSON(w, 404, cfEnvelope{Errors: []cfAPIError{{Code: 10007, Message: "ordinary Worker not found"}}, Messages: []cfAPIError{}})
		return
	}
	if err != nil {
		cfServiceError(w, err)
		return
	}
	if r.Method == "DELETE" {
		if r.URL.Query().Get("force") != "true" || len(r.Header.Values("Idempotency-Key")) > 0 || len(r.Header.Values("If-Match")) > 0 {
			cfError(w, 422, "ordinary deletion requires force=true and does not accept mutation preconditions")
			return
		}
		if err := s.service.DeleteAppForOrg(controlOrgID(r), app.ID); err != nil {
			cfServiceError(w, err)
			return
		}
		cfJSON(w, nil)
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
	cfJSON(w, map[string]any{"id": name, "default_environment": map[string]any{"script": s.cfScriptResult(app, snapshot)}})
}

// Ordinary uploads use account-scoped script identity rather than namespace
// membership. The shared upload path preserves native publication/retry rules.
func (s *Server) cfOrdinaryScript(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(r.Header.Values("X-Nanoflare-Customer-ID")) > 0 {
		cfError(w, 422, "ordinary Workers do not accept customer identity headers")
		return
	}
	if len(parts) == 1 && r.Method == "PUT" {
		s.cfUploadWorker(w, r, nanoflare.DispatchNamespace{}, parts[0], true)
		return
	}
	if len(parts) >= 2 && parts[1] == "secrets" {
		mutation := (len(parts) == 2 && r.Method == "PUT") || (len(parts) == 3 && r.Method == "DELETE")
		read := (len(parts) == 2 || len(parts) == 3) && r.Method == "GET"
		if !mutation && !read {
			cfError(w, 404, "unsupported ordinary secret route")
			return
		}
		if mutation {
			if !s.cfScopes(w, r, "secrets:write", "deployments:write", "namespaces:dispatch") {
				return
			}
		} else if !s.cfScopes(w, r, "workers:read") {
			return
		}
		app, err := s.service.OrdinaryScriptForOrg(controlOrgID(r), parts[0])
		if err != nil {
			cfServiceError(w, err)
			return
		}
		if mutation {
			s.cfMutateScript(w, r, app, parts[1:], true)
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
		out := []map[string]string{}
		for _, secret := range snapshot.SecretRefs {
			item := map[string]string{"name": secret.Name, "type": "secret_text"}
			if len(parts) == 3 && parts[2] == secret.Name {
				cfJSON(w, item)
				return
			}
			out = append(out, item)
		}
		if len(parts) == 3 {
			cfServiceError(w, nanoflare.ErrSecretNotFound)
			return
		}
		cfJSON(w, out)
		return
	}
	if len(parts) == 2 && parts[1] == "subdomain" && r.Method == "GET" {
		if !s.cfScopes(w, r, "workers:read") {
			return
		}
		if _, err := s.service.OrdinaryScriptForOrg(controlOrgID(r), parts[0]); err != nil {
			cfServiceError(w, err)
			return
		}
		cfJSON(w, map[string]bool{"enabled": false, "previews_enabled": false})
		return
	}
	if len(parts) == 2 && parts[1] == "subdomain" && r.Method == "POST" {
		if !s.cfScopes(w, r, "workers:write") {
			return
		}
		if _, err := s.service.OrdinaryScriptForOrg(controlOrgID(r), parts[0]); err != nil {
			cfServiceError(w, err)
			return
		}
		data, err := cfMutationBody(r)
		var input struct {
			Enabled         *bool `json:"enabled"`
			PreviewsEnabled *bool `json:"previews_enabled"`
		}
		if err != nil || cfcompat.DecodeObject(data, &input, "enabled", "previews_enabled") != nil || input.Enabled == nil || *input.Enabled || (input.PreviewsEnabled != nil && *input.PreviewsEnabled) {
			cfError(w, 422, "only disabled workers.dev and preview URLs are supported; configure native ingress separately")
			return
		}
		cfJSON(w, map[string]bool{"enabled": false, "previews_enabled": false})
		return
	}
	if len(parts) != 2 || (parts[1] != "settings" && parts[1] != "deployments") || r.Method != "GET" {
		cfError(w, 404, "unsupported ordinary Worker compatibility route")
		return
	}
	if !s.cfScopes(w, r, "workers:read") {
		return
	}
	app, err := s.service.OrdinaryScriptForOrg(controlOrgID(r), parts[0])
	if parts[1] == "deployments" && errors.Is(err, nanoflare.ErrAppNotFound) {
		writeJSON(w, 404, cfEnvelope{Errors: []cfAPIError{{Code: 10007, Message: "ordinary Worker not found"}}, Messages: []cfAPIError{}})
		return
	}
	if err != nil {
		cfServiceError(w, err)
		return
	}
	if parts[1] == "deployments" {
		records, err := s.service.WorkerDeploymentsForOrg(controlOrgID(r), app.ID)
		if err != nil {
			cfServiceError(w, err)
			return
		}
		versions := []map[string]any{}
		for _, record := range records {
			if record.TrafficPercent > 0 {
				versions = append(versions, map[string]any{"version_id": record.ID, "percentage": record.TrafficPercent})
			}
		}
		deployments := []map[string]any{}
		if len(versions) > 0 {
			deployments = append(deployments, map[string]any{"versions": versions, "nanoflare_current_traffic": true})
		}
		cfJSON(w, map[string]any{"deployments": deployments})
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
	w.Header().Set("ETag", `"`+snapshot.DeploymentID+`"`)
	cfJSON(w, s.cfSettings(snapshot))
}
