package nanoflare

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type objectStorageUpdateLocker interface {
	LockObjectStorageUpdates(context.Context, string) (func(), error)
}

func (s *Store) LockObjectStorageUpdates(ctx context.Context, _ string) (func(), error) {
	s.objectStorageMu.Lock()
	if err := ctx.Err(); err != nil {
		s.objectStorageMu.Unlock()
		return nil, err
	}
	return s.objectStorageMu.Unlock, nil
}

func (s *Service) lockObjectStorage(ctx context.Context, bucketID string) (func(), error) {
	if store, ok := s.store.(objectStorageUpdateLocker); ok {
		bucket, err := s.GetObjectStorageBucket(bucketID)
		if err != nil {
			return nil, err
		}
		scope := "bucket:" + bucket.ID
		if bucket.OrgID != "" {
			scope = "org:" + bucket.OrgID
		}
		return store.LockObjectStorageUpdates(ctx, scope)
	}
	s.objectStorageMu.Lock()
	if err := ctx.Err(); err != nil {
		s.objectStorageMu.Unlock()
		return nil, err
	}
	return s.objectStorageMu.Unlock, nil
}

// Quota checks use the provider's committed bytes so a failed metadata update
// or an older bucket with stale counters cannot understate bucket usage.
func (s *Service) storedObjectUsage(ctx context.Context, bucketID string) (int64, int64, error) {
	if s.objects == nil {
		return 0, 0, errors.New("object storage is unavailable")
	}
	prefix := objectStorageBucketPath(bucketID, "")
	if store, ok := s.objects.(objectUsageReader); ok {
		return store.UsageContext(ctx, objectStorageBucketScope, prefix)
	}
	var objects []ObjectInfo
	var err error
	if store, ok := s.objects.(ContextObjectStore); ok {
		objects, err = store.ListContext(ctx, objectStorageBucketScope, prefix)
	} else {
		objects, err = s.objects.List(objectStorageBucketScope, prefix)
	}
	if err != nil {
		return 0, 0, err
	}
	var size int64
	for _, object := range objects {
		size += object.Size
	}
	return size, int64(len(objects)), nil
}

type objectUsageReader interface {
	UsageContext(context.Context, string, string) (int64, int64, error)
}

type objectUsageLedger interface {
	ObjectUsageRevision(context.Context, string, bool) (int64, error)
	UnreconciledObjectBuckets(context.Context, string, string) ([]string, error)
	ReconcileObjectUsage(context.Context, string, int64, int64) error
	BeginObjectUsageChange(context.Context, string) (bool, error)
	FinishObjectUsageChange(context.Context, string, int64, int64) error
}

var ErrObjectUsageRebuilding = errors.New("object storage usage is being rebuilt; retry shortly")

func requireObjectUsage(ctx context.Context, ledger objectUsageLedger, bucketID, orgID string) error {
	buckets, err := ledger.UnreconciledObjectBuckets(ctx, bucketID, orgID)
	if err != nil {
		return err
	}
	if len(buckets) != 0 {
		return ErrObjectUsageRebuilding
	}
	return nil
}

