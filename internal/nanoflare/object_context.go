package nanoflare

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
)

// ContextObjectStore lets runtime requests carry cancellation and invocation
// deadlines into actual storage I/O. Existing synchronous fixture stores remain
// compatible; production MinIO implements all of these operations.
type ContextObjectStore interface {
	PutContext(context.Context, string, string, string, []byte) (ObjectInfo, error)
	GetContext(context.Context, string, string) (ObjectBody, error)
	HeadContext(context.Context, string, string) (ObjectInfo, error)
	ListContext(context.Context, string, string) ([]ObjectInfo, error)
	DeleteContext(context.Context, string, string) error
}

func (s *Service) objectGetContext(ctx context.Context, app, path string) (ObjectBody, error) {
	if err := ctx.Err(); err != nil {
		return ObjectBody{}, err
	}
	if store, ok := s.objects.(ContextObjectStore); ok {
		return store.GetContext(ctx, app, path)
	}
	return s.objects.Get(app, path)
}
func (s *Service) objectHeadContext(ctx context.Context, app, path string) (ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	if store, ok := s.objects.(ContextObjectStore); ok {
		return store.HeadContext(ctx, app, path)
	}
	return s.objects.Head(app, path)
}
func (s *Service) objectPutContext(ctx context.Context, app, path, contentType string, data []byte) (ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	if store, ok := s.objects.(ContextObjectStore); ok {
		return store.PutContext(ctx, app, path, contentType, data)
	}
	return s.objects.Put(app, path, contentType, data)
}
func (s *Service) objectDeleteContext(ctx context.Context, app, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store, ok := s.objects.(ContextObjectStore); ok {
		return store.DeleteContext(ctx, app, path)
	}
	return s.objects.Delete(app, path)
}

// OpenObjectContext streams from providers that support a reader.
func (s *Service) OpenObjectContext(ctx context.Context, capability, bucketID, objectPath string) (ObjectInfo, io.ReadCloser, error) {
	if _, err := s.objectCapabilityAppIDContext(ctx, capability, bucketID); err != nil {
		return ObjectInfo{}, nil, err
	}
	storedPath := objectStorageBucketPath(bucketID, objectPath)
	if store, ok := s.objects.(interface {
		OpenObjectContext(context.Context, string, string) (ObjectInfo, io.ReadCloser, error)
	}); ok {
		info, body, err := store.OpenObjectContext(ctx, objectStorageBucketScope, storedPath)
		info.Key = strings.TrimPrefix(objectPath, "/")
		return info, body, err
	}
	object, err := s.objectGetContext(ctx, objectStorageBucketScope, storedPath)
	if err != nil {
		return ObjectInfo{}, nil, err
	}
	object.Key = strings.TrimPrefix(objectPath, "/")
	return object.ObjectInfo, io.NopCloser(bytes.NewReader(object.Body)), nil
}

func (s *Service) OpenWorkerObjectForOrg(ctx context.Context, orgID, appID, bucketID, key string) (ObjectInfo, io.ReadCloser, error) {
	if err := s.ensureAppAndObjectBucketInOrg(orgID, appID, bucketID); err != nil {
		return ObjectInfo{}, nil, err
	}
	app, _, err := s.worker(appID)
	if err != nil {
		return ObjectInfo{}, nil, err
	}
	if err := s.ensureActiveDeploymentBindsObjectStorageBucket(appID, bucketID); err != nil {
		return ObjectInfo{}, nil, err
	}
	return s.OpenObjectContext(ctx, app.RuntimeToken, bucketID, key)
}

var ErrObjectDownloadUnavailable = errors.New("public object downloads are unavailable")

type publicObjectDownloadSigner interface {
	PublicPresignedDownloads() bool
	PresignPublicDownload(context.Context, string, string, time.Duration) (string, error)
}

func (s *Service) PresignPublicObjectDownload(ctx context.Context, capability, bucketID, objectPath string) (string, error) {
	if _, err := s.objectCapabilityAppIDContext(ctx, capability, bucketID); err != nil {
		return "", err
	}
	if strings.Trim(path.Clean("/"+objectPath), "/") == "" {
		return "", fmt.Errorf("%w: object key is required", ErrObjectTransferInput)
	}
	signer, ok := s.objects.(publicObjectDownloadSigner)
	if !ok || !signer.PublicPresignedDownloads() {
		return "", ErrObjectDownloadUnavailable
	}
	return signer.PresignPublicDownload(ctx, objectStorageBucketScope, objectStorageBucketPath(bucketID, objectPath), 5*time.Minute)
}
