package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/clas/nanoflare/internal/cfcompat"
	"github.com/clas/nanoflare/internal/nanoflare"
)

// The adapter is an explicit trusted-profile pilot. Enabling it does not enable
// native dispatch or imply untrusted execution or drop-in client compatibility.
func (s *Server) SetCloudflareCompatibilityEnabled(enabled bool) {
	s.cloudflareCompatibility.Store(enabled)
}

type cfAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type cfEnvelope struct {
	Success  bool         `json:"success"`
	Errors   []cfAPIError `json:"errors"`
	Messages []cfAPIError `json:"messages"`
	Result   any          `json:"result"`
}

func cfJSON(w http.ResponseWriter, result any) {
	writeJSON(w, 200, cfEnvelope{Success: true, Errors: []cfAPIError{}, Messages: []cfAPIError{}, Result: result})
}
func cfError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, cfEnvelope{Errors: []cfAPIError{{Code: 10000, Message: message}}, Messages: []cfAPIError{}, Result: nil})
}
func cfServiceError(w http.ResponseWriter, err error) {
	status := 400
	switch {
	case errors.Is(err, nanoflare.ErrAppNotFound), errors.Is(err, nanoflare.ErrDispatchNamespaceNotFound), errors.Is(err, nanoflare.ErrArtifactNotFound), errors.Is(err, nanoflare.ErrSecretNotFound), errors.Is(err, nanoflare.ErrKVNamespaceNotFound), errors.Is(err, nanoflare.ErrDatabaseNotFound), errors.Is(err, nanoflare.ErrObjectStorageBucketNotFound):
		status = 404
	case errors.Is(err, nanoflare.ErrManagementConflict), errors.Is(err, nanoflare.ErrIdempotencyConflict), errors.Is(err, nanoflare.ErrDispatchNamespaceExists), errors.Is(err, nanoflare.ErrDispatchNamespaceInUse):
		status = 409
	case errors.Is(err, nanoflare.ErrNamespaceDeletionConflict):
		status = 409
	case errors.Is(err, nanoflare.ErrNamespaceGrantDenied):
		status = 403
	case errors.Is(err, nanoflare.ErrUsageLimitExceeded):
		status = 402
	case errors.Is(err, nanoflare.ErrDispatchUnavailable), errors.Is(err, nanoflare.ErrUnsupportedExecutionProfile):
		status = 422
	}
	// Backend errors can contain storage URLs or response bodies. Typed public
	// errors are safe; arbitrary service failures receive a stable generic body.
	message := "request rejected; inspect native publication status for details"
	if status != 400 {
		message = err.Error()
	}
	cfError(w, status, message)
}

type cfDiscardWriter struct {
	status int
	header http.Header
}

func (w *cfDiscardWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *cfDiscardWriter) WriteHeader(status int)          { w.status = status }
func (w *cfDiscardWriter) Write(value []byte) (int, error) { return len(value), nil }
func (s *Server) cfScopes(w http.ResponseWriter, r *http.Request, scopes ...string) bool {
	for _, scope := range scopes {
		sink := &cfDiscardWriter{}
		if !s.requireScope(sink, r, scope) {
			cfError(w, 403, "required Nanoflare scope: "+scope)
			return false
		}
	}
	return true
}

