package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

// openAPIError is the established JSON error response returned by this API.
type openAPIError struct {
	Error string `json:"error"`
}

type openAPIOperation struct {
	method, path, id, tag string
	public                bool
	request, response     any
	status                int
}

type authRefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}
type patSessionRequest struct {
	Token string `json:"token"`
}
type oidcSessionRequest struct {
	Code string `json:"code"`
}
type cliCodeResponse struct {
	Code string `json:"code"`
}
type oidcConfigResponse struct {
	Enabled     bool `json:"enabled"`
	DirectLogin bool `json:"direct_login"`
}
type oauthAuthorizeInfoResponse struct {
	User        *nanoflare.User `json:"user,omitempty"`
	ClientID    string          `json:"client_id,omitempty"`
	ClientName  string          `json:"client_name,omitempty"`
	RedirectURI string          `json:"redirect_uri,omitempty"`
	Scopes      []string        `json:"scopes,omitempty"`
}

func newOpenAPI(mux *http.ServeMux) huma.API {
	config := huma.DefaultConfig("Nanoflare Control API", "v1.0.0")
	config.OpenAPIPath = "/v1/openapi"
	config.DocsPath = "/v1/docs"
	config.OpenAPI.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"bearerAuth": {Type: "http", Scheme: "bearer", BearerFormat: "JWT or personal access token"},
	}
	return humago.New(mux, config)
}

func (s *Server) registerOpenAPIOperations() { documentPublicRoutes(s.openAPI) }

// OpenAPIJSON is used by the checked-in contract generator and intentionally
// shares exactly the same operation catalogue as the running server.
func OpenAPIJSON() ([]byte, error) {
	api := newOpenAPI(http.NewServeMux())
	documentPublicRoutes(api)
	return json.MarshalIndent(api.OpenAPI(), "", "  ")
}

func documentPublicRoutes(api huma.API) {
	for _, item := range publicOperations() {
		addOpenAPIOperation(api, item)
	}
}

func addOpenAPIOperation(api huma.API, item openAPIOperation) {
	op := &huma.Operation{Method: item.method, Path: strings.ReplaceAll(item.path, "...", ""), OperationID: item.id, Tags: []string{item.tag}, Summary: item.id, Responses: map[string]*huma.Response{}}
	for _, name := range pathValues(item.path) {
		op.Parameters = append(op.Parameters, &huma.Param{Name: name, In: "path", Required: true, Schema: &huma.Schema{Type: "string"}})
	}
	if !item.public {
		op.Security = []map[string][]string{{"bearerAuth": {}}}
	}
	for _, param := range queryParameters(item.id) {
		op.Parameters = append(op.Parameters, param)
	}
	if item.request != nil {
		op.RequestBody = &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{"application/json": {Schema: schemaFor(api, item.request)}}}
	}
	if item.status == http.StatusNoContent {
		op.Responses["204"] = &huma.Response{Description: "No content"}
	} else if item.response == (binaryBody{}) {
		op.Responses[statusKey(item.status)] = &huma.Response{Description: "Success", Content: map[string]*huma.MediaType{"application/octet-stream": {Schema: &huma.Schema{Type: "string", Format: "binary"}}}}
	} else {
		op.Responses[statusKey(item.status)] = &huma.Response{Description: "Success", Content: map[string]*huma.MediaType{"application/json": {Schema: schemaFor(api, item.response)}}}
	}
	if !item.public {
		op.Responses["401"] = errorResponse(api)
		op.Responses["403"] = errorResponse(api)
	}
	op.Responses["400"] = errorResponse(api)
	op.Responses["404"] = errorResponse(api)
	op.Responses["500"] = errorResponse(api)
	if item.id == "updateWorker" {
		op.Summary = "Update worker hostname and authentication"
		op.Description = "Updates only supplied fields. Hostnames are globally unique ASCII DNS names (including punycode), normalized to lowercase with surrounding whitespace and a trailing dot removed; empty or null values, IP addresses, schemes, paths, ports and wildcards are rejected. DNS and TLS must be configured externally. Worker identity, deployments, bindings, secrets and traffic allocation are preserved. Routing takes effect after Traefik refreshes its configuration. A 500 response can occur after the update was saved: GET the worker to reconcile its authoritative hostname and retry the same PATCH to complete routing refresh. Requests for the current hostname still refresh routing."
		op.Responses["409"] = errorResponse(api)
	}
	if item.id == "createWorkerDeployment" {
		op.Description = "An optional worker-scoped idempotency_key (or Idempotency-Key header) returns the original revision on identical retries without reactivation. A reused key with different content or a confirmed failed publication returns 409. Inspect the publication endpoint after an uncertain response. Immutable artifacts pin source, configuration, asset digests and sealed secret versions. Runtime readiness is verified only when the configured publisher supports prepare/commit; route_published confirms configuration publication, not edge propagation."
		op.Responses["409"] = errorResponse(api)
		op.Parameters = append(op.Parameters, &huma.Param{Name: "Idempotency-Key", In: "header", Schema: &huma.Schema{Type: "string", MaxLength: func() *int { n := 256; return &n }()}})
	}
	if item.id == "updateWorkerVars" {
		op.Summary = "Replace plain worker variables without uploading source or assets"
		op.Description = "Requires deployments:write in the worker organization. vars is a complete replacement object; omitted keys are removed, {} clears all plain variables, and missing/null is invalid. Preserves code, assets, secrets, compatibility settings, bindings and triggers. expected_deployment_id must identify the sole active revision receiving 100% of traffic; stale, inactive and split-traffic bases return 409. Every accepted new request creates a deployment with its caller marker. Identical requests with the same worker-scoped idempotency key return the original result, including after a later deployment, without reactivation. Reusing a key for a different normalized request returns 409. A 500 may follow activation: retry the identical request/key to recover the outcome. A confirmed failed rollout restores prior traffic/configuration; its key stays failed and a new key is required for another attempt. PostgreSQL persists outcomes across restarts; in-memory mode retains them only for the process lifetime. Success confirms publication of the new Worker revision; unrelated Workers keep running, and edge routing propagation may follow later."
		op.Responses["409"] = errorResponse(api)
	}
	api.OpenAPI().AddOperation(op)
}

