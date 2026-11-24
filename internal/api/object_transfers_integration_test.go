package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/objects"
)

func TestObjectTransferRuntimeAndQuota(t *testing.T) {
	endpoint := os.Getenv("NANOFLARE_TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("NANOFLARE_TEST_MINIO_ENDPOINT is not configured")
	}
	ctx := context.Background()
	repo := runtimePostgres(t)
	storage, err := objects.Open(ctx, objects.MinIOConfig{
		Endpoint: endpoint, PublicEndpoint: "http://" + endpoint,
		Bucket:          os.Getenv("NANOFLARE_TEST_MINIO_BUCKET"),
		AccessKey:       os.Getenv("NANOFLARE_TEST_MINIO_ACCESS_KEY"),
		SecretKey:       os.Getenv("NANOFLARE_TEST_MINIO_SECRET_KEY"),
		UploadAccessKey: os.Getenv("NANOFLARE_TEST_MINIO_UPLOAD_ACCESS_KEY"),
		UploadSecretKey: os.Getenv("NANOFLARE_TEST_MINIO_UPLOAD_SECRET_KEY"),
	})
	if err != nil {
		t.Fatal(err)
	}
	const org = "object-transfer-test"
	if err := repo.CreateOrganization(nanoflare.Organization{ID: org, Name: org, UsageLevel: nanoflare.UsageLevelPaid}); err != nil {
		t.Fatal(err)
	}
	service := nanoflare.NewServiceWithObjects(repo, discardWriter{}, storage)
	service.SetPlatformDispatchEnabled(true)
	controlAuth := nanoflare.NewControlAuthService(repo, "object-transfer-test")
	controlAuth.SetTestHashCost(4)
	control := newTestServerWithRuntimeAndOAuth(service, nil, "", nil, controlAuth, nanoflare.NewOAuthService(repo), nil)
	session := signupControlUser(t, control)
	if err := repo.UpsertOrganizationMembership(nanoflare.OrganizationMembership{
		UserID: session.User.ID, OrgID: org, Role: nanoflare.RoleOwner,
		Scopes: nanoflare.RoleScopes(nanoflare.RoleOwner), CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	scopes := []string{"objects:read", "objects:write"}
	client := createOAuthClient(t, control, session.Token, org, scopes)
	token := authorizeOAuthClient(t, control, session.Token, org, client, scopes)
	withToken := func(token string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
			control.ServeHTTP(w, r)
		})
	}
	asServiceAccount := withToken(token.AccessToken)
	bucketBase := "/v1/organizations/" + org + "/object-storage-buckets"
	var bucket nanoflare.ObjectStorageBucket
	requestJSONBytes(t, asServiceAccount, http.MethodPost, bucketBase, []byte(`{"name":"quota"}`), http.StatusCreated, &bucket)
	bucketURL := bucketBase + "/" + bucket.ID
	otherOrgURL := "/v1/organizations/" + session.ActiveOrgID + "/object-storage-buckets/" + bucket.ID
	requestJSONBytes(t, asServiceAccount, http.MethodPatch, otherOrgURL, []byte(`{"max_bytes":5}`), http.StatusForbidden, nil)
	requestJSONBytes(t, withToken(session.Token), http.MethodPatch, otherOrgURL, []byte(`{"max_bytes":5}`), http.StatusNotFound, nil)
	var updated nanoflare.ObjectStorageBucket
	for range 2 {
		requestJSONBytes(t, asServiceAccount, http.MethodPatch, bucketURL, []byte(`{"max_bytes":5}`), http.StatusOK, &updated)
	}
	requestJSON(t, asServiceAccount, http.MethodGet, bucketURL, http.StatusOK, &updated)
	if updated.MaxBytes == nil || *updated.MaxBytes != 5 {
		t.Fatalf("PATCH response = %#v", updated)
	}
	ns, err := service.CreateDispatchNamespace(org, nanoflare.CreateDispatchNamespaceInput{Name: "transfer", CustomerMode: "platform"})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := service.CreateNamespaceScript(org, ns.ID, nanoflare.CreateNamespaceScriptInput{ScriptName: "site", CustomerID: "site", Execution: "dynamic"})
	if err != nil {
		t.Fatal(err)
	}
	ns, err = service.SetNamespaceGrants(org, ns.ID, nanoflare.NamespaceGrantsInput{ExpectedRevision: 1, Grants: []nanoflare.NamespaceResourceGrant{
		{Kind: "objects", ResourceID: bucket.ID, CustomerID: "site"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	input := nanoflare.DeployInput{
		Files:  []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}},
		Format: "modules", CompatibilityDate: "2025-12-10",
		ObjectStorageBuckets: []nanoflare.ObjectStorageBucketBinding{{Binding: "OBJECTS", BucketID: bucket.ID}},
	}
	if _, err := service.Deploy(worker.ID, input); err != nil {
		t.Fatal(err)
	}
	host, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: org, Name: "host", Hostname: "transfer.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	input.ObjectStorageBuckets = nil
	input.DispatchNamespaces = []nanoflare.DispatchBinding{{Binding: "DISPATCHER", NamespaceID: ns.ID}}
	hostDeployment, err := service.Deploy(host.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	hostToken := nanoflare.RuntimeCapability(nanoflare.ActiveDeployment{App: host, Deployment: hostDeployment}, nanoflare.RuntimeDispatch, ns.ID)
	invocation, err := service.AcquireDispatch(ctx, hostToken, ns.ID, "site")
	if err != nil {
		t.Fatal(err)
	}
	defer service.FinishDispatch(ctx, hostToken, invocation.Lease, true)
	broker := NewRuntimeDispatchServer(service, control.Server)
	objectCall := func(binding, effect string, input map[string]any, status int, output any) {
		t.Helper()
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/internal/runtime/dispatch/effect/objects/"+binding+"/internal/runtime/objects/"+effect, bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+hostToken)
		req.Header.Set("X-Nanoflare-Invocation", invocation.Lease)
		res := httptest.NewRecorder()
		broker.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("upload API = %d, want %d, body = %s", res.Code, status, res.Body.String())
		}
		if output != nil {
			if err := json.Unmarshal(res.Body.Bytes(), output); err != nil {
				t.Fatal(err)
			}
		}
	}
	uploadCall := func(input map[string]any, status int, output any) {
		t.Helper()
		objectCall("OBJECTS", "upload-session", input, status, output)
	}
	objectCall("OTHER", "upload-session", map[string]any{"action": "create", "key": "wrong", "size": 5}, http.StatusForbidden, nil)
	var ticket nanoflare.ObjectTransferTicket
	uploadCall(map[string]any{"action": "create", "key": "published", "size": 5}, http.StatusOK, &ticket)
	defer func() {
		_ = storage.Delete("object-storage-buckets", "buckets/"+bucket.ID+"/published")
		_ = storage.CleanupObjectTransfer(ctx, nanoflare.ObjectTransfer{ID: ticket.ID})
	}()
	other := nanoflare.NewServiceWithObjects(repo, discardWriter{}, storage)
	other.SetPlatformDispatchEnabled(true)
	otherInvocation, err := other.AcquireDispatch(ctx, hostToken, ns.ID, "site")
	if err != nil {
		t.Fatal(err)
	}
	defer other.FinishDispatch(ctx, hostToken, otherInvocation.Lease, true)
	got, err := other.GetObjectStorageBucketForOrg(org, bucket.ID)
	if err != nil || got.MaxBytes == nil || *got.MaxBytes != 5 {
		t.Fatalf("persisted bucket quota = %#v, %v", got, err)
	}
	if _, err := other.ObjectPut(otherInvocation.Lease, bucket.ID, "other", "text/plain", []byte("x")); !errors.Is(err, nanoflare.ErrUsageLimitExceeded) {
		t.Fatalf("write while bytes reserved = %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, ticket.URL, strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("If-None-Match", "*")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("storage PUT = %d", resp.StatusCode)
	}
	for range 2 {
		var object nanoflare.ObjectInfo
		uploadCall(map[string]any{"action": "complete", "id": ticket.ID}, http.StatusOK, &object)
		if object.Size != 5 || object.Key != "published" {
			t.Fatalf("completion = %#v", object)
		}
	}
	var download struct {
		URL string `json:"url"`
	}
	objectCall("OBJECTS", "presign-public-download", map[string]any{"key": "published"}, http.StatusOK, &download)
	if !strings.HasPrefix(download.URL, "http://"+endpoint+"/") {
		t.Fatalf("download URL points outside public endpoint: %q", download.URL)
	}
	get := httptest.NewRequest(http.MethodGet, "/internal/runtime/dispatch/effect/objects/OBJECTS/internal/runtime/objects/published", nil)
	get.Header.Set("Authorization", "Bearer "+hostToken)
	get.Header.Set("X-Nanoflare-Invocation", invocation.Lease)
	stream := httptest.NewRecorder()
	broker.ServeHTTP(stream, get)
	if stream.Code != http.StatusOK || stream.Body.String() != "hello" {
		t.Fatalf("streamed GET = %d %q", stream.Code, stream.Body.String())
	}
	if _, err := service.ObjectPut(invocation.Lease, bucket.ID, "other", "text/plain", []byte("x")); !errors.Is(err, nanoflare.ErrUsageLimitExceeded) {
		t.Fatalf("write while bytes committed = %v", err)
	}
	if err := service.DeleteObject(invocation.Lease, bucket.ID, "published"); err != nil {
		t.Fatal(err)
	}
	var aborted nanoflare.ObjectTransferTicket
	uploadCall(map[string]any{"action": "create", "key": "aborted", "size": 5}, http.StatusOK, &aborted)
	uploadCall(map[string]any{"action": "abort", "id": aborted.ID}, http.StatusOK, nil)
	if _, err := service.ObjectPut(invocation.Lease, bucket.ID, "published", "text/plain", []byte("hello")); err != nil {
		t.Fatalf("write after abort = %v", err)
	}
}