func (s *Server) serveCloudflareCompatibility(w http.ResponseWriter, r *http.Request) {
	if !s.cloudflareCompatibility.Load() {
		cfError(w, 404, "compatibility adapter is disabled")
		return
	}
	if s.controlAuth == nil {
		cfError(w, 503, "control authentication is required")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/client/v4/"), "/")
	for index, part := range parts {
		decoded, err := url.PathUnescape(part)
		if err != nil {
			cfError(w, 400, "invalid compatibility path")
			return
		}
		parts[index] = decoded
	}
	assetUpload := len(parts) == 5 && parts[0] == "accounts" && parts[2] == "workers" && parts[3] == "assets" && parts[4] == "upload"
	if len(parts) < 5 || parts[0] != "accounts" || parts[1] == "" || parts[2] != "workers" || (!assetUpload && (parts[3] != "dispatch" || parts[4] != "namespaces")) {
		cfError(w, 404, "unsupported compatibility route")
		return
	}
	for index, part := range parts {
		item := len(parts) == 10 && index == 9 && (parts[8] == "tags" || parts[8] == "secrets")
		if part == "" || (!item && (part == "." || part == ".." || strings.ContainsAny(part, "/\\\x00"))) {
			cfError(w, 400, "invalid compatibility path")
			return
		}
	}
	if assetUpload {
		s.cfAssetFiles(w, r, parts[1])
		return
	}
	authorized := r.Clone(r.Context())
	authorized.SetPathValue("orgID", parts[1])
	sink := &cfDiscardWriter{}
	next, ok := s.authenticateControlRequest(sink, authorized)
	if !ok {
		status := sink.status
		if status == 0 {
			status = 401
		}
		cfError(w, status, "Nanoflare account authorization denied")
		return
	}
	r = next
	// Organization-scoped PAT authentication resolves the token's organization;
	// native handlers separately enforce requirePathOrg. This adapter must do
	// the same before resolving any namespace or performing a mutation.
	if controlOrgID(r) != parts[1] {
		cfError(w, 403, "Nanoflare account authorization denied")
		return
	}
	w.Header().Set("X-Nanoflare-Execution-Profile", "trusted_shared_workerd")
	if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("X-Nanoflare-Execution-Profile") != "trusted_shared_workerd" {
		cfError(w, 422, "mutations require X-Nanoflare-Execution-Profile: trusted_shared_workerd")
		return
	}
	allowedQuery := map[string]bool{}
	if len(parts) == 8 && r.Method == "PUT" {
		allowedQuery["bindings_inherit"] = true
		allowedQuery["excludeScript"] = true
	}
	if len(parts) == 7 && (r.Method == "GET" || r.Method == "DELETE") {
		allowedQuery["tags"] = true
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		cfError(w, 422, "invalid compatibility query")
		return
	}
	for name, values := range query {
		if !allowedQuery[name] || len(values) != 1 {
			cfError(w, 422, "unsupported compatibility query parameter")
			return
		}
	}
	if value := r.URL.Query().Get("bindings_inherit"); value != "" && value != "strict" {
		cfError(w, 422, "bindings_inherit supports only strict")
		return
	}
	if values, exists := query["excludeScript"]; exists && values[0] != "true" {
		cfError(w, 422, "excludeScript supports only true; source is available from the content endpoint")
		return
	}
	if len(parts) == 5 {
		s.cfNamespaceCollection(w, r)
		return
	}
	if len(parts) == 6 {
		scope := "namespaces:read"
		if r.Method == "DELETE" {
			scope = "namespaces:write"
		}
		if !s.cfScopes(w, r, scope) {
			return
		}
	} else {
		scopes := []string{"workers:read"}
		if len(parts) == 7 && r.Method == "DELETE" {
			scopes = []string{"workers:write"}
		}
		if len(parts) == 8 && r.Method == "PUT" {
			scopes = []string{"workers:write", "deployments:write", "secrets:write"}
		}
		if len(parts) == 8 && r.Method == "DELETE" {
			scopes = []string{"workers:write"}
		}
		if len(parts) == 9 && parts[8] == "settings" && r.Method == "PATCH" {
			scopes = []string{"workers:read", "deployments:write", "secrets:write"}
		}
		if len(parts) == 9 && parts[8] == "content" && r.Method == "PUT" {
			scopes = []string{"deployments:write"}
		}
		if len(parts) == 9 && parts[8] == "assets-upload-session" && r.Method == "POST" {
			scopes = []string{"workers:write", "deployments:write"}
		}
		if len(parts) >= 9 && parts[8] == "secrets" && ((len(parts) == 9 && r.Method == "PUT") || (len(parts) == 10 && r.Method == "DELETE")) {
			scopes = []string{"deployments:write", "secrets:write"}
		}
		if len(parts) == 9 && parts[8] == "secrets-bulk" && r.Method == "PATCH" {
			scopes = []string{"deployments:write", "secrets:write"}
		}
		if len(parts) >= 9 && parts[8] == "tags" && ((len(parts) == 9 && r.Method == "PUT") || (len(parts) == 10 && (r.Method == "DELETE" || r.Method == "PUT"))) {
			scopes = []string{"workers:write"}
		}
		if !s.cfScopes(w, r, scopes...) {
			return
		}
	}
	namespace, err := s.cfNamespaceByName(controlOrgID(r), parts[5])
	if err != nil {
		cfServiceError(w, err)
		return
	}
	if len(parts) == 6 {
		s.cfNamespaceItem(w, r, namespace)
		return
	}
	if parts[6] != "scripts" {
		cfError(w, 404, "unsupported compatibility route")
		return
	}
	if len(parts) == 7 {
		s.cfScriptCollection(w, r, namespace)
		return
	}
	if len(parts) == 8 && r.Method == "PUT" {
		s.cfUploadScript(w, r, namespace, parts[7])
		return
	}
	if len(parts) == 9 && parts[8] == "assets-upload-session" && r.Method == "POST" {
		s.cfCreateAssetSession(w, r, namespace, parts[7])
		return
	}
	app, err := s.service.NamespaceScript(controlOrgID(r), namespace.ID, parts[7])
	if err != nil {
		cfServiceError(w, err)
		return
	}
	if app.Platform.Kind != "customer" {
		cfError(w, 422, "compatibility routes support customer scripts only")
		return
	}
	if len(parts) == 8 && r.Method == "DELETE" {
		if !s.cfScopes(w, r, "workers:write") {
			return
		}
		if err := s.service.DeleteAppForOrg(controlOrgID(r), app.ID); err != nil {
			cfServiceError(w, err)
			return
		}
		cfJSON(w, nil)
		return
	}
	if len(parts) >= 9 {
		if value := r.Header.Get("X-Nanoflare-Customer-ID"); value != "" && value != app.Platform.CustomerID {
			cfError(w, 422, "customer identity is immutable")
			return
		}
		if s.cfMutateScript(w, r, app, parts[8:]) {
			return
		}
	}
	if !s.cfScopes(w, r, "workers:read") {
		return
	}
	if r.Method != "GET" {
		cfError(w, 422, "unsupported compatibility operation")
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
	if len(parts) == 10 && parts[8] == "secrets" {
		for _, secret := range snapshot.SecretRefs {
			if secret.Name == parts[9] {
				cfJSON(w, map[string]string{"name": secret.Name, "type": "secret_text"})
				return
			}
		}
		cfServiceError(w, nanoflare.ErrSecretNotFound)
		return
	}
	if len(parts) == 8 {
		cfJSON(w, map[string]any{"created_on": app.CreatedAt, "modified_on": snapshot.State.UpdatedAt, "dispatch_namespace": namespace.Name, "script": s.cfScriptResult(app, snapshot)})
		return
	}
	if len(parts) == 9 {
		switch parts[8] {
		case "settings":
			cfJSON(w, s.cfSettings(snapshot))
			return
		case "content":
			s.cfScriptContent(w, snapshot)
			return
		case "bindings":
			cfJSON(w, append([]json.RawMessage{}, snapshot.State.Metadata.Bindings...))
			return
		case "tags":
			cfJSON(w, append([]string{}, snapshot.State.Metadata.Tags...))
			return
		case "secrets":
			out := []map[string]string{}
			for _, secret := range snapshot.SecretRefs {
				out = append(out, map[string]string{"name": secret.Name, "type": "secret_text"})
			}
			cfJSON(w, out)
			return
		}
	}
	cfError(w, 404, "unsupported compatibility route")
}

func (s *Server) cfNamespaceByName(org, name string) (nanoflare.DispatchNamespace, error) {
	items, err := s.service.DispatchNamespacesForOrg(org)
	if err != nil {
		return nanoflare.DispatchNamespace{}, err
	}
	for _, item := range items {
		if item.Name == name {
			return item, nil
		}
	}
	return nanoflare.DispatchNamespace{}, nanoflare.ErrDispatchNamespaceNotFound
}
func (s *Server) cfNamespaceResult(ns nanoflare.DispatchNamespace) (map[string]any, error) {
	workers, err := s.service.NamespaceScripts(ns.OrgID, ns.ID)
	if err != nil {
		return nil, err
	}
	count := 0
	for _, worker := range workers {
		if worker.Platform.Kind == "customer" {
			count++
		}
	}
	return map[string]any{"namespace_id": ns.ID, "namespace_name": ns.Name, "created_on": ns.CreatedAt, "script_count": count, "nanoflare_execution_profile": "trusted_shared_workerd"}, nil
}
func cfReadObject(r *http.Request, target any, fields ...string) error {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		return errors.New("application/json is required")
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("JSON body exceeds 1 MiB")
	}
	return cfcompat.DecodeObject(data, target, fields...)
}
func (s *Server) cfNamespaceCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		if !s.cfScopes(w, r, "namespaces:read") {
			return
		}
		items, err := s.service.DispatchNamespacesForOrg(controlOrgID(r))
		if err != nil {
			cfServiceError(w, err)
			return
		}
		out := []map[string]any{}
		for _, item := range items {
			result, err := s.cfNamespaceResult(item)
			if err != nil {
				cfServiceError(w, err)
				return
			}
			out = append(out, result)
		}
		cfJSON(w, out)
	case "POST":
		if !s.cfScopes(w, r, "namespaces:write") {
			return
		}
		var input struct {
			Name string `json:"name"`
		}
		if err := cfReadObject(r, &input, "name"); err != nil {
			cfError(w, 422, err.Error())
			return
		}
		item, err := s.service.CreateDispatchNamespace(controlOrgID(r), nanoflare.CreateDispatchNamespaceInput{Name: input.Name, Trust: "trusted"})
		if err != nil {
			cfServiceError(w, err)
			return
		}
		result, err := s.cfNamespaceResult(item)
		if err != nil {
			cfServiceError(w, err)
			return
		}
		cfJSON(w, result)
	default:
		cfError(w, 422, "unsupported compatibility operation")
	}
}
func (s *Server) cfNamespaceItem(w http.ResponseWriter, r *http.Request, ns nanoflare.DispatchNamespace) {
	switch r.Method {
	case "GET":
		if !s.cfScopes(w, r, "namespaces:read") {
			return
		}
		result, err := s.cfNamespaceResult(ns)
		if err != nil {
			cfServiceError(w, err)
			return
		}
		cfJSON(w, result)
	case "DELETE":
		if !s.cfScopes(w, r, "namespaces:write") {
			return
		}
		if err := s.service.DeleteDispatchNamespace(controlOrgID(r), ns.ID); err != nil {
			cfServiceError(w, err)
			return
		}
		cfJSON(w, nil)
	default:
		cfError(w, 422, "unsupported compatibility operation")
	}
}
func (s *Server) cfScriptCollection(w http.ResponseWriter, r *http.Request, ns nanoflare.DispatchNamespace) {
	if r.Method == "DELETE" {
		s.cfDeleteScriptsByTag(w, r, ns)
		return
	}
	if !s.cfScopes(w, r, "workers:read") {
		return
	}
	if r.Method != "GET" {
		cfError(w, 422, "unsupported compatibility operation")
		return
	}
	var filterName string
	var filterPresent bool
	if value, exists := r.URL.Query()["tags"]; exists {
		name, present, err := cfcompat.ParseTagFilter(value[0])
		if err != nil {
			cfError(w, 422, err.Error())
			return
		}
		filterName, filterPresent = name, present
	}
	apps, err := s.service.NamespaceScripts(controlOrgID(r), ns.ID)
	if err != nil {
		cfServiceError(w, err)
		return
	}
	out := []map[string]any{}
	for _, app := range apps {
		if app.Platform.Kind != "customer" {
			continue
		}
		snapshot, _, err := s.service.ManagementUploadForOrg(controlOrgID(r), app.ID, false)
		if err != nil {
			cfServiceError(w, err)
			return
		}
		if filterName != "" {
			present := false
			for _, tag := range snapshot.State.Metadata.Tags {
				present = present || tag == filterName
			}
			if present != filterPresent {
				continue
			}
		}
		out = append(out, s.cfScriptResult(app, snapshot))
	}
	cfJSON(w, out)
}
func (s *Server) cfScriptResult(app nanoflare.App, snapshot nanoflare.ManagementUpload) map[string]any {
	return map[string]any{"id": app.Platform.ScriptName, "etag": snapshot.DeploymentID, "created_on": app.CreatedAt, "modified_on": snapshot.State.UpdatedAt, "tags": append([]string{}, snapshot.State.Metadata.Tags...), "nanoflare_runtime_ready_verified": false}
}
func (s *Server) cfSettings(snapshot nanoflare.ManagementUpload) map[string]any {
	return map[string]any{"bindings": append([]json.RawMessage{}, snapshot.State.Metadata.Bindings...), "compatibility_date": snapshot.Deployment.CompatibilityDate, "compatibility_flags": append([]string{}, snapshot.Deployment.CompatibilityFlags...), "tags": append([]string{}, snapshot.State.Metadata.Tags...)}
}

