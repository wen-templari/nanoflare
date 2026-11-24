package nanoflare

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type objectStorageUpdateLocker interface {
	LockObjectStorageUpdates(context.Context) (func(), error)
}

func (s *Store) LockObjectStorageUpdates(ctx context.Context) (func(), error) {
	s.objectStorageMu.Lock()
	if err := ctx.Err(); err != nil {
		s.objectStorageMu.Unlock()
		return nil, err
	}
	return s.objectStorageMu.Unlock, nil
}

func (s *Service) lockObjectStorage(ctx context.Context) (func(), error) {
	if store, ok := s.store.(objectStorageUpdateLocker); ok {
		return store.LockObjectStorageUpdates(ctx)
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

func (s *Service) checkObjectWriteLimits(ctx context.Context, bucket ObjectStorageBucket, delta int64, transfers []ObjectTransfer, excludeID string) error {
	if delta <= 0 {
		return nil
	}
	now := time.Now()
	var bucketReserved, orgReserved int64
	for _, transfer := range transfers {
		if transfer.ID == excludeID || !transfer.active(now) {
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
		size, _, err := s.storedObjectUsage(ctx, bucket.ID)
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

func (s *Service) organizationObjectUsage(ctx context.Context, orgID string) (int64, error) {
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

// Retain a growth reservation on provider errors because the write may have
// reached storage even when its response did not reach Nanoflare.
func (s *Service) reserveBufferedObjectWrite(bucket ObjectStorageBucket, key string, delta int64) (func() error, error) {
	repo, ok := s.store.(objectTransferRepository)
	if !ok || delta <= 0 {
		return func() error { return nil }, nil
	}
	id, err := randomToken()
	if err != nil {
		return nil, err
	}
	transfer := ObjectTransfer{
		ID: id, BucketID: bucket.ID, OrgID: bucket.OrgID, Key: key,
		Reserved: delta, Buffered: true, ExpiresAt: time.Now().UTC().Add(objectTransferTTL),
	}
	if err := repo.SaveObjectTransfer(transfer); err != nil {
		return nil, err
	}
	return func() error { return repo.DeleteObjectTransfer(id) }, nil
}
