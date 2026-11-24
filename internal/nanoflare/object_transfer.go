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
	maxSingleObjectUpload    = int64(5 << 30)
	minObjectUploadPart      = int64(5 << 20)
	maxMultipartObjectUpload = int64(5 << 40)
)

var ErrObjectTransferInput = errors.New("invalid object transfer request")
var ErrObjectTransferUnavailable = errors.New("direct object uploads are unavailable")
var ErrObjectTransferNotFound = errors.New("object upload session not found")
var ErrObjectTransferConflict = errors.New("object key has an active upload session")

type ObjectTransfer struct {
	ID        string
	BucketID  string
	OrgID     string
	Key       string
	Size      int64
	Reserved  int64
	Parts     []int64
	UploadID  string
	ExpiresAt time.Time
	Result    *ObjectInfo
	Aborted   bool
	Buffered  bool
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
	ListObjectTransfers() ([]ObjectTransfer, error)
	SaveObjectTransfer(ObjectTransfer) error
	DeleteObjectTransfer(string) error
}

type objectTransferProvider interface {
	PublicPresignedUploads() bool
	CreateMultipartObjectTransfer(context.Context, string) (string, error)
	SignObjectTransfer(context.Context, ObjectTransfer, int) (string, error)
	CompleteMultipartObjectTransfer(context.Context, ObjectTransfer) error
	PublishObjectTransfer(context.Context, ObjectTransfer, string) (ObjectInfo, error)
	CleanupObjectTransfer(context.Context, ObjectTransfer) error
}

func (t ObjectTransfer) StagingKey() string {
	return path.Join("staged-uploads", t.ID)
}

func (t ObjectTransfer) active(now time.Time) bool {
	return t.Result == nil && !t.Aborted && now.Before(t.ExpiresAt)
}

