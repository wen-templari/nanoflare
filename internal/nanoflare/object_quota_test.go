package nanoflare

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestObjectBucketQuotaUsesExistingBytesAndSerializesWrites(t *testing.T) {
	store := newObjectBackedRepo()
	service := NewServiceWithObjects(store, &recordingWriter{}, newMemoryObjectStore())
	org := Organization{ID: "quota-org", Name: "Quota Org", UsageLevel: UsageLevelPaid}
	if err := store.CreateOrganization(org); err != nil {
		t.Fatal(err)
	}
	bucket, err := service.CreateObjectStorageBucket(CreateObjectStorageBucketInput{Name: "objects", OrgID: org.ID})
	if err != nil || bucket.MaxBytes != nil {
		t.Fatalf("legacy bucket = %#v, %v", bucket, err)
	}
	app, err := service.CreateApp(CreateAppInput{Name: "Quota App", OrgID: org.ID, Hostname: "quota.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Deploy(app.ID, DeployInput{
		Files:                []WorkerFile{{Path: "worker.js", Content: "export default {}"}},
		CompatibilityDate:    "2025-12-10",
		ObjectStorageBuckets: []ObjectStorageBucketBinding{{Binding: "OBJECTS", BucketID: bucket.ID}},
	}); err != nil {
		t.Fatal(err)
	}
	put := func(key, value string) error {
		_, err := service.ObjectPut(app.RuntimeToken, bucket.ID, key, "text/plain", []byte(value))
		return err
	}
	if err := put("existing", "abcd"); err != nil {
		t.Fatal(err)
	}
	maxBytes := int64(3)
	input := UpdateObjectStorageBucketInput{MaxBytes: &maxBytes}
	if _, err := service.UpdateObjectStorageBucketForOrg("another-org", bucket.ID, input); !errors.Is(err, ErrObjectStorageBucketNotFound) {
		t.Fatalf("cross-org update = %v", err)
	}
	for range 2 {
		got, err := service.UpdateObjectStorageBucketForOrg(org.ID, bucket.ID, input)
		if err != nil || got.MaxBytes == nil || *got.MaxBytes != maxBytes || got.Name != bucket.Name {
			t.Fatalf("quota update = %#v, %v", got, err)
		}
	}
	got, err := service.GetObjectStorageBucketForOrg(org.ID, bucket.ID)
	if err != nil || got.MaxBytes == nil || *got.MaxBytes != maxBytes {
		t.Fatalf("bucket read = %#v, %v", got, err)
	}
	if err := put("new", "a"); !errors.Is(err, ErrUsageLimitExceeded) {
		t.Fatalf("growth over an already-exceeded limit = %v", err)
	}
	if err := put("existing", "ab"); err != nil {
		t.Fatalf("smaller replacement = %v", err)
	}
	const writers = 8
	results := make(chan error, writers)
	for i := range writers {
		go func() { results <- put(fmt.Sprintf("concurrent-%d", i), "a") }()
	}
	var success int
	for range writers {
		if err := <-results; err == nil {
			success++
		} else if !errors.Is(err, ErrUsageLimitExceeded) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("successful writes = %d, want 1", success)
	}
	if err := service.DeleteObject(app.RuntimeToken, bucket.ID, "existing"); err != nil {
		t.Fatal(err)
	}
	if err := put("replacement", "de"); err != nil {
		t.Fatalf("write after deletion = %v", err)
	}
}

func TestObjectUploadWindowAndCompletionGrace(t *testing.T) {
	if got := objectUploadWindow(5 << 30); got != 5*time.Hour {
		t.Fatalf("5 GiB upload window = %v", got)
	}
	expiry := time.Now()
	transfer := ObjectTransfer{ExpiresAt: expiry}
	if !transfer.active(expiry.Add(30*time.Minute)) || transfer.active(expiry.Add(time.Hour)) {
		t.Fatal("completion should be allowed for one hour after URL expiry")
	}
	transfer.Aborted = true
	if !transfer.holdsReservation() {
		t.Fatal("failed abort cleanup released its staging reservation")
	}
	transfer.Cleaned = true
	if transfer.holdsReservation() {
		t.Fatal("successful abort cleanup retained its staging reservation")
	}
}
