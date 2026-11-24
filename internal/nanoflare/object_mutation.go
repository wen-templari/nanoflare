package nanoflare

import (
	"context"
	"errors"
	"strings"
	"time"
)

type objectMutationRepository interface {
	objectTransferRepository
	FinishObjectMutation(context.Context, string, string, int64, int64, *ObjectTransfer) error
}

// ErrObjectPutRejected indicates the provider definitively rejected a PUT.
var ErrObjectPutRejected = errors.New("object PUT was rejected before commit")

type objectMutationContextKey struct{}

// ObjectMutationID identifies an admitted PUT so provider metadata can confirm
// its outcome even when the response was lost.
func ObjectMutationID(ctx context.Context) string {
	id, _ := ctx.Value(objectMutationContextKey{}).(string)
	return id
}

// A durable key claim protects the HEAD and ensuing mutation even while the
// organization lock is released. Positive growth is reserved before the PUT.
func (s *Service) mutateStoredObject(ctx context.Context, repo objectMutationRepository, bucketID, key, contentType string, data []byte, deleting bool) (ObjectInfo, error) {
	transfer, err := s.claimObjectMutation(ctx, repo, bucketID, key)
	if err != nil {
		return ObjectInfo{}, err
	}
	mutating := false
	defer func() {
		if !mutating {
			_ = repo.DeleteObjectTransfer(transfer.ID)
		}
	}()

	var oldSize, oldCount int64
	existing, err := s.objectHeadContext(ctx, objectStorageBucketScope, transfer.Key)
	if err == nil {
		oldSize, oldCount = existing.Size, 1
	} else if !errors.Is(err, ErrObjectNotFound) {
		return ObjectInfo{}, err
	}
	delta := int64(len(data)) - oldSize
	if err := s.reserveObjectMutation(ctx, repo, transfer, delta, ""); err != nil {
		return ObjectInfo{}, err
	}

	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	mutating = true
	var object ObjectInfo
	var count int64
	if deleting {
		err = s.objectDeleteContext(ctx, objectStorageBucketScope, transfer.Key)
		count = -oldCount
	} else {
		// Let an admitted PUT reach a definitive outcome after a disconnect.
		put, stopPut := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		put = context.WithValue(put, objectMutationContextKey{}, transfer.ID)
		object, err = s.objectPutContext(put, objectStorageBucketScope, transfer.Key, contentType, data)
		stopPut()
		count = 1 - oldCount
	}
	if err != nil {
		if !deleting {
			s.settleFailedObjectPut(ctx, repo, transfer, oldSize, oldCount, err)
		}
		return ObjectInfo{}, err
	}
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.finishStoredObjectMutation(finish, repo, transfer, object.Size-oldSize, count, nil); err != nil {
		return ObjectInfo{}, err
	}
	object.Key = strings.TrimPrefix(key, "/")
	return object, nil
}

func (s *Service) settleFailedObjectPut(ctx context.Context, repo objectMutationRepository, claim ObjectTransfer, oldSize, oldCount int64, putErr error) {
	recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	object, err := s.objectHeadContext(recovery, objectStorageBucketScope, claim.Key)
	switch {
	case err == nil && (object.MutationID == claim.ID || errors.Is(putErr, ErrObjectPutRejected)):
		_ = s.finishStoredObjectMutation(recovery, repo, claim, object.Size-oldSize, 1-oldCount, nil)
	case errors.Is(putErr, ErrObjectPutRejected) && errors.Is(err, ErrObjectNotFound):
		_ = s.finishStoredObjectMutation(recovery, repo, claim, -oldSize, -oldCount, nil)
	}
	// An unchanged HEAD after an ambiguous timeout cannot rule out a late
	// commit. Retain the claim if the provider outcome is still unknown.
}

func (s *Service) claimObjectMutation(ctx context.Context, repo objectMutationRepository, bucketID, key string) (ObjectTransfer, error) {
	release, err := s.lockObjectStorage(ctx, bucketID)
	if err != nil {
		return ObjectTransfer{}, err
	}
	defer release()
	bucket, err := s.GetObjectStorageBucket(bucketID)
	if err != nil {
		return ObjectTransfer{}, err
	}
	transfers, err := repo.ListObjectTransfers(bucketID, "", "")
	if err != nil {
		return ObjectTransfer{}, err
	}
	storedPath := objectStorageBucketPath(bucketID, key)
	if err := checkObjectTransferConflict(transfers, bucketID, storedPath); err != nil {
		return ObjectTransfer{}, err
	}
	return saveObjectMutationClaim(repo, bucket, storedPath)
}