func validateObjectTransfer(input ObjectTransferInput) error {
	if input.Size < 0 || input.Size > maxMultipartObjectUpload {
		return fmt.Errorf("%w: invalid upload size", ErrObjectTransferInput)
	}
	if len(input.Parts) == 0 {
		if input.Size > maxSingleObjectUpload {
			return fmt.Errorf("%w: single-part uploads are limited to 5 GiB", ErrObjectTransferInput)
		}
		return nil
	}
	if len(input.Parts) > 10000 {
		return fmt.Errorf("%w: multipart uploads are limited to 10000 parts", ErrObjectTransferInput)
	}
	var size int64
	for i, part := range input.Parts {
		if part <= 0 || part > maxSingleObjectUpload || (i < len(input.Parts)-1 && part < minObjectUploadPart) {
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
	err := s.withObjectTransfers(ctx, capability, bucketID, func(repo objectTransferRepository, provider objectTransferProvider, bucket ObjectStorageBucket, transfers []ObjectTransfer) error {
		if err := checkObjectTransferConflict(transfers, bucketID, storedPath); err != nil {
			return err
		}
		delta, err := s.objectTransferDelta(ctx, storedPath, input.Size)
		if err != nil {
			return err
		}
		if err := s.checkObjectWriteLimits(ctx, bucket, delta, transfers, ""); err != nil {
			return err
		}
		id, err := randomToken()
		if err != nil {
			return err
		}
		transfer := ObjectTransfer{
			ID: id, BucketID: bucketID, OrgID: bucket.OrgID, Key: storedPath,
			Size: input.Size, Reserved: max(delta, 0), Parts: input.Parts,
			ExpiresAt: time.Now().UTC().Add(objectTransferTTL),
		}
		if err := repo.SaveObjectTransfer(transfer); err != nil {
			return err
		}
		ticket = ObjectTransferTicket{ID: id, ExpiresAt: transfer.ExpiresAt}
		if len(transfer.Parts) == 0 {
			ticket.URL, err = provider.SignObjectTransfer(ctx, transfer, 0)
		} else {
			transfer.UploadID, err = provider.CreateMultipartObjectTransfer(ctx, transfer.StagingKey())
			if err == nil {
				err = repo.SaveObjectTransfer(transfer)
			}
		}
		if err != nil {
			// Keep the durable record when cleanup fails so the sweeper can retry.
			if provider.CleanupObjectTransfer(context.WithoutCancel(ctx), transfer) == nil {
				_ = repo.DeleteObjectTransfer(id)
			}
			return err
		}
		return nil
	})
	return ticket, err
}

func (s *Service) ObjectTransferAction(ctx context.Context, capability, bucketID, id, action string, part int) (any, error) {
	var result any
	err := s.withObjectTransfers(ctx, capability, bucketID, func(repo objectTransferRepository, provider objectTransferProvider, bucket ObjectStorageBucket, transfers []ObjectTransfer) error {
		for _, transfer := range transfers {
			if transfer.ID != id || transfer.BucketID != bucketID || transfer.Buffered {
				continue
			}
			switch action {
			case "abort":
				if transfer.Result == nil && !transfer.Aborted {
					transfer.Aborted = true
					if err := repo.SaveObjectTransfer(transfer); err != nil {
						return err
					}
				}
				if transfer.Result == nil && transfer.UploadID != "" {
					return provider.CleanupObjectTransfer(ctx, transfer)
				}
				return nil
			case "sign-part":
				if !transfer.active(time.Now()) {
					return ErrObjectTransferNotFound
				}
				if transfer.UploadID == "" || part < 1 || part > len(transfer.Parts) {
					return fmt.Errorf("%w: invalid part number", ErrObjectTransferInput)
				}
				url, err := provider.SignObjectTransfer(ctx, transfer, part)
				result = ObjectTransferTicket{ID: id, ExpiresAt: transfer.ExpiresAt, URL: url}
				return err
			case "complete":
				if transfer.Result != nil {
					result = *transfer.Result
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
				return nil
			}
			break
		}
		return ErrObjectTransferNotFound
	})
	return result, err
}

func (s *Service) withObjectTransfers(ctx context.Context, capability, bucketID string, action func(objectTransferRepository, objectTransferProvider, ObjectStorageBucket, []ObjectTransfer) error) error {
	if _, err := s.objectCapabilityAppIDContext(ctx, capability, bucketID); err != nil {
		return err
	}
	repo, repoOK := s.store.(objectTransferRepository)
	provider, providerOK := s.objects.(objectTransferProvider)
	if !repoOK || !providerOK || !provider.PublicPresignedUploads() {
		return ErrObjectTransferUnavailable
	}
	release, err := s.lockObjectStorage(ctx)
	if err != nil {
		return err
	}
	defer release()
	bucket, err := s.GetObjectStorageBucket(bucketID)
	if err != nil {
		return err
	}
	transfers, err := repo.ListObjectTransfers()
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
	delta, err := s.objectTransferDelta(ctx, transfer.Key, transfer.Size)
	if err != nil {
		return ObjectInfo{}, err
	}
	if err := s.checkObjectWriteLimits(ctx, bucket, delta, transfers, transfer.ID); err != nil {
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
	object.Key = transfer.Key[len(objectStorageBucketPath(bucket.ID, ""))+1:]
	return object, nil
}

func (s *Service) objectTransfers() ([]ObjectTransfer, error) {
	if repo, ok := s.store.(objectTransferRepository); ok {
		return repo.ListObjectTransfers()
	}
	return nil, nil
}

func checkObjectTransferConflict(transfers []ObjectTransfer, bucketID, storedPath string) error {
	now := time.Now()
	for _, t := range transfers {
		if t.BucketID == bucketID && (storedPath == "" || t.Key == storedPath) && t.active(now) {
			return ErrObjectTransferConflict
		}
	}
	return nil
}

func (s *Service) CleanupObjectTransfers(ctx context.Context) error {
	repo, repoOK := s.store.(objectTransferRepository)
	provider, providerOK := s.objects.(objectTransferProvider)
	if !repoOK || !providerOK {
		return nil
	}
	release, err := s.lockObjectStorage(ctx)
	if err != nil {
		return err
	}
	defer release()
	transfers, err := repo.ListObjectTransfers()
	if err != nil {
		return err
	}
	now := time.Now()
	var failures error
	for _, transfer := range transfers {
		// Retain the final result for retries and clean staging after presigned URLs expire.
		if transfer.Buffered {
			if !now.Before(transfer.ExpiresAt) {
				failures = errors.Join(failures, repo.DeleteObjectTransfer(transfer.ID))
			}
			continue
		}
		if now.Before(transfer.ExpiresAt.Add(time.Hour)) {
			continue
		}
		if err := provider.CleanupObjectTransfer(ctx, transfer); err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		failures = errors.Join(failures, repo.DeleteObjectTransfer(transfer.ID))
	}
	return failures
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