func queryParameters(operationID string) []*huma.Param {
	query := func(name, format string) *huma.Param {
		return &huma.Param{Name: name, In: "query", Schema: &huma.Schema{Type: "string", Format: format}}
	}
	switch operationID {
	case "getWorkerOutput":
		return []*huma.Param{query("deployment_id", ""), query("level", ""), query("q", ""), {Name: "limit", In: "query", Schema: &huma.Schema{Type: "integer", Format: "int32"}}, query("since", "date-time"), query("until", "date-time")}
	case "getOAuthAuthorization":
		return []*huma.Param{query("client_id", ""), query("redirect_uri", "uri"), query("scope", "")}
	case "startOIDC", "logoutOIDC":
		return []*huma.Param{query("next", "")}
	case "startCLIOIDC":
		return []*huma.Param{query("oidc_code", "")}
	default:
		return nil
	}
}

type binaryBody struct{}

func schemaFor(api huma.API, value any) *huma.Schema {
	return api.OpenAPI().Components.Schemas.Schema(reflect.TypeOf(value), true, "")
}
func errorResponse(api huma.API) *huma.Response {
	return &huma.Response{Description: "Error", Content: map[string]*huma.MediaType{"application/json": {Schema: schemaFor(api, openAPIError{})}}}
}
func statusKey(status int) string { return strconv.Itoa(status) }
func pathValues(path string) []string {
	var names []string
	for _, part := range strings.Split(path, "/") {
		if strings.HasPrefix(part, "{") {
			name := strings.TrimSuffix(strings.TrimPrefix(part, "{"), "}")
			names = append(names, strings.TrimSuffix(name, "..."))
		}
	}
	return names
}
func needsOrganization(path string) bool {
	return !strings.HasPrefix(path, "/v1/auth/") && !strings.HasPrefix(path, "/v1/invites/") && path != "/v1/organizations" && path != "/v1/me/personal-access-tokens" && !strings.HasPrefix(path, "/v1/oauth/") && !strings.HasPrefix(path, "/v1/partner-")
}

