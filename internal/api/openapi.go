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
		if needsOrganization(item.path) {
			op.Parameters = append(op.Parameters, &huma.Param{Name: orgHeaderName, In: "header", Required: true, Schema: &huma.Schema{Type: "string"}})
		}
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
	return huma.SchemaFromType(api.OpenAPI().Components.Schemas, reflect.TypeOf(value))
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
	return !strings.HasPrefix(path, "/v1/auth/") && !strings.HasPrefix(path, "/v1/invites/") && path != "/v1/orgs" && path != "/v1/pats" && !strings.HasPrefix(path, "/v1/oauth/") && !strings.HasPrefix(path, "/v1/partner-")
}

func publicOperations() []openAPIOperation {
	return []openAPIOperation{
		{"GET", "/v1/workers", "listWorkers", "Workers", false, nil, []nanoflare.App{}, 200}, {"POST", "/v1/workers", "createWorker", "Workers", false, nanoflare.CreateAppInput{}, nanoflare.App{}, 201}, {"PATCH", "/v1/workers/{workerID}", "updateWorker", "Workers", false, nanoflare.UpdateAppInput{}, nanoflare.App{}, 200}, {"DELETE", "/v1/workers/{workerID}", "deleteWorker", "Workers", false, nil, nil, 204}, {"GET", "/v1/workers/{workerID}", "getWorker", "Workers", false, nil, nanoflare.WorkerDetail{}, 200},
		{"GET", "/v1/workers/{workerID}/files", "listWorkerFiles", "Workers", false, nil, []nanoflare.WorkerFile{}, 200}, {"GET", "/v1/workers/{workerID}/output", "getWorkerOutput", "Workers", false, nil, []nanoflare.WorkerOutputLine{}, 200}, {"GET", "/v1/workers/{workerID}/traffic", "getWorkerTraffic", "Workers", false, nil, nanoflare.WorkerTraffic{}, 200}, {"GET", "/v1/workers/{workerID}/deployments", "listWorkerDeployments", "Workers", false, nil, []nanoflare.WorkerDeployment{}, 200}, {"PUT", "/v1/workers/{workerID}/deployments/traffic", "setWorkerDeploymentTraffic", "Workers", false, deploymentTrafficRequest{}, []nanoflare.DeploymentTraffic{}, 200}, {"POST", "/v1/workers/{workerID}/deployments", "createWorkerDeployment", "Workers", false, nanoflare.DeployInput{}, nanoflare.WorkerDeployment{}, 201}, {"GET", "/v1/workers/{workerID}/secrets", "listWorkerSecrets", "Workers", false, nil, []nanoflare.Secret{}, 200}, {"PUT", "/v1/workers/{workerID}/secrets/{name}", "putWorkerSecret", "Workers", false, nanoflare.PutSecretInput{}, nanoflare.Secret{}, 200}, {"DELETE", "/v1/workers/{workerID}/secrets/{name}", "deleteWorkerSecret", "Workers", false, nil, nil, 204},
		{"GET", "/v1/kv/namespaces", "listKVNamespaces", "KV", false, nil, []nanoflare.KVNamespace{}, 200}, {"POST", "/v1/kv/namespaces", "createKVNamespace", "KV", false, nanoflare.CreateKVNamespaceInput{}, nanoflare.KVNamespace{}, 201}, {"GET", "/v1/kv/namespaces/{namespaceID}", "getKVNamespace", "KV", false, nil, nanoflare.KVNamespace{}, 200}, {"PATCH", "/v1/kv/namespaces/{namespaceID}", "updateKVNamespace", "KV", false, nanoflare.UpdateKVNamespaceInput{}, nanoflare.KVNamespace{}, 200}, {"DELETE", "/v1/kv/namespaces/{namespaceID}", "deleteKVNamespace", "KV", false, nil, nil, 204}, {"GET", "/v1/kv/namespaces/{namespaceID}/metrics", "getKVNamespaceMetrics", "KV", false, nil, nanoflare.KVNamespaceMetrics{}, 200},
		{"GET", "/v1/workers/{workerID}/kv/namespaces/{namespaceID}", "listWorkerKV", "KV", false, nil, []nanoflare.WorkerKVKey{}, 200}, {"GET", "/v1/workers/{workerID}/kv/namespaces/{namespaceID}/{key...}", "getWorkerKV", "KV", false, nil, binaryBody{}, 200}, {"PUT", "/v1/workers/{workerID}/kv/namespaces/{namespaceID}/{key...}", "putWorkerKV", "KV", false, binaryBody{}, nil, 204}, {"DELETE", "/v1/workers/{workerID}/kv/namespaces/{namespaceID}/{key...}", "deleteWorkerKV", "KV", false, nil, nil, 204},
		{"GET", "/v1/db", "listDatabases", "Databases", false, nil, []nanoflare.Database{}, 200}, {"POST", "/v1/db", "createDatabase", "Databases", false, nanoflare.CreateDatabaseInput{}, nanoflare.Database{}, 201}, {"DELETE", "/v1/db/{databaseID}", "deleteDatabase", "Databases", false, nil, nil, 204}, {"GET", "/v1/db/{databaseID}/metrics", "getDatabaseMetrics", "Databases", false, nil, nanoflare.DatabaseMetrics{}, 200}, {"GET", "/v1/db/{databaseID}/metrics/timeseries", "getDatabaseMetricsTimeseries", "Databases", false, nil, nanoflare.DatabaseMetricsTimeseries{}, 200}, {"POST", "/v1/db/{databaseID}/execute", "executeDatabase", "Databases", false, dbExecuteInput{}, nanoflare.DBQueryResponse{}, 200}, {"POST", "/v1/db/{databaseID}/migrations", "applyDatabaseMigration", "Databases", false, dbExecuteInput{}, nanoflare.DBMigrationResult{}, 200},
		{"GET", "/v1/object-storage-buckets", "listObjectStorageBuckets", "Object storage", false, nil, []nanoflare.ObjectStorageBucket{}, 200}, {"POST", "/v1/object-storage-buckets", "createObjectStorageBucket", "Object storage", false, nanoflare.CreateObjectStorageBucketInput{}, nanoflare.ObjectStorageBucket{}, 201}, {"GET", "/v1/object-storage-buckets/{bucketID}", "getObjectStorageBucket", "Object storage", false, nil, nanoflare.ObjectStorageBucket{}, 200}, {"PATCH", "/v1/object-storage-buckets/{bucketID}", "updateObjectStorageBucket", "Object storage", false, nanoflare.UpdateObjectStorageBucketInput{}, nanoflare.ObjectStorageBucket{}, 200}, {"DELETE", "/v1/object-storage-buckets/{bucketID}", "deleteObjectStorageBucket", "Object storage", false, nil, nil, 204}, {"GET", "/v1/object-storage-buckets/{bucketID}/metrics", "getObjectStorageBucketMetrics", "Object storage", false, nil, nanoflare.ObjectStorageBucketMetrics{}, 200},
		{"GET", "/v1/workers/{workerID}/object-storage-buckets/{bucketID}", "listWorkerObjects", "Object storage", false, nil, []nanoflare.ObjectInfo{}, 200}, {"GET", "/v1/workers/{workerID}/object-storage-buckets/{bucketID}/{key...}", "getWorkerObject", "Object storage", false, nil, binaryBody{}, 200}, {"PUT", "/v1/workers/{workerID}/object-storage-buckets/{bucketID}/{key...}", "putWorkerObject", "Object storage", false, binaryBody{}, nanoflare.ObjectInfo{}, 200}, {"DELETE", "/v1/workers/{workerID}/object-storage-buckets/{bucketID}/{key...}", "deleteWorkerObject", "Object storage", false, nil, nil, 204},
		{"POST", "/v1/auth/validate", "validateAuthToken", "Authentication", true, authTokenRequest{}, AuthResult{}, 200}, {"POST", "/v1/auth/userinfo", "getAuthUserInfo", "Authentication", true, authTokenRequest{}, authUserInfoResponse{}, 200},
		{"POST", "/v1/setup/signup", "setupSignup", "Authentication", true, nanoflare.SignupInput{}, nanoflare.AuthSession{}, 201}, {"POST", "/v1/auth/signup", "signup", "Authentication", true, nanoflare.SignupInput{}, nanoflare.AuthSession{}, 201}, {"POST", "/v1/auth/login", "login", "Authentication", true, nanoflare.LoginInput{}, nanoflare.AuthSession{}, 200}, {"POST", "/v1/auth/refresh", "refreshSession", "Authentication", true, authRefreshRequest{}, nanoflare.AuthSession{}, 200}, {"POST", "/v1/auth/pat/session", "createPATSession", "Authentication", true, patSessionRequest{}, nanoflare.AuthSession{}, 200}, {"GET", "/v1/auth/oidc/config", "getOIDCConfig", "Authentication", true, nil, oidcConfigResponse{}, 200}, {"GET", "/v1/auth/oidc/start", "startOIDC", "Authentication", true, nil, binaryBody{}, 200}, {"GET", "/v1/auth/oidc/callback", "completeOIDC", "Authentication", true, nil, binaryBody{}, 200}, {"GET", "/v1/auth/oidc/cli", "startCLIOIDC", "Authentication", true, nil, binaryBody{}, 200}, {"GET", "/v1/auth/oidc/logout", "logoutOIDC", "Authentication", true, nil, binaryBody{}, 200}, {"POST", "/v1/auth/oidc/session", "createOIDCSession", "Authentication", true, oidcSessionRequest{}, nanoflare.AuthSession{}, 200}, {"POST", "/v1/auth/cli/code", "createCLICode", "Authentication", false, nil, cliCodeResponse{}, 201}, {"POST", "/v1/auth/cli/session", "createCLISession", "Authentication", true, oidcSessionRequest{}, nanoflare.AuthSession{}, 200}, {"GET", "/v1/auth/me", "getCurrentUser", "Authentication", false, nil, nanoflare.AuthSession{}, 200},
		{"GET", "/v1/pats", "listPersonalAccessTokens", "Authentication", false, nil, []nanoflare.PersonalAccessToken{}, 200}, {"POST", "/v1/pats", "createPersonalAccessToken", "Authentication", false, nanoflare.CreatePersonalAccessTokenInput{}, nanoflare.PersonalAccessTokenCreated{}, 201}, {"DELETE", "/v1/pats/{patID}", "revokePersonalAccessToken", "Authentication", false, nil, nil, 204}, {"POST", "/v1/orgs", "createOrganization", "Organizations", false, nanoflare.CreateOrganizationInput{}, nanoflare.Organization{}, 201}, {"GET", "/v1/orgs/{orgID}/members", "listOrganizationMembers", "Organizations", false, nil, []nanoflare.OrganizationMembership{}, 200}, {"PATCH", "/v1/orgs/{orgID}/members/{userID}", "updateOrganizationMember", "Organizations", false, nanoflare.UpdateMembershipInput{}, nanoflare.OrganizationMembership{}, 200}, {"DELETE", "/v1/orgs/{orgID}/members/{userID}", "deleteOrganizationMember", "Organizations", false, nil, nil, 204}, {"POST", "/v1/orgs/{orgID}/invites", "createOrganizationInvite", "Organizations", false, nanoflare.CreateInviteInput{}, nanoflare.InviteCreated{}, 201}, {"GET", "/v1/orgs/{orgID}/invites", "listOrganizationInvites", "Organizations", false, nil, []nanoflare.OrganizationInvite{}, 200}, {"DELETE", "/v1/orgs/{orgID}/invites/{inviteID}", "revokeOrganizationInvite", "Organizations", false, nil, nil, 204}, {"GET", "/v1/invites/{token}", "getInvite", "Organizations", true, nil, nanoflare.OrganizationInvite{}, 200}, {"POST", "/v1/invites/{token}/accept", "acceptInvite", "Organizations", true, nanoflare.AcceptInviteInput{}, nanoflare.AcceptInviteResponse{}, 200},
		{"GET", "/v1/oauth/clients", "listOAuthClients", "OAuth", false, nil, []nanoflare.OAuthClient{}, 200}, {"POST", "/v1/oauth/clients", "createOAuthClient", "OAuth", false, nanoflare.CreateOAuthClientInput{}, nanoflare.OAuthClientCreated{}, 201}, {"GET", "/v1/oauth/clients/{clientID}", "getOAuthClient", "OAuth", false, nil, nanoflare.OAuthClient{}, 200}, {"GET", "/v1/oauth/clients/{clientID}/connections", "listOAuthClientConnections", "OAuth", false, nil, []nanoflare.OAuthClientConnection{}, 200}, {"PATCH", "/v1/oauth/clients/{clientID}", "updateOAuthClient", "OAuth", false, nanoflare.UpdateOAuthClientInput{}, nanoflare.OAuthClient{}, 200}, {"POST", "/v1/oauth/clients/{clientID}/secret", "rotateOAuthClientSecret", "OAuth", false, nil, nanoflare.OAuthClientCreated{}, 200}, {"POST", "/v1/oauth/clients/{clientID}/restore", "restoreOAuthClient", "OAuth", false, nil, nanoflare.OAuthClient{}, 200}, {"DELETE", "/v1/oauth/clients/{clientID}", "disableOAuthClient", "OAuth", false, nil, nil, 204}, {"GET", "/v1/oauth/authorize", "getOAuthAuthorization", "OAuth", true, nil, oauthAuthorizeInfoResponse{}, 200}, {"POST", "/v1/oauth/authorize", "authorizeOAuthClient", "OAuth", true, nanoflare.OAuthAuthorizeInput{}, nanoflare.OAuthAuthorizeResponse{}, 200}, {"POST", "/v1/oauth/token", "createOAuthToken", "OAuth", true, oauthTokenRequest{}, nanoflare.OAuthTokenResponse{}, 200}, {"POST", "/v1/oauth/revoke", "revokeOAuthToken", "OAuth", true, oauthRevokeRequest{}, nil, 204}, {"GET", "/v1/oauth/connections", "listOAuthConnections", "OAuth", false, nil, []nanoflare.OAuthConnection{}, 200}, {"DELETE", "/v1/oauth/connections/{clientID}", "disconnectOAuthClient", "OAuth", false, nil, nil, 204},
		{"GET", "/v1/partner-integrations", "listPartnerIntegrations", "Partners", false, nil, []nanoflare.PartnerIntegration{}, 200}, {"POST", "/v1/partner-integrations", "createPartnerIntegration", "Partners", false, nanoflare.CreatePartnerIntegrationInput{}, nanoflare.PartnerIntegrationCreated{}, 201}, {"POST", "/v1/partner-integrations/{integrationID}/secret", "rotatePartnerIntegrationSecret", "Partners", false, nil, nanoflare.PartnerIntegrationCreated{}, 200}, {"DELETE", "/v1/partner-integrations/{integrationID}", "disablePartnerIntegration", "Partners", false, nil, nil, 204}, {"GET", "/v1/partner-integrations/{integrationID}/connections", "listPartnerConnections", "Partners", false, nil, []nanoflare.PartnerConnection{}, 200}, {"POST", "/v1/partner-integrations/{integrationID}/connections", "provisionPartnerConnection", "Partners", false, nanoflare.ProvisionPartnerConnectionInput{}, partnerConnectionResponse{}, 201}, {"DELETE", "/v1/partner-integrations/{integrationID}/connections/{connectionID}", "revokePartnerConnection", "Partners", false, nil, nil, 204}, {"POST", "/v1/partner-connections/token", "refreshPartnerConnection", "Partners", true, partnerRefreshRequest{}, partnerConnectionResponse{}, 200},
	}
}
