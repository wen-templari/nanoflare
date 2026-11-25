package nanoflare

import (
	"context"
	"errors"
	"fmt"
	"path"
	"time"
)

const (
	objectTransferTTL        = 15 * time.Minute
	objectPartURLTTL         = 24 * time.Hour
	objectCompletionGrace    = time.Hour
	objectSingleURLTTL       = time.Hour
	maxSingleObjectUpload    = int64(64 << 20)
	maxObjectUploadPart      = int64(5 << 30)
	minObjectUploadPart      = int64(5 << 20)
	maxMultipartObjectUpload = int64(5 << 40)
)

var ErrObjectTransferInput = errors.New("invalid object transfer request")
var ErrObjectTransferUnavailable = errors.New("direct object uploads are unavailable")
var ErrObjectTransferNotFound = errors.New("object upload session not found")
var ErrObjectTransferConflict = errors.New("object key has an active upload session")

type ObjectTransfer struct {
	ID              string
	BucketID        string
	OrgID           string
	Key             string
	Size            int64
	Reserved        int64
	Parts           []int64
	UploadID        string
	ExpiresAt       time.Time
	Cleaned         bool
	DirectMultipart bool
	Result          *ObjectInfo
	Aborted         bool
	Buffered        bool
	ClaimOnly       bool
}

type ObjectTransferInput struct {
	Key   string  `json:"key"`
	Size  int64   `json:"size"`
	Parts []int64 `json:"parts,omitempty"`
}

type ObjectTransferTicket struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
	URL       string    `json:"url,omitempty"`
}

type objectTransferRepository interface {
	ListObjectTransfers(bucketID, orgID, includeID string) ([]ObjectTransfer, error)
	SaveObjectTransfer(ObjectTransfer) error
	DeleteObjectTransfer(string) error
}

type objectTransferProvider interface {
	PublicPresignedUploads() bool
	CreateMultipartObjectTransfer(context.Context, ObjectTransfer) (string, error)
	SignObjectTransfer(context.Context, ObjectTransfer, int) (string, error)
	CompleteMultipartObjectTransfer(context.Context, ObjectTransfer) error
	PublishObjectTransfer(context.Context, ObjectTransfer, string) (ObjectInfo, error)
	CleanupObjectTransfer(context.Context, ObjectTransfer) error
}

func (t ObjectTransfer) StagingKey() string {
	return path.Join("staged-uploads", t.ID)
}

func (t ObjectTransfer) UploadKey() string {
	if t.DirectMultipart {
		return t.Key
	}
	return t.StagingKey()
}

func (t ObjectTransfer) active(now time.Time) bool {
	return t.Result == nil && !t.Aborted && now.Before(t.completionDeadline())
}

func (t ObjectTransfer) holdsReservation() bool {
	// Keep the reservation until storage cleanup or ambiguous PUT recovery.
	return !t.Cleaned
}

func (t ObjectTransfer) completionDeadline() time.Time {
	if t.Buffered {
		return t.ExpiresAt
	}
	return t.ExpiresAt.Add(objectCompletionGrace)
}

func validateObjectTransfer(input ObjectTransferInput) error {
	if input.Size < 0 || input.Size > maxMultipartObjectUpload {
		return fmt.Errorf("%w: invalid upload size", ErrObjectTransferInput)
	}
	if len(input.Parts) == 0 {
		if input.Size > maxSingleObjectUpload {
			return fmt.Errorf("%w: single-part uploads are limited to 64 MiB; use multipart for larger objects", ErrObjectTransferInput)
		}
		return nil
	}
	if len(input.Parts) > 10000 {
		return fmt.Errorf("%w: multipart uploads are limited to 10000 parts", ErrObjectTransferInput)
	}
	var size int64
	for i, part := range input.Parts {
		if part <= 0 || part > maxObjectUploadPart || (i < len(input.Parts)-1 && part < minObjectUploadPart) {
			return fmt.Errorf("%w: invalid upload size for part %d", ErrObjectTransferInput, i+1)
		}
		size += part
	}
	if size != input.Size {
		return fmt.Errorf("%w: part sizes must add up to upload size", ErrObjectTransferInput)
	}
	return nil
}