func (s *Server) cfResolver(org string) cfcompat.Resolver {
	return func(kind, name string) (string, error) {
		switch kind {
		case "r2_bucket":
			items, err := s.service.ListObjectStorageBucketsForOrg(org)
			if err != nil {
				return "", err
			}
			for _, item := range items {
				if item.Name == name {
					return item.ID, nil
				}
			}
		case "service":
			items, err := s.service.ListAppsForOrg(org)
			if err != nil {
				return "", err
			}
			for _, item := range items {
				if item.ID == name {
					return item.ID, nil
				}
			}
			id := ""
			for _, item := range items {
				if item.Platform == nil && item.Name == name {
					if id != "" {
						return "", nanoflare.ErrAmbiguousService
					}
					id = item.ID
				}
			}
			if id != "" {
				return id, nil
			}
		}
		return "", nanoflare.ErrAppNotFound
	}
}
func cfPrevious(snapshot nanoflare.ManagementUpload) (*cfcompat.Previous, error) {
	previous := &cfcompat.Previous{Deployment: snapshot.Deployment, Tags: snapshot.State.Metadata.Tags}
	for _, raw := range snapshot.State.Metadata.Bindings {
		var binding cfcompat.Binding
		if err := json.Unmarshal(raw, &binding); err != nil {
			return nil, err
		}
		previous.Bindings = append(previous.Bindings, binding)
	}
	return previous, nil
}
func cfManagedInput(candidate cfcompat.Candidate, snapshot nanoflare.ManagementUpload) (nanoflare.ManagedDeployInput, error) {
	metadata := nanoflare.ScriptManagementMetadata{Tags: candidate.Tags, Bindings: []json.RawMessage{}}
	for _, binding := range candidate.Bindings {
		raw, err := json.Marshal(binding)
		if err != nil {
			return nanoflare.ManagedDeployInput{}, err
		}
		metadata.Bindings = append(metadata.Bindings, raw)
	}
	return nanoflare.ManagedDeployInput{Deployment: candidate.Deployment, ExpectedRevision: snapshot.State.Revision, BaseDeploymentID: snapshot.DeploymentID, Metadata: metadata, Secrets: candidate.Secrets, KeepSecrets: candidate.KeepSecrets}, nil
}
func (s *Server) cfReplayUpload(w http.ResponseWriter, r *http.Request, app nanoflare.App, key, identity string) bool {
	original, found, err := s.service.ReplayManagedRequestForOrg(controlOrgID(r), app.ID, key, identity)
	if err != nil {
		cfServiceError(w, err)
		return true
	}
	if !found {
		return false
	}
	w.Header().Set("ETag", `"`+original.DeploymentID+`"`)
	cfJSON(w, s.cfScriptResult(app, original))
	return true
}
func (s *Server) cfUploadScript(w http.ResponseWriter, r *http.Request, ns nanoflare.DispatchNamespace, name string) {
	if !s.cfScopes(w, r, "workers:write", "deployments:write", "secrets:write") {
		return
	}
	if len(r.Header.Values("If-Match")) > 0 {
		cfError(w, 422, "If-Match is supported on configuration/content mutations, not full script upload")
		return
	}
	if len(r.Header.Values("Idempotency-Key")) > 1 || len(r.Header.Values("X-Nanoflare-Customer-ID")) > 1 {
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
	upload, err := cfcompat.DecodeUpload(r.Header.Get("Content-Type"), r.Body)
	if err != nil {
		cfError(w, 422, err.Error())
		return
	}
	payload, err := upload.IdentityPayload()
	if err != nil {
		cfError(w, 422, "invalid upload identity")
		return
	}
	identityPayload, _ := json.Marshal(struct {
		Org, Namespace, Script, Customer string
		Strict                           bool
		Payload                          json.RawMessage
	}{controlOrgID(r), ns.ID, name, r.Header.Get("X-Nanoflare-Customer-ID"), r.URL.Query().Get("bindings_inherit") == "strict", payload})
	identity, err := s.service.ManagementRequestIdentity(identityPayload)
	if err != nil {
		cfError(w, 422, "invalid upload identity")
		return
	}
	app, err := s.service.NamespaceScript(controlOrgID(r), ns.ID, name)
	newScript := errors.Is(err, nanoflare.ErrAppNotFound)
	if err != nil && !newScript {
		cfServiceError(w, err)
		return
	}
	if !newScript && app.Platform.Kind != "customer" {
		cfError(w, 422, "compatibility routes support customer scripts only")
		return
	}
	if !newScript && r.Header.Get("X-Nanoflare-Customer-ID") != "" && r.Header.Get("X-Nanoflare-Customer-ID") != app.Platform.CustomerID {
		cfError(w, 422, "customer identity is immutable")
		return
	}
	if !newScript {
		if s.cfReplayUpload(w, r, app, key, identity) {
			return
		}
	}
	if _, err := time.Parse("2006-01-02", upload.Metadata.CompatibilityDate); err != nil || upload.Metadata.CompatibilityDate > s.service.CompatibilityDateCeiling() {
		cfError(w, 422, "compatibility_date is invalid or exceeds the native runtime ceiling")
		return
	}
	var snapshot nanoflare.ManagementUpload
	var previous *cfcompat.Previous
	if !newScript {
		var found bool
		snapshot, found, err = s.service.ManagementUploadForOrg(controlOrgID(r), app.ID, false)
		if err != nil {
			cfServiceError(w, err)
			return
		}
		if found {
			previous, err = cfPrevious(snapshot)
			if err != nil {
				cfServiceError(w, err)
				return
			}
		}
	}
	options := cfcompat.Options{StrictInherit: r.URL.Query().Get("bindings_inherit") == "strict"}
	if upload.Metadata.Assets != nil {
		customer := r.Header.Get("X-Nanoflare-Customer-ID")
		if !newScript {
			customer = app.Platform.CustomerID
		}
		options.LoadCompletedAssets = func() ([]nanoflare.AssetFile, error) {
			return s.service.CompletedAssetsForOrg(controlOrgID(r), ns.ID, name, customer, upload.Metadata.Assets.JWT)
		}
	}
	if previous != nil {
		options.LoadRetainedAssets = func() ([]nanoflare.AssetFile, error) {
			current, _, err := s.service.ManagementUploadForOrg(controlOrgID(r), app.ID, true)
			if err != nil {
				return nil, errors.New("retained assets are unavailable")
			}
			if current.DeploymentID != snapshot.DeploymentID {
				return nil, nanoflare.ErrManagementConflict
			}
			return current.Deployment.Assets, nil
		}
	}
	candidate, err := cfcompat.CompleteUpload(upload, previous, s.cfResolver(controlOrgID(r)), options)
	if err != nil {
		if !newScript && s.cfReplayUpload(w, r, app, key, identity) {
			return
		}
		if errors.Is(err, nanoflare.ErrManagementConflict) {
			cfServiceError(w, err)
			return
		}
		cfError(w, 422, err.Error())
		return
	}
	if newScript {
		app, err = s.service.CreateNamespaceScript(controlOrgID(r), ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: name, CustomerID: r.Header.Get("X-Nanoflare-Customer-ID"), Execution: "dynamic"})
		if err != nil {
			cfServiceError(w, err)
			return
		}
	}
	input, err := cfManagedInput(candidate, snapshot)
	if err != nil {
		cfServiceError(w, err)
		return
	}
	input.Deployment.IdempotencyKey = key
	if upload.Metadata.Assets != nil {
		input.AssetUploadToken = upload.Metadata.Assets.JWT
	}
	input.RequestIdentity = identity
	input.Deployment.CreatedBy = controlActor(r)
	deployment, err := s.service.ManagedDeployForOrg(controlOrgID(r), app.ID, input)
	if err != nil {
		if s.cfReplayUpload(w, r, app, key, identity) {
			return
		}
		cfServiceError(w, err)
		return
	}
	snapshot, err = s.service.ManagementUploadRevisionForOrg(controlOrgID(r), app.ID, deployment.ID)
	if err != nil {
		cfServiceError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+deployment.ID+`"`)
	cfJSON(w, s.cfScriptResult(app, snapshot))
}
