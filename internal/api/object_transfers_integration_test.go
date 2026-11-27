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
	"sync/atomic"
	"testing"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/clas/nanoflare/internal/objects"
)

type countedObjectUsage struct {
	*objects.MinIO
	listings       atomic.Int64
	lostResponse   atomic.Bool
	rejectPut      atomic.Bool
	cancelPut      context.CancelFunc
	putStarted     chan struct{}
	resumePut      chan struct{}
	listingStarted chan struct{}
	resumeListing  chan struct{}
}

func (s *countedObjectUsage) UsageContext(ctx context.Context, scope, prefix string) (int64, int64, error) {
	s.listings.Add(1)
	if s.listingStarted != nil {
		close(s.listingStarted)
		select {
		case <-ctx.Done():
			return 0, 0, ctx.Err()
		case <-s.resumeListing:
		}
	}
	return s.MinIO.UsageContext(ctx, scope, prefix)
}

func (s *countedObjectUsage) PutContext(ctx context.Context, scope, key, contentType string, data []byte) (nanoflare.ObjectInfo, error) {
	if s.putStarted != nil && strings.HasSuffix(key, "/slow-put") {
		close(s.putStarted)
		select {
		case <-ctx.Done():
			return nanoflare.ObjectInfo{}, ctx.Err()
		case <-s.resumePut:
		}
	}
	if strings.HasSuffix(key, "/lost-put") && s.lostResponse.CompareAndSwap(true, false) {
		s.cancelPut()
		if _, err := s.MinIO.PutContext(ctx, scope, key, contentType, data); err != nil {
			return nanoflare.ObjectInfo{}, err
		}
		return nanoflare.ObjectInfo{}, context.Canceled
	}
	if strings.HasSuffix(key, "/rejected-put") && s.rejectPut.CompareAndSwap(true, false) {
		return nanoflare.ObjectInfo{}, nanoflare.ErrObjectPutRejected
	}
	return s.MinIO.PutContext(ctx, scope, key, contentType, data)
}