func (s *Service) CreateObjectTransfer(ctx context.Context, capability, bucketID string, input ObjectTransferInput) (ObjectTransferTicket, error) {
	var ticket ObjectTransferTicket
	if err := validateObjectTransfer(input); err != nil {
		return ticket, err
	}
	storedPath := objectStorageBucketPath(bucketID, input.Key)
	if storedPath == objectStorageBucketPath(bucketID, "") {
		return ticket, fmt.Errorf("%w: object key is required", ErrObjectTransferInput)
	}
	var claim ObjectTransfer
	var claimedDelta int64
	if mutationRepo, ok := s.store.(objectMutationRepository); ok {
		if _, err := s.objectCapabilityAppIDContext(ctx, capability, bucketID); err != nil {
			return ticket, err
		}
		backend, ok := s.objects.(objectTransferProvider)
		if !ok || !backend.PublicPresignedUploads() {
			return ticket, ErrObjectTransferUnavailable
		}
		var err error
		claim, err = s.claimObjectMutation(ctx, mutationRepo, bucketID, input.Key)
		if err != nil {
			return ticket, err
		}
		defer mutationRepo.DeleteObjectTransfer(claim.ID)
		claimedDelta, err = s.objectTransferDelta(ctx, storedPath, input.Size)
		if err != nil {
			return ticket, err
		}
	}
	var transfer ObjectTransfer
	var provider objectTransferProvider
	var repo objectTransferRepository
	err := s.withObjectTransfers(ctx, capability, bucketID, "", func(repository objectTransferRepository, backend objectTransferProvider, bucket ObjectStorageBucket, transfers []ObjectTransfer) error {
		repo, provider = repository, backend
		delta := claimedDelta
		if claim.ID == "" {
			if err := checkObjectTransferConflict(transfers, bucketID, storedPath); err != nil {
				return err
			}
			var err error
			delta, err = s.objectTransferDelta(ctx, storedPath, input.Size)
			if err != nil {
				return err
			}
		}
		if err := s.checkObjectWriteLimits(ctx, bucket, delta, transfers, ""); err != nil {
			return err
		}
		if err := s.checkObjectStagingLimit(bucket, input.Size, transfers); err != nil {
			return err
		}
		id, err := randomToken()
		if err != nil {
			return err
		}
		transfer = ObjectTransfer{
			ID: id, BucketID: bucketID, OrgID: bucket.OrgID, Key: storedPath,
			Size: input.Size, Reserved: max(delta, 0), Parts: input.Parts, DirectMultipart: len(input.Parts) > 0,
			ExpiresAt: time.Now().UTC().Add(objectSingleURLTTL),
		}
		if transfer.DirectMultipart {
			transfer.ExpiresAt = time.Now().UTC().Add(objectPartURLTTL)
		}
		return repo.SaveObjectTransfer(transfer)
	})
	if err != nil {
		return ticket, err
	}
	ticket = ObjectTransferTicket{ID: transfer.ID, ExpiresAt: transfer.ExpiresAt}
	if len(transfer.Parts) == 0 {
		ticket.URL, err = provider.SignObjectTransfer(ctx, transfer, 0)
	} else {
		transfer.UploadID, err = provider.CreateMultipartObjectTransfer(ctx, transfer)
		if err == nil {
			err = repo.SaveObjectTransfer(transfer)
		}
	}
	if err != nil {
		// Keep the durable record when cleanup fails so the sweeper can retry.
		if provider.CleanupObjectTransfer(context.WithoutCancel(ctx), transfer) == nil {
			_ = repo.DeleteObjectTransfer(transfer.ID)
		}
	}
	return ticket, err
}

