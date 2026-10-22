package nanoflare

import (
	"context"
	"errors"
	"time"
)

const AssetUploadCleanupBatch = 256

func validateAssetPruneLimit(limit int) error {
	if limit < 1 || limit > AssetUploadCleanupBatch {
		return errors.New("asset cleanup limit must be 1-256")
	}
	return nil
}

// Cleanup only touches expired staging records. Deployed artifacts and their
// copied object bytes have a separate lifetime and are never swept here.
func (s *Service) PruneExpiredAssetUploads(ctx context.Context) (int, error) {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.store.PruneAssetUploadSessions(bounded, time.Now().UTC(), AssetUploadCleanupBatch)
}

// The returned stop function cancels database work and joins the worker before
// the caller closes its repository. Each tick removes at most one bounded batch.
func (s *Service) StartAssetUploadCleanup(parent context.Context, interval time.Duration, onError func(error)) func() {
	if interval <= 0 {
		interval = time.Minute
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			if _, err := s.PruneExpiredAssetUploads(ctx); err != nil && ctx.Err() == nil && onError != nil {
				onError(err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}
