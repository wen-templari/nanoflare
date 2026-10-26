package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/clas/nanoflare/internal/cfcompat"
	"github.com/clas/nanoflare/internal/nanoflare"
)

// cfApplyMutation identifies the original wire operation before reading a newer
// snapshot. Thus retries cannot reapply an old edit or undo a traffic rollback.
func (s *Server) cfApplyMutation(w http.ResponseWriter, r *http.Request, app nanoflare.App, operation string, payload any, prepare func(nanoflare.ManagementUpload) (nanoflare.ManagedDeployInput, error), render func(nanoflare.ManagementUpload) any) {
	if len(r.Header.Values("Idempotency-Key")) > 1 || len(r.Header.Values("If-Match")) > 1 || len(r.Header.Values("X-Nanoflare-Customer-ID")) > 1 {
		cfError(w, 422, "duplicate mutation identity header")
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) > 256 {
		cfError(w, 422, "Idempotency-Key exceeds 256 bytes")
		return
	}
	if key == "" {
		key = newRequestID()
	}
	identityPayload, err := json.Marshal(struct {
		Org, Worker, Method, Operation, IfMatch string
		Payload                                 any
	}{controlOrgID(r), app.ID, r.Method, operation, strings.TrimSpace(r.Header.Get("If-Match")), payload})
	if err != nil {
		cfError(w, 422, "invalid mutation")
		return
	}
	identity, err := s.service.ManagementRequestIdentity(identityPayload)
	if err != nil {
		cfError(w, 422, "invalid mutation identity")
		return
	}
	replay := func() bool {
		snapshot, found, err := s.service.ReplayManagedRequestForOrg(controlOrgID(r), app.ID, key, identity)
		if err != nil {
			cfServiceError(w, err)
			return true
		}
		if !found {
			return false
		}
		w.Header().Set("ETag", `"`+snapshot.DeploymentID+`"`)
		cfJSON(w, render(snapshot))
		return true
	}
	if replay() {
		return
	}
	snapshot, found, err := s.service.ManagementUploadForOrg(controlOrgID(r), app.ID, true)
	if err != nil {
		cfServiceError(w, err)
		return
	}
	if !found {
		cfError(w, 404, "script has no uploaded revision")
		return
	}
	if value := strings.TrimSpace(r.Header.Get("If-Match")); value != "" && value != "*" && value != snapshot.DeploymentID && value != `"`+snapshot.DeploymentID+`"` {
		cfError(w, 412, "latest upload does not match If-Match")
		return
	}
	input, err := prepare(snapshot)
	if err != nil {
		if replay() {
			return
		}
		if errors.Is(err, nanoflare.ErrSecretNotFound) {
			cfServiceError(w, err)
			return
		}
		cfError(w, 422, err.Error())
		return
	}
	if _, err := time.Parse("2006-01-02", input.Deployment.CompatibilityDate); err != nil || input.Deployment.CompatibilityDate > s.service.CompatibilityDateCeiling() {
		cfError(w, 422, "compatibility_date is invalid or exceeds the native runtime ceiling")
		return
	}
	input.Deployment.IdempotencyKey = key
	input.Deployment.CreatedBy = controlActor(r)
	input.RequestIdentity = identity
	deployment, err := s.service.ManagedDeployForOrg(controlOrgID(r), app.ID, input)
	if err != nil {
		if replay() {
			return
		}
		cfServiceError(w, err)
		return
	}
	result, err := s.service.ManagementUploadRevisionForOrg(controlOrgID(r), app.ID, deployment.ID)
	if err != nil {
		cfServiceError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+deployment.ID+`"`)
	cfJSON(w, render(result))
}

func cfPreservedMutation(snapshot nanoflare.ManagementUpload, allowDispatch ...bool) (nanoflare.ManagedDeployInput, error) {
	if snapshot.Deployment.Format != "modules" || (len(snapshot.Deployment.DispatchNamespaces) > 0 && !(len(allowDispatch) > 0 && allowDispatch[0])) {
		return nanoflare.ManagedDeployInput{}, errors.New("this native configuration requires the native API")
	}
	input := nanoflare.ManagedDeployInput{Deployment: snapshot.Deployment, ExpectedRevision: snapshot.State.Revision, BaseDeploymentID: snapshot.DeploymentID, Metadata: snapshot.State.Metadata, KeepSecrets: map[string]string{}}
	// Native publication supplies an ASSETS runtime default even when the
	// compatible upload omitted an explicit assets binding. Restore that intent
	// before validating a preserved mutation against management metadata.
	if len(input.Deployment.Assets) > 0 {
		explicit := false
		for _, raw := range input.Metadata.Bindings {
			var binding struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(raw, &binding); err != nil {
				return input, err
			}
			explicit = explicit || binding.Type == "assets"
		}
		if !explicit {
			input.Deployment.AssetConfig.Binding = ""
		}
	}
	for _, secret := range snapshot.SecretRefs {
		input.KeepSecrets[secret.Name] = secret.Name
	}
	return input, nil
}
func cfMutationBody(r *http.Request) ([]byte, error) {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		return nil, errors.New("application/json is required")
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil {
		return nil, errors.New("could not read mutation body")
	}
	if len(data) > 1<<20 {
		return nil, errors.New("mutation body exceeds 1 MiB")
	}
	return data, nil
}
func cfWithoutBinding(metadata nanoflare.ScriptManagementMetadata, name string) nanoflare.ScriptManagementMetadata {
	result := nanoflare.ScriptManagementMetadata{Tags: append([]string{}, metadata.Tags...), Bindings: []json.RawMessage{}}
	for _, raw := range metadata.Bindings {
		var binding struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(raw, &binding)
		if binding.Name != name {
			result.Bindings = append(result.Bindings, raw)
		}
	}
	return result
}