func (s *Service) ObjectTransferAction(ctx context.Context, capability, bucketID, id, action string, part int) (any, error) {
	if repo, ok := s.store.(objectMutationRepository); ok && action == "complete" {
		return s.completeStoredObjectTransfer(ctx, repo, capability, bucketID, id)
	}
	var result any
	var afterUnlock func() error
	err := s.withObjectTransfers(ctx, capability, bucketID, id, func(repo objectTransferRepository, provider objectTransferProvider, bucket ObjectStorageBucket, transfers []ObjectTransfer) error {
		for _, transfer := range transfers {
			if transfer.ID != id || transfer.BucketID != bucketID || transfer.Buffered {
				continue
			}
			if transfer.Result == nil && (action == "abort" || action == "sign-part") {
				for _, other := range transfers {
					if other.Buffered && other.Key == transfer.Key {
						return ErrObjectTransferConflict
					}
				}
			}
			switch action {
			case "abort":
				if transfer.Result == nil && !transfer.Aborted {
					transfer.Aborted = true
					if err := repo.SaveObjectTransfer(transfer); err != nil {
						return err
					}
				}
				if transfer.Result == nil {
					afterUnlock = func() error { return cleanObjectTransfer(ctx, repo, provider, transfer) }
				}
				return nil
			case "sign-part":
				if !transfer.active(time.Now()) {
					return ErrObjectTransferNotFound
				}
				if transfer.UploadID == "" || part < 1 || part > len(transfer.Parts) {
					return fmt.Errorf("%w: invalid part number", ErrObjectTransferInput)
				}
				ticket := transfer
				ticket.ExpiresAt = time.Now().UTC().Add(objectPartURLTTL)
				if transfer.ExpiresAt.Before(ticket.ExpiresAt) {
					transfer.ExpiresAt = ticket.ExpiresAt
					if err := repo.SaveObjectTransfer(transfer); err != nil {
						return err
					}
				}
				afterUnlock = func() error {
					url, err := provider.SignObjectTransfer(ctx, ticket, part)
					result = ObjectTransferTicket{ID: id, ExpiresAt: ticket.ExpiresAt, URL: url}
					return err
				}
				return nil
			case "complete":
				if transfer.Result != nil {
					result = *transfer.Result
					_ = cleanObjectTransfer(ctx, repo, provider, transfer)
					return nil
				}
				if !transfer.active(time.Now()) {
					return ErrObjectTransferNotFound
				}
				object, err := s.completeObjectTransfer(ctx, provider, bucket, transfer, transfers)
				if err != nil {
					return err
				}
				transfer.Result = &object
				if err := repo.SaveObjectTransfer(transfer); err != nil {
					return err
				}
				_ = s.store.IncrementObjectStorageBucketWrites(bucketID)
				result = object
				_ = cleanObjectTransfer(ctx, repo, provider, transfer)
				return nil
			}
			break
		}
		return ErrObjectTransferNotFound
	})
	if err == nil && afterUnlock != nil {
		err = afterUnlock()
	}
	return result, err
}

func (s *Service) withObjectTransfers(ctx context.Context, capability, bucketID, includeID string, action func(objectTransferRepository, objectTransferProvider, ObjectStorageBucket, []ObjectTransfer) error) error {
	if _, err := s.objectCapabilityAppIDContext(ctx, capability, bucketID); err != nil {
		return err
	}
	repo, repoOK := s.store.(objectTransferRepository)
	provider, providerOK := s.objects.(objectTransferProvider)
	if !repoOK || !providerOK || !provider.PublicPresignedUploads() {
		return ErrObjectTransferUnavailable
	}
	release, err := s.lockObjectStorage(ctx, bucketID)
	if err != nil {
		return err
	}
	defer release()
	bucket, err := s.GetObjectStorageBucket(bucketID)
	if err != nil {
		return err
	}
	transfers, err := repo.ListObjectTransfers(bucket.ID, bucket.OrgID, includeID)
	if err != nil {
		return err
	}
	return action(repo, provider, bucket, transfers)
}

func (s *Service) objectTransferDelta(ctx context.Context, storedPath string, size int64) (int64, error) {
	existing, err := s.objectHeadContext(ctx, objectStorageBucketScope, storedPath)
	if errors.Is(err, ErrObjectNotFound) {
		return size, nil
	}
	return size - existing.Size, err
}

func (s *Service) completeObjectTransfer(ctx context.Context, provider objectTransferProvider, bucket ObjectStorageBucket, transfer ObjectTransfer, transfers []ObjectTransfer) (ObjectInfo, error) {
	var count int64
	existing, err := s.objectHeadContext(ctx, objectStorageBucketScope, transfer.Key)
	if errors.Is(err, ErrObjectNotFound) {
		count = 1
	} else if err != nil {
		return ObjectInfo{}, err
	}
	delta := transfer.Size - existing.Size
	if err := s.checkObjectWriteLimits(ctx, bucket, delta, transfers, transfer.ID); err != nil {
		return ObjectInfo{}, err
	}
	finishUsage, err := s.beginObjectUsageChange(ctx, bucket.ID)
	if err != nil {
		return ObjectInfo{}, err
	}
	if transfer.UploadID != "" {
		if err := provider.CompleteMultipartObjectTransfer(ctx, transfer); err != nil {
			return ObjectInfo{}, err
		}
	}
	object, err := provider.PublishObjectTransfer(ctx, transfer, transfer.Key)
	if err != nil {
		return ObjectInfo{}, err
	}
	if err := finishUsage(delta, count); err != nil {
		return ObjectInfo{}, err
	}
	object.Key = transfer.Key[len(objectStorageBucketPath(bucket.ID, ""))+1:]
	return object, nil
}