func (s *Service) StartObjectUsageReconciliation(parent context.Context, onError func(error)) func() {
	ledger, ok := s.store.(objectUsageLedger)
	if !ok || s.objects == nil {
		return func() {}
	}
	ctx, stop := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			if err := s.rebuildObjectUsage(ctx, ledger); err != nil && ctx.Err() == nil && onError != nil {
				onError(err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { stop(); <-done }
}

func (s *Service) rebuildObjectUsage(ctx context.Context, ledger objectUsageLedger) error {
	buckets, err := ledger.UnreconciledObjectBuckets(ctx, "", "")
	if err != nil {
		return err
	}
	var failures error
	for _, bucket := range buckets {
		if err := ctx.Err(); err != nil {
			return errors.Join(failures, err)
		}
		attempt, cancel := context.WithTimeout(ctx, 10*time.Minute)
		err := s.rebuildObjectBucketUsage(attempt, ledger, bucket)
		cancel()
		failures = errors.Join(failures, err)
	}
	return failures
}

func (s *Service) rebuildObjectBucketUsage(ctx context.Context, ledger objectUsageLedger, bucketID string) error {
	release, err := s.lockObjectStorage(ctx, bucketID)
	if errors.Is(err, ErrObjectStorageBucketNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	revision, dirty, err := s.objectUsageSnapshot(ctx, ledger, bucketID)
	release()
	if err != nil || !dirty {
		return err
	}

	size, count, err := s.storedObjectUsage(ctx, bucketID)
	if err != nil {
		return err
	}
	release, err = s.lockObjectStorage(ctx, bucketID)
	if errors.Is(err, ErrObjectStorageBucketNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	defer release()
	current, dirty, err := s.objectUsageSnapshot(ctx, ledger, bucketID)
	if err != nil || !dirty || current != revision {
		return err
	}
	return ledger.ReconcileObjectUsage(ctx, bucketID, size, count)
}

func (s *Service) objectUsageSnapshot(ctx context.Context, ledger objectUsageLedger, bucketID string) (int64, bool, error) {
	buckets, err := ledger.UnreconciledObjectBuckets(ctx, bucketID, "")
	if err != nil || len(buckets) == 0 {
		return 0, false, err
	}
	if repo, ok := s.store.(objectTransferRepository); ok {
		transfers, err := repo.ListObjectTransfers(bucketID, "", "")
		if err != nil {
			return 0, false, err
		}
		for _, transfer := range transfers {
			if transfer.Buffered && !transfer.ClaimOnly {
				return 0, false, nil
			}
		}
	}
	revision, err := ledger.ObjectUsageRevision(ctx, bucketID, false)
	return revision, true, err
}

func (s *Service) beginObjectUsageChange(ctx context.Context, bucketID string) (func(int64, int64) error, error) {
	if ledger, ok := s.store.(objectUsageLedger); ok {
		current, err := ledger.BeginObjectUsageChange(ctx, bucketID)
		if err != nil {
			return nil, err
		}
		return func(bytes, count int64) error {
			if current {
				return ledger.FinishObjectUsageChange(ctx, bucketID, bytes, count)
			}
			// Keep uninitialized usage dirty until it can be reconciled.
			return nil
		}, nil
	}
	return func(bytes, count int64) error {
		if err := s.store.AdjustObjectStorageBucketSize(bucketID, bytes); err != nil {
			return err
		}
		return s.store.AdjustObjectStorageBucketObjectCount(bucketID, count)
	}, nil
}

func (s *Service) objectBucketCommittedBytes(ctx context.Context, bucketID string) (int64, error) {
	if ledger, ok := s.store.(objectUsageLedger); ok {
		if err := requireObjectUsage(ctx, ledger, bucketID, ""); err != nil {
			return 0, err
		}
		metrics, err := s.store.ObjectStorageBucketMetrics(bucketID)
		return metrics.Size, err
	}
	size, _, err := s.storedObjectUsage(ctx, bucketID)
	return size, err
}

func (s *Service) checkObjectWriteLimits(ctx context.Context, bucket ObjectStorageBucket, delta int64, transfers []ObjectTransfer, excludeID string) error {
	if delta <= 0 {
		return nil
	}
	var bucketReserved, orgReserved int64
	for _, transfer := range transfers {
		if transfer.ID == excludeID || !transfer.holdsReservation() {
			continue
		}
		if transfer.BucketID == bucket.ID {
			bucketReserved += transfer.Reserved
		}
		if transfer.OrgID == bucket.OrgID {
			orgReserved += transfer.Reserved
		}
	}
	if bucket.MaxBytes != nil {
		size, err := s.objectBucketCommittedBytes(ctx, bucket.ID)
		if err != nil {
			return err
		}
		if delta > *bucket.MaxBytes-size-bucketReserved {
			return UsageLimitError{Message: fmt.Sprintf("object storage bucket exceeds its %d-byte limit", *bucket.MaxBytes)}
		}
	}
	if bucket.OrgID == "" {
		return nil
	}
	org, err := s.store.GetOrganization(bucket.OrgID)
	if err != nil {
		return err
	}
	limit := OrgLimitsForLevel(org.UsageLevel).ObjectStorageBytes
	if limit == nil {
		return nil
	}
	size, err := s.organizationObjectUsage(ctx, bucket.OrgID)
	if err != nil {
		return err
	}
	if delta > *limit-size-orgReserved {
		return usageByteLimitError(org.UsageLevel, "object storage", *limit)
	}
	return nil
}

// Bound temporary provider bytes independently of logical replacement growth.
func (s *Service) checkObjectStagingLimit(bucket ObjectStorageBucket, size int64, transfers []ObjectTransfer) error {
	var bucketStaged, orgStaged int64
	for _, transfer := range transfers {
		if transfer.Buffered || !transfer.holdsReservation() {
			continue
		}
		if transfer.BucketID == bucket.ID {
			bucketStaged += transfer.Size
		}
		if transfer.OrgID == bucket.OrgID {
			orgStaged += transfer.Size
		}
	}
	// A same-size replacement remains possible after lowering a quota.
	if bucket.MaxBytes != nil && bucketStaged > max(*bucket.MaxBytes, size)-size {
		return UsageLimitError{Message: "object storage bucket exceeds its staging byte limit"}
	}
	if bucket.OrgID == "" {
		return nil
	}
	org, err := s.store.GetOrganization(bucket.OrgID)
	if err != nil {
		return err
	}
	limit := OrgLimitsForLevel(org.UsageLevel).ObjectStorageBytes
	if limit != nil && orgStaged > max(*limit, size)-size {
		return UsageLimitError{Message: "organization exceeds its object storage staging byte limit"}
	}
	return nil
}

func (s *Service) organizationObjectUsage(ctx context.Context, orgID string) (int64, error) {
	if ledger, ok := s.store.(objectUsageLedger); ok {
		if err := requireObjectUsage(ctx, ledger, "", orgID); err != nil {
			return 0, err
		}
		return s.store.ObjectStorageBytesByOrg(orgID)
	}
	if _, ok := s.objects.(objectUsageReader); !ok {
		return s.store.ObjectStorageBytesByOrg(orgID)
	}
	buckets, err := s.store.ListObjectStorageBucketsByOrg(orgID)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, bucket := range buckets {
		size, _, err := s.storedObjectUsage(ctx, bucket.ID)
		if err != nil {
			return 0, err
		}
		total += size
	}
	return total, nil
}

// Keep an ambiguous write pinned to its key until reconciliation. Growth also
// consumes quota while the provider outcome is unknown.
func (s *Service) reserveBufferedObjectWrite(bucket ObjectStorageBucket, key string, delta int64) (func() error, error) {
	repo, ok := s.store.(objectTransferRepository)
	if !ok {
		return func() error { return nil }, nil
	}
	id, err := randomToken()
	if err != nil {
		return nil, err
	}
	transfer := ObjectTransfer{
		ID: id, BucketID: bucket.ID, OrgID: bucket.OrgID, Key: key,
		Reserved: max(delta, 0), Buffered: true, ExpiresAt: time.Now().UTC().Add(objectTransferTTL),
	}
	if err := repo.SaveObjectTransfer(transfer); err != nil {
		return nil, err
	}
	return func() error { return repo.DeleteObjectTransfer(id) }, nil
}
