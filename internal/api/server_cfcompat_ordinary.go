package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/clas/nanoflare/internal/cfcompat"
	"github.com/clas/nanoflare/internal/nanoflare"
)

// Wrangler patches these non-versioned settings after activation. Support tags
// and explicit disabled observability; reject features the runtime cannot honor.
func (s *Server) cfOrdinaryScriptSettings(w http.ResponseWriter, r *http.Request, name string) {
	if !s.cfScopes(w, r, "workers:read", "deployments:write", "secrets:write", "namespaces:dispatch") {
		return
	}
	var input struct {
		Tags          *[]string         `json:"tags"`
		Logpush       bool              `json:"logpush"`
		TailConsumers []json.RawMessage `json:"tail_consumers"`
		Observability *struct {
			Enabled bool `json:"enabled"`
		} `json:"observability"`
	}
	if err := cfReadObject(r, &input, "tags", "logpush", "tail_consumers", "observability"); err != nil {
		cfError(w, 422, err.Error())
		return
	}
	if input.Logpush || len(input.TailConsumers) != 0 || (input.Observability != nil && input.Observability.Enabled) {
		cfError(w, 422, "logpush, tail consumers and observability must remain disabled")
		return
	}
	app, err := s.service.OrdinaryScriptForOrg(controlOrgID(r), name)
	if err != nil {
		cfServiceError(w, err)
		return
	}
	if len(r.Header.Values("If-Match")) == 0 && len(r.Header.Values("Idempotency-Key")) == 0 {
		snapshot, found, err := s.service.ManagementUploadForOrg(controlOrgID(r), app.ID, false)
		if err != nil {
			cfServiceError(w, err)
			return
		}
		if found && (input.Tags == nil || slices.Equal(*input.Tags, snapshot.State.Metadata.Tags)) {
			cfJSON(w, s.cfSettings(snapshot))
			return
		}
	}
	s.cfApplyMutation(w, r, app, "script-settings.patch", input, func(snapshot nanoflare.ManagementUpload) (nanoflare.ManagedDeployInput, error) {
		in, err := cfPreservedMutation(snapshot, true)
		if input.Tags != nil {
			if err := cfcompat.ValidateTags(*input.Tags); err != nil {
				return in, err
			}
			in.Metadata.Tags = append([]string{}, (*input.Tags)...)
		}
		in.ConfigurationOnly = true
		return in, err
	}, func(snapshot nanoflare.ManagementUpload) any { return s.cfSettings(snapshot) })
}

// Version upload advances the latest management snapshot, independently of live
// traffic. Activation selects immutable revisions through the native validator
// and its runtime rollback path; it never rebuilds code or resolves secrets.
func (s *Server) cfOrdinaryDeployment(w http.ResponseWriter, r *http.Request, name string) {
	if !s.cfScopes(w, r, "deployments:write", "namespaces:dispatch") {
		return
	}
	if len(r.Header.Values("If-Match")) > 0 || len(r.Header.Values("Idempotency-Key")) > 0 {
		cfError(w, 422, "traffic selection does not accept mutation preconditions")
		return
	}
	var input struct {
		Strategy string `json:"strategy"`
		Versions []struct {
			ID         string `json:"version_id"`
			Percentage int    `json:"percentage"`
		} `json:"versions"`
		Annotations map[string]string `json:"annotations"`
	}
	if err := cfReadObject(r, &input, "strategy", "versions", "annotations"); err != nil {
		cfError(w, 422, err.Error())
		return
	}
	if input.Strategy != "percentage" {
		cfError(w, 422, "only percentage deployment strategy is supported")
		return
	}
	// Wrangler sends an empty annotations object for ordinary deploy. Do not
	// silently discard requested messages until durable deployment history exists.
	if len(input.Annotations) != 0 {
		cfError(w, 422, "deployment annotations are not supported")
		return
	}
	app, err := s.service.OrdinaryScriptForOrg(controlOrgID(r), name)
	if err != nil {
		cfServiceError(w, err)
		return
	}
	traffic := make([]nanoflare.DeploymentTraffic, 0, len(input.Versions))
	for _, version := range input.Versions {
		traffic = append(traffic, nanoflare.DeploymentTraffic{ID: version.ID, TrafficPercent: version.Percentage})
	}
	if _, err := s.service.SetDeploymentTrafficForOrg(controlOrgID(r), app.ID, traffic); err != nil {
		cfServiceError(w, err)
		return
	}
	cfJSON(w, map[string]any{"strategy": input.Strategy, "versions": input.Versions, "nanoflare_current_traffic": true})
}

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
	if len(parts) == 2 && parts[1] == "versions" && r.Method == "POST" {
		s.cfUploadWorker(w, r, nanoflare.DispatchNamespace{}, parts[0], true)
		return
	}
	if len(parts) == 2 && parts[1] == "deployments" && r.Method == "POST" {
		s.cfOrdinaryDeployment(w, r, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "script-settings" && r.Method == "PATCH" {
		s.cfOrdinaryScriptSettings(w, r, parts[0])
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