func saveObjectMutationClaim(repo objectTransferRepository, bucket ObjectStorageBucket, storedPath string) (ObjectTransfer, error) {
	id, err := randomToken()
	if err != nil {
		return ObjectTransfer{}, err
	}
	transfer := ObjectTransfer{
		ID: id, BucketID: bucket.ID, OrgID: bucket.OrgID, Key: storedPath,
		Buffered: true, ClaimOnly: true, ExpiresAt: time.Now().UTC().Add(objectTransferTTL),
	}
	return transfer, repo.SaveObjectTransfer(transfer)
}

func (s *Service) reserveObjectMutation(ctx context.Context, repo objectMutationRepository, claim ObjectTransfer, delta int64, uploadID string) error {
	release, err := s.lockObjectStorage(ctx, claim.BucketID)
	if err != nil {
		return err
	}
	defer release()
	bucket, err := s.GetObjectStorageBucket(claim.BucketID)
	if err != nil {
		return err
	}
	if delta > 0 {
		transfers, err := repo.ListObjectTransfers(bucket.ID, bucket.OrgID, uploadID)
		if err != nil {
			return err
		}
		if err := s.checkObjectWriteLimits(ctx, bucket, delta, transfers, uploadID); err != nil {
			return err
		}
		claim.Reserved = delta
		for _, transfer := range transfers {
			if uploadID != "" && transfer.ID == uploadID {
				claim.Reserved = max(0, delta-transfer.Reserved)
				break
			}
		}
	}
	if ledger, ok := repo.(objectUsageLedger); ok {
		if _, err := ledger.ObjectUsageRevision(ctx, bucket.ID, true); err != nil {
			return err
		}
	}
	claim.ClaimOnly = false
	return repo.SaveObjectTransfer(claim)
}

func (s *Service) completeStoredObjectTransfer(ctx context.Context, repo objectMutationRepository, capability, bucketID, id string) (any, error) {
	var transfer, claim ObjectTransfer
	var provider objectTransferProvider
	err := s.withObjectTransfers(ctx, capability, bucketID, id, func(_ objectTransferRepository, backend objectTransferProvider, bucket ObjectStorageBucket, transfers []ObjectTransfer) error {
		provider = backend
		for _, candidate := range transfers {
			if candidate.ID != id || candidate.Buffered || candidate.BucketID != bucketID {
				continue
			}
			transfer = candidate
			if transfer.Result != nil {
				return nil
			}
			if !transfer.active(time.Now()) {
				return ErrObjectTransferNotFound
			}
			for _, other := range transfers {
				if other.Buffered && other.Key == transfer.Key {
					return ErrObjectTransferConflict
				}
			}
			var err error
			claim, err = saveObjectMutationClaim(repo, bucket, transfer.Key)
			return err
		}
		return ErrObjectTransferNotFound
	})
	if err != nil {
		return nil, err
	}
	if transfer.Result != nil {
		return *transfer.Result, nil
	}

	mutating := false
	defer func() {
		if !mutating {
			_ = repo.DeleteObjectTransfer(claim.ID)
		}
	}()
	var oldSize, count int64
	existing, err := s.objectHeadContext(ctx, objectStorageBucketScope, transfer.Key)
	switch {
	case err == nil:
		oldSize = existing.Size
	case errors.Is(err, ErrObjectNotFound):
		count = 1
	default:
		return nil, err
	}
	delta := transfer.Size - oldSize
	if err := s.reserveObjectMutation(ctx, repo, claim, delta, transfer.ID); err != nil {
		return nil, err
	}

	mutating = true
	if transfer.UploadID != "" {
		if err := provider.CompleteMultipartObjectTransfer(ctx, transfer); err != nil {
			mutating = !errors.Is(err, ErrObjectTransferInput)
			return nil, err
		}
	}
	object, err := provider.PublishObjectTransfer(ctx, transfer, transfer.Key)
	if err != nil {
		mutating = !errors.Is(err, ErrObjectTransferInput)
		return nil, err
	}
	object.Key = strings.TrimPrefix(transfer.Key, objectStorageBucketPath(bucketID, "")+"/")
	transfer.Result = &object
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.finishStoredObjectMutation(finish, repo, claim, delta, count, &transfer); err != nil {
		return nil, err
	}
	_ = s.store.IncrementObjectStorageBucketWrites(bucketID)
	_ = cleanObjectTransfer(finish, repo, provider, transfer)
	return object, nil
}

func (s *Service) finishStoredObjectMutation(ctx context.Context, repo objectMutationRepository, claim ObjectTransfer, bytes, count int64, transfer *ObjectTransfer) error {
	release, err := s.lockObjectStorage(ctx, claim.BucketID)
	if err != nil {
		return err
	}
	defer release()
	return repo.FinishObjectMutation(ctx, claim.BucketID, claim.ID, bytes, count, transfer)
}