func publicOperations() []openAPIOperation {
	return []openAPIOperation{
		{"GET", "/v1/organizations/{orgID}/dispatch-namespaces", "listDispatchNamespaces", "Dispatch namespaces", false, nil, []nanoflare.DispatchNamespace{}, 200},
		{"POST", "/v1/organizations/{orgID}/dispatch-namespaces", "createDispatchNamespace", "Dispatch namespaces", false, nanoflare.CreateDispatchNamespaceInput{}, nanoflare.DispatchNamespace{}, 201},
		{"GET", "/v1/organizations/{orgID}/dispatch-namespaces/{namespaceID}", "getDispatchNamespace", "Dispatch namespaces", false, nil, nanoflare.DispatchNamespace{}, 200},
		{"PATCH", "/v1/organizations/{orgID}/dispatch-namespaces/{namespaceID}", "renameDispatchNamespace", "Dispatch namespaces", false, nanoflare.RenameDispatchNamespaceInput{}, nanoflare.DispatchNamespace{}, 200},
		{"DELETE", "/v1/organizations/{orgID}/dispatch-namespaces/{namespaceID}", "deleteDispatchNamespace", "Dispatch namespaces", false, nil, nil, 204},
		{"PUT", "/v1/organizations/{orgID}/dispatch-namespaces/{namespaceID}/grants", "setNamespaceGrants", "Dispatch namespaces", false, nanoflare.NamespaceGrantsInput{}, nanoflare.DispatchNamespace{}, 200},
		{"GET", "/v1/organizations/{orgID}/dispatch-namespaces/{namespaceID}/policies", "listNamespacePolicies", "Dispatch namespaces", false, nil, []nanoflare.NamespacePolicy{}, 200},
		{"GET", "/v1/organizations/{orgID}/dispatch-namespaces/{namespaceID}/scripts", "listNamespaceScripts", "Dispatch namespaces", false, nil, []nanoflare.App{}, 200},
		{"POST", "/v1/organizations/{orgID}/dispatch-namespaces/{namespaceID}/scripts", "createNamespaceScript", "Dispatch namespaces", false, nanoflare.CreateNamespaceScriptInput{}, nanoflare.App{}, 201},
		{"GET", "/v1/organizations/{orgID}/dispatch-namespaces/{namespaceID}/scripts/{scriptName}", "getNamespaceScript", "Dispatch namespaces", false, nil, nanoflare.App{}, 200},
		{"DELETE", "/v1/organizations/{orgID}/dispatch-namespaces/{namespaceID}/scripts/{scriptName}", "deleteNamespaceScript", "Dispatch namespaces", false, nil, nil, 204},
		{"GET", "/v1/organizations/{orgID}/workers", "listWorkers", "Workers", false, nil, []nanoflare.App{}, 200}, {"POST", "/v1/organizations/{orgID}/workers", "createWorker", "Workers", false, nanoflare.CreateAppInput{}, nanoflare.App{}, 201}, {"GET", "/v1/organizations/{orgID}/workers/analytics", "getOrganizationWorkerMetrics", "Workers", false, nil, nanoflare.WorkerMetricsTimeseries{}, 200}, {"PATCH", "/v1/organizations/{orgID}/workers/{workerID}", "updateWorker", "Workers", false, nanoflare.UpdateAppInput{}, nanoflare.App{}, 200}, {"DELETE", "/v1/organizations/{orgID}/workers/{workerID}", "deleteWorker", "Workers", false, nil, nil, 204}, {"GET", "/v1/organizations/{orgID}/workers/{workerID}", "getWorker", "Workers", false, nil, nanoflare.WorkerDetail{}, 200},
		{"GET", "/v1/organizations/{orgID}/workers/{workerID}/capabilities", "getWorkerCapabilities", "Workers", false, nil, nanoflare.RuntimeCapabilities{}, 200},
		{"GET", "/v1/organizations/{orgID}/workers/{workerID}/publications", "listWorkerPublications", "Workers", false, nil, []nanoflare.PublicationResult{}, 200},
		{"GET", "/v1/organizations/{orgID}/workers/{workerID}/deployments/{deploymentID}/artifact", "getWorkerArtifact", "Workers", false, nil, nanoflare.ArtifactManifest{}, 200},
		{"GET", "/v1/organizations/{orgID}/workers/{workerID}/publications/{key}", "getWorkerPublication", "Workers", false, nil, nanoflare.PublicationResult{}, 200},
		{"GET", "/v1/organizations/{orgID}/dispatch-namespaces/{namespaceID}/scripts/{scriptName}/artifact", "resolveNamespaceArtifact", "Platforms", false, nil, nanoflare.ArtifactManifest{}, 200},
		{"GET", "/v1/organizations/{orgID}/workers/{workerID}/files", "listWorkerFiles", "Workers", false, nil, []nanoflare.WorkerFile{}, 200}, {"GET", "/v1/organizations/{orgID}/workers/{workerID}/output", "getWorkerOutput", "Workers", false, nil, []nanoflare.WorkerOutputLine{}, 200}, {"GET", "/v1/organizations/{orgID}/workers/{workerID}/analytics", "getWorkerMetrics", "Workers", false, nil, nanoflare.WorkerMetricsTimeseries{}, 200}, {"GET", "/v1/organizations/{orgID}/workers/{workerID}/deployments", "listWorkerDeployments", "Workers", false, nil, []nanoflare.ConsoleDeployment{}, 200}, {"PUT", "/v1/organizations/{orgID}/workers/{workerID}/deployment-traffic", "setWorkerDeploymentTraffic", "Workers", false, deploymentTrafficRequest{}, []nanoflare.ConsoleDeployment{}, 200}, {"POST", "/v1/organizations/{orgID}/workers/{workerID}/deployments", "createWorkerDeployment", "Workers", false, nanoflare.DeployInput{}, nanoflare.WorkerDeployment{}, 201}, {"PUT", "/v1/organizations/{orgID}/workers/{workerID}/vars", "updateWorkerVars", "Workers", false, nanoflare.UpdateVarsInput{}, nanoflare.VarsUpdateResult{}, 200}, {"GET", "/v1/organizations/{orgID}/workers/{workerID}/secrets", "listWorkerSecrets", "Workers", false, nil, []nanoflare.Secret{}, 200}, {"PUT", "/v1/organizations/{orgID}/workers/{workerID}/secrets/{name}", "putWorkerSecret", "Workers", false, nanoflare.PutSecretInput{}, nanoflare.Secret{}, 200}, {"DELETE", "/v1/organizations/{orgID}/workers/{workerID}/secrets/{name}", "deleteWorkerSecret", "Workers", false, nil, nil, 204},
		{"GET", "/v1/organizations/{orgID}/kv-namespaces", "listKVNamespaces", "KV", false, nil, []nanoflare.KVNamespace{}, 200}, {"GET", "/v1/organizations/{orgID}/kv-namespaces/metrics", "listKVNamespaceMetrics", "KV", false, nil, []nanoflare.KVNamespaceMetricsItem{}, 200}, {"POST", "/v1/organizations/{orgID}/kv-namespaces", "createKVNamespace", "KV", false, nanoflare.CreateKVNamespaceInput{}, nanoflare.KVNamespace{}, 201}, {"GET", "/v1/organizations/{orgID}/kv-namespaces/{namespaceID}", "getKVNamespace", "KV", false, nil, nanoflare.KVNamespace{}, 200}, {"PATCH", "/v1/organizations/{orgID}/kv-namespaces/{namespaceID}", "updateKVNamespace", "KV", false, nanoflare.UpdateKVNamespaceInput{}, nanoflare.KVNamespace{}, 200}, {"DELETE", "/v1/organizations/{orgID}/kv-namespaces/{namespaceID}", "deleteKVNamespace", "KV", false, nil, nil, 204}, {"GET", "/v1/organizations/{orgID}/kv-namespaces/{namespaceID}/analytics", "getKVNamespaceMetrics", "KV", false, nil, nanoflare.KVNamespaceMetricsTimeseries{}, 200}, {"GET", "/v1/organizations/{orgID}/kv-namespaces/{namespaceID}/values", "listKVNamespaceValues", "KV", false, nil, []nanoflare.WorkerKVKey{}, 200}, {"GET", "/v1/organizations/{orgID}/kv-namespaces/{namespaceID}/values/{key...}", "deleteKVNamespaceValue", "KV", false, nil, nil, 204},
		{"GET", "/v1/organizations/{orgID}/kv-namespaces/{namespaceID}/values/{key...}", "getKVNamespaceValue", "KV", false, nil, binaryBody{}, 200}, {"PUT", "/v1/organizations/{orgID}/kv-namespaces/{namespaceID}/values/{key...}", "putKVNamespaceValue", "KV", false, binaryBody{}, nil, 204},
		{"GET", "/v1/organizations/{orgID}/workers/{workerID}/kv-namespaces/{namespaceID}/values", "listWorkerKV", "KV", false, nil, []nanoflare.WorkerKVKey{}, 200}, {"GET", "/v1/organizations/{orgID}/workers/{workerID}/kv-namespaces/{namespaceID}/values/{key...}", "getWorkerKV", "KV", false, nil, binaryBody{}, 200}, {"PUT", "/v1/organizations/{orgID}/workers/{workerID}/kv-namespaces/{namespaceID}/values/{key...}", "putWorkerKV", "KV", false, binaryBody{}, nil, 204}, {"DELETE", "/v1/organizations/{orgID}/workers/{workerID}/kv-namespaces/{namespaceID}/values/{key...}", "deleteWorkerKV", "KV", false, nil, nil, 204},
		{"GET", "/v1/organizations/{orgID}/databases", "listDatabases", "Databases", false, nil, []nanoflare.Database{}, 200}, {"GET", "/v1/organizations/{orgID}/databases/metrics", "listDatabaseMetrics", "Databases", false, nil, []nanoflare.DatabaseMetricsItem{}, 200}, {"POST", "/v1/organizations/{orgID}/databases", "createDatabase", "Databases", false, nanoflare.CreateDatabaseInput{}, nanoflare.Database{}, 201}, {"DELETE", "/v1/organizations/{orgID}/databases/{databaseID}", "deleteDatabase", "Databases", false, nil, nil, 204}, {"GET", "/v1/organizations/{orgID}/databases/{databaseID}/analytics", "getDatabaseMetrics", "Databases", false, nil, nanoflare.DatabaseMetricsTimeseries{}, 200}, {"GET", "/v1/organizations/{orgID}/databases/{databaseID}/replication", "getDatabaseReplication", "Databases", false, nil, nanoflare.DatabaseReplicationStatus{}, 200}, {"POST", "/v1/organizations/{orgID}/databases/{databaseID}/restore", "restoreDatabase", "Databases", false, nanoflare.RestoreDatabaseInput{}, nanoflare.RestoreDatabaseResult{}, 200}, {"POST", "/v1/organizations/{orgID}/databases/{databaseID}/queries", "executeDatabase", "Databases", false, dbExecuteInput{}, nanoflare.DBQueryResponse{}, 200}, {"POST", "/v1/organizations/{orgID}/databases/{databaseID}/migrations", "applyDatabaseMigration", "Databases", false, dbExecuteInput{}, nanoflare.DBMigrationResult{}, 200},
		{"GET", "/v1/organizations/{orgID}/object-storage-buckets", "listObjectStorageBuckets", "Object storage", false, nil, []nanoflare.ObjectStorageBucket{}, 200}, {"GET", "/v1/organizations/{orgID}/object-storage-buckets/metrics", "listObjectStorageBucketMetrics", "Object storage", false, nil, []nanoflare.ObjectStorageBucketMetricsItem{}, 200}, {"POST", "/v1/organizations/{orgID}/object-storage-buckets", "createObjectStorageBucket", "Object storage", false, nanoflare.CreateObjectStorageBucketInput{}, nanoflare.ObjectStorageBucket{}, 201}, {"GET", "/v1/organizations/{orgID}/object-storage-buckets/{bucketID}", "getObjectStorageBucket", "Object storage", false, nil, nanoflare.ObjectStorageBucket{}, 200}, {"PATCH", "/v1/organizations/{orgID}/object-storage-buckets/{bucketID}", "updateObjectStorageBucket", "Object storage", false, nanoflare.UpdateObjectStorageBucketInput{}, nanoflare.ObjectStorageBucket{}, 200}, {"DELETE", "/v1/organizations/{orgID}/object-storage-buckets/{bucketID}", "deleteObjectStorageBucket", "Object storage", false, nil, nil, 204}, {"GET", "/v1/organizations/{orgID}/object-storage-buckets/{bucketID}/analytics", "getObjectStorageBucketMetrics", "Object storage", false, nil, nanoflare.ObjectStorageBucketMetricsTimeseries{}, 200},
		{"GET", "/v1/organizations/{orgID}/workers/{workerID}/object-storage-buckets/{bucketID}/objects", "listWorkerObjects", "Object storage", false, nil, []nanoflare.ObjectInfo{}, 200}, {"GET", "/v1/organizations/{orgID}/workers/{workerID}/object-storage-buckets/{bucketID}/objects/{key...}", "getWorkerObject", "Object storage", false, nil, binaryBody{}, 200}, {"PUT", "/v1/organizations/{orgID}/workers/{workerID}/object-storage-buckets/{bucketID}/objects/{key...}", "putWorkerObject", "Object storage", false, binaryBody{}, nanoflare.ObjectInfo{}, 200}, {"DELETE", "/v1/organizations/{orgID}/workers/{workerID}/object-storage-buckets/{bucketID}/objects/{key...}", "deleteWorkerObject", "Object storage", false, nil, nil, 204},
		{"POST", "/v1/auth/validate", "validateAuthToken", "Authentication", true, authTokenRequest{}, AuthResult{}, 200}, {"POST", "/v1/auth/userinfo", "getAuthUserInfo", "Authentication", true, authTokenRequest{}, authUserInfoResponse{}, 200},
		{"POST", "/v1/setup/signup", "setupSignup", "Authentication", true, nanoflare.SignupInput{}, nanoflare.AuthSession{}, 201}, {"POST", "/v1/auth/signup", "signup", "Authentication", true, nanoflare.SignupInput{}, nanoflare.AuthSession{}, 201}, {"POST", "/v1/auth/login", "login", "Authentication", true, nanoflare.LoginInput{}, nanoflare.AuthSession{}, 200}, {"POST", "/v1/auth/refresh", "refreshSession", "Authentication", true, authRefreshRequest{}, nanoflare.AuthSession{}, 200}, {"POST", "/v1/auth/pat/session", "createPATSession", "Authentication", true, patSessionRequest{}, nanoflare.AuthSession{}, 200}, {"GET", "/v1/auth/oidc/config", "getOIDCConfig", "Authentication", true, nil, oidcConfigResponse{}, 200}, {"GET", "/v1/auth/oidc/start", "startOIDC", "Authentication", true, nil, binaryBody{}, 200}, {"GET", "/v1/auth/oidc/callback", "completeOIDC", "Authentication", true, nil, binaryBody{}, 200}, {"GET", "/v1/auth/oidc/cli", "startCLIOIDC", "Authentication", true, nil, binaryBody{}, 200}, {"GET", "/v1/auth/oidc/logout", "logoutOIDC", "Authentication", true, nil, binaryBody{}, 200}, {"POST", "/v1/auth/oidc/session", "createOIDCSession", "Authentication", true, oidcSessionRequest{}, nanoflare.AuthSession{}, 200}, {"POST", "/v1/auth/cli/code", "createCLICode", "Authentication", false, nil, cliCodeResponse{}, 201}, {"POST", "/v1/auth/cli/session", "createCLISession", "Authentication", true, oidcSessionRequest{}, nanoflare.AuthSession{}, 200}, {"GET", "/v1/auth/me", "getCurrentUser", "Authentication", false, nil, nanoflare.AuthSession{}, 200},
		{"GET", "/v1/me/personal-access-tokens", "listPersonalAccessTokens", "Authentication", false, nil, []nanoflare.PersonalAccessToken{}, 200}, {"POST", "/v1/me/personal-access-tokens", "createPersonalAccessToken", "Authentication", false, nanoflare.CreatePersonalAccessTokenInput{}, nanoflare.PersonalAccessTokenCreated{}, 201}, {"DELETE", "/v1/me/personal-access-tokens/{patID}", "revokePersonalAccessToken", "Authentication", false, nil, nil, 204}, {"POST", "/v1/organizations", "createOrganization", "Organizations", false, nanoflare.CreateOrganizationInput{}, nanoflare.Organization{}, 201}, {"GET", "/v1/organizations/{orgID}/members", "listOrganizationMembers", "Organizations", false, nil, []nanoflare.OrganizationMembership{}, 200}, {"PATCH", "/v1/organizations/{orgID}/members/{userID}", "updateOrganizationMember", "Organizations", false, nanoflare.UpdateMembershipInput{}, nanoflare.OrganizationMembership{}, 200}, {"DELETE", "/v1/organizations/{orgID}/members/{userID}", "deleteOrganizationMember", "Organizations", false, nil, nil, 204}, {"POST", "/v1/organizations/{orgID}/invites", "createOrganizationInvite", "Organizations", false, nanoflare.CreateInviteInput{}, nanoflare.InviteCreated{}, 201}, {"GET", "/v1/organizations/{orgID}/invites", "listOrganizationInvites", "Organizations", false, nil, []nanoflare.OrganizationInvite{}, 200}, {"DELETE", "/v1/organizations/{orgID}/invites/{inviteID}", "revokeOrganizationInvite", "Organizations", false, nil, nil, 204}, {"GET", "/v1/invites/{token}", "getInvite", "Organizations", true, nil, nanoflare.OrganizationInvite{}, 200}, {"POST", "/v1/invites/{token}/accept", "acceptInvite", "Organizations", true, nanoflare.AcceptInviteInput{}, nanoflare.AcceptInviteResponse{}, 200},
		{"GET", "/v1/organizations/{orgID}/oauth-clients", "listOAuthClients", "OAuth", false, nil, []nanoflare.OAuthClient{}, 200}, {"POST", "/v1/organizations/{orgID}/oauth-clients", "createOAuthClient", "OAuth", false, nanoflare.CreateOAuthClientInput{}, nanoflare.OAuthClientCreated{}, 201}, {"GET", "/v1/organizations/{orgID}/oauth-clients/{clientID}", "getOAuthClient", "OAuth", false, nil, nanoflare.OAuthClient{}, 200}, {"GET", "/v1/organizations/{orgID}/oauth-clients/{clientID}/connections", "listOAuthClientConnections", "OAuth", false, nil, []nanoflare.OAuthClientConnection{}, 200}, {"PATCH", "/v1/organizations/{orgID}/oauth-clients/{clientID}", "updateOAuthClient", "OAuth", false, nanoflare.UpdateOAuthClientInput{}, nanoflare.OAuthClient{}, 200}, {"POST", "/v1/organizations/{orgID}/oauth-clients/{clientID}/client-secrets", "rotateOAuthClientSecret", "OAuth", false, nil, nanoflare.OAuthClientCreated{}, 200}, {"DELETE", "/v1/organizations/{orgID}/oauth-clients/{clientID}", "disableOAuthClient", "OAuth", false, nil, nil, 204}, {"GET", "/v1/oauth/authorize", "getOAuthAuthorization", "OAuth", true, nil, oauthAuthorizeInfoResponse{}, 200}, {"POST", "/v1/oauth/authorize", "authorizeOAuthClient", "OAuth", true, nanoflare.OAuthAuthorizeInput{}, nanoflare.OAuthAuthorizeResponse{}, 200}, {"POST", "/v1/oauth/token", "createOAuthToken", "OAuth", true, oauthTokenRequest{}, nanoflare.OAuthTokenResponse{}, 200}, {"POST", "/v1/oauth/revoke", "revokeOAuthToken", "OAuth", true, oauthRevokeRequest{}, nil, 204}, {"GET", "/v1/organizations/{orgID}/oauth-connections", "listOAuthConnections", "OAuth", false, nil, []nanoflare.OAuthConnection{}, 200}, {"DELETE", "/v1/organizations/{orgID}/oauth-connections/{clientID}", "disconnectOAuthClient", "OAuth", false, nil, nil, 204},
		{"GET", "/v1/organizations/{orgID}/partner-integrations", "listPartnerIntegrations", "Partners", false, nil, []nanoflare.PartnerIntegration{}, 200}, {"POST", "/v1/organizations/{orgID}/partner-integrations", "createPartnerIntegration", "Partners", false, nanoflare.CreatePartnerIntegrationInput{}, nanoflare.PartnerIntegrationCreated{}, 201}, {"POST", "/v1/organizations/{orgID}/partner-integrations/{integrationID}/client-secrets", "rotatePartnerIntegrationSecret", "Partners", false, nil, nanoflare.PartnerIntegrationCreated{}, 200}, {"DELETE", "/v1/organizations/{orgID}/partner-integrations/{integrationID}", "disablePartnerIntegration", "Partners", false, nil, nil, 204}, {"GET", "/v1/organizations/{orgID}/partner-integrations/{integrationID}/connections", "listPartnerConnections", "Partners", false, nil, []nanoflare.PartnerConnection{}, 200}, {"POST", "/v1/organizations/{orgID}/partner-integrations/{integrationID}/connections", "provisionPartnerConnection", "Partners", false, nanoflare.ProvisionPartnerConnectionInput{}, partnerConnectionResponse{}, 201}, {"DELETE", "/v1/organizations/{orgID}/partner-integrations/{integrationID}/connections/{connectionID}", "revokePartnerConnection", "Partners", false, nil, nil, 204}, {"POST", "/v1/organizations/{orgID}/partner-integrations/{integrationID}/token", "refreshPartnerConnection", "Partners", true, partnerRefreshRequest{}, partnerConnectionResponse{}, 200},
	}
}