func TestBucketDeletionAbortsUnfinishedDirectUploads(t *testing.T) {
	repo := runtimePostgres(t)
	transfers := repo.(interface {
		SaveObjectTransfer(nanoflare.ObjectTransfer) error
		ListObjectTransfers(bucketID, orgID, includeID string) ([]nanoflare.ObjectTransfer, error)
		DeleteObjectTransfer(string) error
	})
	service := nanoflare.NewService(repo, discardWriter{})
	server := newTestServer(service)
	const org = "test-org"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(repo.CreateOrganization(nanoflare.Organization{ID: org, Name: org, UsageLevel: nanoflare.UsageLevelPaid}))

	const bucketsURL = "/v1/organizations/" + org + "/object-storage-buckets"
	var bucket, other nanoflare.ObjectStorageBucket
	requestJSONBytes(t, server, http.MethodPost, bucketsURL, []byte(`{"name":"abandoned"}`), http.StatusCreated, &bucket)
	requestJSONBytes(t, server, http.MethodPost, bucketsURL, []byte(`{"name":"other"}`), http.StatusCreated, &other)
	bucketURL := bucketsURL + "/" + bucket.ID
	worker, err := service.CreateApp(nanoflare.CreateAppInput{OrgID: org, Name: "uploader", Hostname: "uploader.example.test"})
	must(err)
	_, err = service.Deploy(worker.ID, nanoflare.DeployInput{
		Files:                []nanoflare.WorkerFile{{Path: "worker.js", Content: "export default {}"}},
		CompatibilityDate:    "2025-12-10",
		ObjectStorageBuckets: []nanoflare.ObjectStorageBucketBinding{{Binding: "OBJECTS", BucketID: bucket.ID}},
	})
	must(err)

	now := time.Now().UTC()
	for _, transfer := range []nanoflare.ObjectTransfer{
		{ID: "single", BucketID: bucket.ID, ExpiresAt: now.Add(-30 * time.Minute)},
		{ID: "multipart", BucketID: bucket.ID, ExpiresAt: now.Add(24 * time.Hour), DirectMultipart: true, UploadID: "provider-id"},
		{ID: "completed", BucketID: bucket.ID, ExpiresAt: now.Add(time.Hour), Result: &nanoflare.ObjectInfo{Size: 1}},
		{ID: "other", BucketID: other.ID, ExpiresAt: now.Add(time.Hour)},
	} {
		transfer.OrgID = org
		transfer.Key = "buckets/" + transfer.BucketID + "/" + transfer.ID
		must(transfers.SaveObjectTransfer(transfer))
	}
	checkAborted := func(deleted bool) {
		t.Helper()
		rows, err := transfers.ListObjectTransfers(bucket.ID, org, "")
		must(err)
		if len(rows) != 4 {
			t.Fatalf("transfer count = %d, want 4", len(rows))
		}
		for _, transfer := range rows {
			want := deleted && (transfer.ID == "single" || transfer.ID == "multipart")
			if transfer.Aborted != want || transfer.Cleaned {
				t.Errorf("transfer %s: aborted = %t, cleaned = %t, want aborted = %t and pending cleanup", transfer.ID, transfer.Aborted, transfer.Cleaned, want)
			}
		}
	}
	requestJSON(t, server, http.MethodDelete, bucketURL, http.StatusConflict, nil)
	checkAborted(false)
	must(service.DeleteApp(worker.ID))

	// A claim can outlive its expiry while an ambiguous provider write is being recovered.
	must(transfers.SaveObjectTransfer(nanoflare.ObjectTransfer{
		ID: "claim", BucketID: bucket.ID, OrgID: org, Key: "buckets/" + bucket.ID + "/claim",
		Buffered: true, ExpiresAt: now.Add(-time.Minute),
	}))
	requestJSON(t, server, http.MethodDelete, bucketURL, http.StatusConflict, nil)
	must(transfers.DeleteObjectTransfer("claim"))
	checkAborted(false)

	requestJSON(t, server, http.MethodDelete, bucketURL, http.StatusNoContent, nil)
	requestJSON(t, server, http.MethodDelete, bucketURL, http.StatusNotFound, nil)
	checkAborted(true)
}

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
	provider := &countedObjectUsage{MinIO: storage}
	service := nanoflare.NewServiceWithObjects(repo, discardWriter{}, provider)
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
	other := nanoflare.NewServiceWithObjects(repo, discardWriter{}, provider)
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
	if got := provider.listings.Load(); got != 0 {
		t.Fatalf("provider listings = %d, want none for a newly provisioned bucket", got)
	}
	if _, err := repo.(interface {
		BeginObjectUsageChange(context.Context, string) (bool, error)
	}).BeginObjectUsageChange(ctx, bucket.ID); err != nil {
		t.Fatal(err)
	}
	provider.listingStarted, provider.resumeListing = make(chan struct{}), make(chan struct{})
	stopReconcile := service.StartObjectUsageReconciliation(ctx, func(err error) {
		t.Errorf("background reconciliation: %v", err)
	})
	defer stopReconcile()
	select {
	case <-provider.listingStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("background reconciliation did not begin")
	}
	retryCtx, cancelRetry := context.WithTimeout(ctx, time.Second)
	_, err = other.ObjectPutContext(retryCtx, otherInvocation.Lease, bucket.ID, "during-rebuild", "text/plain", []byte("x"))
	cancelRetry()
	close(provider.resumeListing)
	if !errors.Is(err, nanoflare.ErrObjectUsageRebuilding) {
		t.Fatalf("growth during reconciliation = %v, want retryable error", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := other.ObjectPut(otherInvocation.Lease, bucket.ID, "during-rebuild", "text/plain", []byte("x"))
		if errors.Is(err, nanoflare.ErrUsageLimitExceeded) {
			break
		}
		if !errors.Is(err, nanoflare.ErrObjectUsageRebuilding) || time.Now().After(deadline) {
			t.Fatalf("growth after reconciliation = %v, want quota limit", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	stopReconcile()

	slow := make(chan error, 1)
	provider.putStarted, provider.resumePut = make(chan struct{}), make(chan struct{})
	limit := int64(20)
	if _, err := service.UpdateObjectStorageBucketForOrg(org, bucket.ID, nanoflare.UpdateObjectStorageBucketInput{MaxBytes: &limit}); err != nil {
		t.Fatal(err)
	}
	putCtx, cancelPut := context.WithCancel(ctx)
	defer cancelPut()
	provider.cancelPut = cancelPut
	provider.lostResponse.Store(true)
	defer storage.Delete("object-storage-buckets", "buckets/"+bucket.ID+"/lost-put")
	if _, err := service.ObjectPutContext(putCtx, invocation.Lease, bucket.ID, "lost-put", "text/plain", []byte("lost")); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost PUT response = %v", err)
	}
	metrics, err := service.ObjectStorageBucketMetrics(bucket.ID)
	if err != nil || metrics.Size != 9 || metrics.ObjectCount != 2 {
		t.Fatalf("usage after committed PUT with lost response = %#v, %v", metrics, err)
	}
	if _, err := service.ObjectPut(invocation.Lease, bucket.ID, "lost-put", "text/plain", []byte("ok")); err != nil {
		t.Fatalf("retry after lost response = %v", err)
	}
	provider.rejectPut.Store(true)
	defer storage.Delete("object-storage-buckets", "buckets/"+bucket.ID+"/rejected-put")
	if _, err := service.ObjectPut(invocation.Lease, bucket.ID, "rejected-put", "text/plain", []byte("no")); !errors.Is(err, nanoflare.ErrObjectPutRejected) {
		t.Fatalf("rejected PUT = %v", err)
	}
	if _, err := service.ObjectPut(invocation.Lease, bucket.ID, "rejected-put", "text/plain", []byte("ok")); err != nil {
		t.Fatalf("retry after definitive rejection = %v", err)
	}

	go func() {
		_, err := service.ObjectPut(invocation.Lease, bucket.ID, "slow-put", "text/plain", []byte("one"))
		slow <- err
	}()
	defer func() {
		close(provider.resumePut)
		if err := <-slow; err != nil {
			t.Errorf("slow PUT: %v", err)
		}
		_ = storage.Delete("object-storage-buckets", "buckets/"+bucket.ID+"/slow-put")
	}()
	select {
	case <-provider.putStarted:
	case err := <-slow:
		slow <- err
		t.Fatalf("PUT exited before reaching provider: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("PUT did not reach provider")
	}
	defer storage.Delete("object-storage-buckets", "buckets/"+bucket.ID+"/fast-put")
	quick, stopQuick := context.WithTimeout(ctx, 3*time.Second)
	defer stopQuick()
	if _, err := other.ObjectPutContext(quick, otherInvocation.Lease, bucket.ID, "fast-put", "text/plain", []byte("two")); err != nil {
		t.Fatalf("same-org PUT blocked behind a slow provider call: %v", err)
	}

}