func (s *Service) objectTransfers(bucketID string) ([]ObjectTransfer, error) {
	if repo, ok := s.store.(objectTransferRepository); ok {
		bucket, err := s.GetObjectStorageBucket(bucketID)
		if err != nil {
			return nil, err
		}
		return repo.ListObjectTransfers(bucket.ID, bucket.OrgID, "")
	}
	return nil, nil
}

func checkObjectTransferConflict(transfers []ObjectTransfer, bucketID, storedPath string) error {
	now := time.Now()
	for _, t := range transfers {
		if t.BucketID == bucketID && t.Key == storedPath && (t.active(now) || (t.Buffered && t.holdsReservation())) {
			return ErrObjectTransferConflict
		}
	}
	return nil
}

func cleanObjectTransfer(ctx context.Context, repo objectTransferRepository, provider objectTransferProvider, transfer ObjectTransfer) error {
	if transfer.Cleaned {
		return nil
	}
	if err := provider.CleanupObjectTransfer(ctx, transfer); err != nil {
		return err
	}
	if store, ok := repo.(interface{ MarkObjectTransferCleaned(string) error }); ok {
		return store.MarkObjectTransferCleaned(transfer.ID)
	}
	transfer.Cleaned = true
	return repo.SaveObjectTransfer(transfer)
}

func (s *Service) CleanupObjectTransfers(ctx context.Context) error {
	repo, repoOK := s.store.(objectTransferRepository)
	provider, providerOK := s.objects.(objectTransferProvider)
	if !repoOK || !providerOK {
		return nil
	}
	transfers, err := repo.ListObjectTransfers("", "", "")
	if err != nil {
		return err
	}
	var failures error
	for _, candidate := range transfers {
		if err := ctx.Err(); err != nil {
			return errors.Join(failures, err)
		}
		failures = errors.Join(failures, s.cleanupObjectTransfer(ctx, repo, provider, candidate))
	}
	return failures
}

func (s *Service) cleanupObjectTransfer(ctx context.Context, repo objectTransferRepository, provider objectTransferProvider, candidate ObjectTransfer) error {
	// The scope survives bucket deletion, so abandoned staging can still be removed.
	scope := "bucket:" + candidate.BucketID
	if candidate.OrgID != "" {
		scope = "org:" + candidate.OrgID
	}
	locker, ok := s.store.(objectStorageUpdateLocker)
	if !ok {
		return ErrObjectTransferUnavailable
	}
	release, err := locker.LockObjectStorageUpdates(ctx, scope)
	if err != nil {
		return err
	}
	defer func() { release() }()
	transfers, err := repo.ListObjectTransfers(candidate.BucketID, "", candidate.ID)
	if err != nil {
		return err
	}
	for _, transfer := range transfers {
		if transfer.ID != candidate.ID {
			continue
		}
		if time.Now().Before(transfer.completionDeadline()) {
			if transfer.Result != nil || transfer.Aborted {
				release()
				release = func() {}
				return cleanObjectTransfer(ctx, repo, provider, transfer)
			}
			return nil
		}
		if !transfer.Buffered {
			for _, other := range transfers {
				if other.Buffered && other.Key == transfer.Key {
					return nil
				}
			}
		}
		if !transfer.Buffered {
			release()
			release = func() {}
			if err := provider.CleanupObjectTransfer(ctx, transfer); err != nil {
				return err
			}
		}
		if transfer.Buffered && !transfer.ClaimOnly {
			if ledger, ok := s.store.(objectUsageLedger); ok {
				if _, err := ledger.BeginObjectUsageChange(ctx, transfer.BucketID); err != nil {
					return err
				}
			}
		}
		return repo.DeleteObjectTransfer(transfer.ID)
	}
	return nil
}

func (s *Service) StartObjectTransferCleanup(parent context.Context, onError func(error)) func() {
	if _, ok := s.objects.(objectTransferProvider); !ok {
		return func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanup, stop := context.WithTimeout(ctx, 10*time.Second)
				err := s.CleanupObjectTransfers(cleanup)
				stop()
				if err != nil && ctx.Err() == nil && onError != nil {
					onError(err)
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}