// Returns true only for a supported mutation route, leaving read operations to
// the shared reader. Each response excludes secret plaintext and source code.
func (s *Server) cfMutateScript(w http.ResponseWriter, r *http.Request, app nanoflare.App, suffix []string, allowDispatch ...bool) bool {
	if len(suffix) == 1 && suffix[0] == "secrets-bulk" && r.Method == "PATCH" {
		data, err := cfMutationBody(r)
		if err != nil {
			cfError(w, 422, err.Error())
			return true
		}
		patch, err := cfcompat.DecodeSecretPatch(data)
		if err != nil {
			cfError(w, 422, err.Error())
			return true
		}
		s.cfApplyMutation(w, r, app, "secrets.bulk", json.RawMessage(data), func(snapshot nanoflare.ManagementUpload) (nanoflare.ManagedDeployInput, error) {
			input, err := cfPreservedMutation(snapshot, allowDispatch...)
			if err != nil {
				return input, err
			}
			input.Secrets = map[string]string{}
			types := map[string]string{}
			for _, raw := range input.Metadata.Bindings {
				var binding struct {
					Name string `json:"name"`
					Type string `json:"type"`
				}
				_ = json.Unmarshal(raw, &binding)
				types[binding.Name] = binding.Type
			}
			names := []string{}
			for name := range patch {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				value := patch[name]
				if value == nil {
					if _, exists := input.KeepSecrets[name]; exists {
						delete(input.KeepSecrets, name)
						input.Metadata = cfWithoutBinding(input.Metadata, name)
					}
					continue
				}
				if kind := types[name]; kind != "" && kind != "secret_text" {
					return input, errors.New("secret name conflicts with a non-secret binding")
				}
				delete(input.KeepSecrets, name)
				input.Secrets[name] = value.Text
				input.Metadata = cfWithoutBinding(input.Metadata, name)
				input.Metadata.Bindings = append(input.Metadata.Bindings, mustCFJSON(map[string]string{"name": name, "type": "secret_text"}))
			}
			return input, nil
		}, func(snapshot nanoflare.ManagementUpload) any {
			result := map[string]map[string]string{}
			for _, secret := range snapshot.SecretRefs {
				result[secret.Name] = map[string]string{"name": secret.Name, "type": "secret_text"}
			}
			return result
		})
		return true
	}
	if len(suffix) == 1 && suffix[0] == "content" && r.Method == "PUT" {
		if len(r.Header.Values("CF-WORKER-MAIN-MODULE-PART")) > 1 || len(r.Header.Values("CF-WORKER-BODY-PART")) > 0 {
			cfError(w, 422, "only unambiguous module content is supported")
			return true
		}
		upload, err := cfcompat.DecodeContentUpload(r.Header.Get("Content-Type"), r.Body)
		if err != nil {
			cfError(w, 422, err.Error())
			return true
		}
		if main := r.Header.Get("CF-WORKER-MAIN-MODULE-PART"); main != "" && main != upload.Metadata.MainModule {
			cfError(w, 422, "main module header conflicts with metadata")
			return true
		}
		payload, err := upload.IdentityPayload()
		if err != nil {
			cfError(w, 422, "invalid content identity")
			return true
		}
		s.cfApplyMutation(w, r, app, "content.put", json.RawMessage(payload), func(snapshot nanoflare.ManagementUpload) (nanoflare.ManagedDeployInput, error) {
			input, err := cfPreservedMutation(snapshot, allowDispatch...)
			if err != nil {
				return input, err
			}
			input.Deployment.Files = upload.Files
			input.Deployment.Entrypoint = upload.Metadata.MainModule
			return input, nil
		}, func(snapshot nanoflare.ManagementUpload) any { return s.cfScriptResult(app, snapshot) })
		return true
	}
	if len(suffix) == 1 && suffix[0] == "settings" && r.Method == "PATCH" {
		data, err := cfcompat.DecodeSettings(r.Header.Get("Content-Type"), r.Body)
		if err != nil {
			cfError(w, 422, err.Error())
			return true
		}
		s.cfApplyMutation(w, r, app, "settings.patch", json.RawMessage(data), func(snapshot nanoflare.ManagementUpload) (nanoflare.ManagedDeployInput, error) {
			previous, err := cfPrevious(snapshot)
			if err != nil {
				return nanoflare.ManagedDeployInput{}, err
			}
			candidate, err := cfcompat.ApplySettings(data, previous, s.cfResolver(controlOrgID(r)))
			if err != nil {
				return nanoflare.ManagedDeployInput{}, err
			}
			return cfManagedInput(candidate, snapshot)
		}, func(snapshot nanoflare.ManagementUpload) any { return s.cfSettings(snapshot) })
		return true
	}
	if len(suffix) == 1 && suffix[0] == "secrets" && r.Method == "PUT" {
		var secret struct {
			Name string  `json:"name"`
			Type string  `json:"type"`
			Text *string `json:"text"`
		}
		if err := cfReadObject(r, &secret, "name", "type", "text"); err != nil {
			cfError(w, 422, err.Error())
			return true
		}
		if secret.Type != "secret_text" || secret.Text == nil || secret.Name == "" {
			cfError(w, 422, "a named secret_text value is required")
			return true
		}
		s.cfApplyMutation(w, r, app, "secrets.put", secret, func(snapshot nanoflare.ManagementUpload) (nanoflare.ManagedDeployInput, error) {
			input, err := cfPreservedMutation(snapshot, allowDispatch...)
			if err != nil {
				return input, err
			}
			for _, raw := range input.Metadata.Bindings {
				var binding struct {
					Name string `json:"name"`
					Type string `json:"type"`
				}
				_ = json.Unmarshal(raw, &binding)
				if binding.Name == secret.Name && binding.Type != "secret_text" {
					return input, errors.New("secret name conflicts with a non-secret binding")
				}
			}
			input.Metadata = cfWithoutBinding(input.Metadata, secret.Name)
			input.Metadata.Bindings = append(input.Metadata.Bindings, mustCFJSON(map[string]string{"name": secret.Name, "type": "secret_text"}))
			delete(input.KeepSecrets, secret.Name)
			input.Secrets = map[string]string{secret.Name: *secret.Text}
			return input, nil
		}, func(nanoflare.ManagementUpload) any {
			return map[string]string{"name": secret.Name, "type": "secret_text"}
		})
		return true
	}
	if len(suffix) == 2 && suffix[0] == "secrets" && r.Method == "DELETE" {
		name := suffix[1]
		s.cfApplyMutation(w, r, app, "secrets.delete", name, func(snapshot nanoflare.ManagementUpload) (nanoflare.ManagedDeployInput, error) {
			input, err := cfPreservedMutation(snapshot, allowDispatch...)
			if err != nil {
				return input, err
			}
			if _, exists := input.KeepSecrets[name]; !exists {
				return input, nanoflare.ErrSecretNotFound
			}
			delete(input.KeepSecrets, name)
			input.Metadata = cfWithoutBinding(input.Metadata, name)
			return input, nil
		}, func(nanoflare.ManagementUpload) any { return nil })
		return true
	}
	if len(suffix) == 1 && suffix[0] == "tags" && r.Method == "PUT" {
		data, err := cfMutationBody(r)
		if err != nil {
			cfError(w, 422, err.Error())
			return true
		}
		tags, err := cfcompat.DecodeTags(data)
		if err != nil {
			cfError(w, 422, err.Error())
			return true
		}
		s.cfApplyMutation(w, r, app, "tags.put", tags, func(snapshot nanoflare.ManagementUpload) (nanoflare.ManagedDeployInput, error) {
			input, err := cfPreservedMutation(snapshot, allowDispatch...)
			input.Metadata.Tags = append([]string{}, tags...)
			input.ConfigurationOnly = true
			return input, err
		}, func(snapshot nanoflare.ManagementUpload) any {
			return append([]string{}, snapshot.State.Metadata.Tags...)
		})
		return true
	}
	if len(suffix) == 2 && suffix[0] == "tags" && (r.Method == "DELETE" || r.Method == "PUT") {
		name := suffix[1]
		if err := cfcompat.ValidateTags([]string{name}); err != nil {
			cfError(w, 422, err.Error())
			return true
		}
		s.cfApplyMutation(w, r, app, "tags.item", name, func(snapshot nanoflare.ManagementUpload) (nanoflare.ManagedDeployInput, error) {
			input, err := cfPreservedMutation(snapshot, allowDispatch...)
			if err != nil {
				return input, err
			}
			tags := []string{}
			for _, tag := range input.Metadata.Tags {
				if tag != name {
					tags = append(tags, tag)
				}
			}
			if r.Method == "PUT" {
				tags = append(tags, name)
			}
			if err := cfcompat.ValidateTags(tags); err != nil {
				return input, err
			}
			input.Metadata.Tags = tags
			input.ConfigurationOnly = true
			return input, nil
		}, func(nanoflare.ManagementUpload) any { return nil })
		return true
	}
	return false
}
func mustCFJSON(value any) json.RawMessage { data, _ := json.Marshal(value); return data }
