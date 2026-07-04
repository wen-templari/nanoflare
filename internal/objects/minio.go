package objects

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type MinIO struct {
	client *minio.Client
	bucket string
}

type MinIOConfig struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Secure    bool
}

func Open(ctx context.Context, config MinIOConfig) (*MinIO, error) {
	if config.Endpoint == "" || config.AccessKey == "" || config.SecretKey == "" || config.Bucket == "" {
		return nil, errors.New("MinIO endpoint, credentials, and bucket are required")
	}
	client, err := minio.New(config.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(config.AccessKey, config.SecretKey, ""),
		Secure: config.Secure,
	})
	if err != nil {
		return nil, err
	}
	store := &MinIO{client: client, bucket: config.Bucket}
	exists, err := client.BucketExists(ctx, config.Bucket)
	if err != nil {
		return nil, err
	}
	if !exists {
		if err := client.MakeBucket(ctx, config.Bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (m *MinIO) PresignUpload(appID, objectPath string, expiry time.Duration) (string, error) {
	key, err := objectKey(appID, objectPath)
	if err != nil {
		return "", err
	}
	signed, err := m.client.PresignedPutObject(context.Background(), m.bucket, key, expiry)
	if err != nil {
		return "", err
	}
	return signed.String(), nil
}

func (m *MinIO) PresignDownload(appID, objectPath string, expiry time.Duration) (string, error) {
	key, err := objectKey(appID, objectPath)
	if err != nil {
		return "", err
	}
	signed, err := m.client.PresignedGetObject(context.Background(), m.bucket, key, expiry, nil)
	if err != nil {
		return "", err
	}
	return signed.String(), nil
}

func (m *MinIO) Put(appID, objectPath string, contentType string, data []byte) (nanoflare.ObjectInfo, error) {
	key, err := objectKey(appID, objectPath)
	if err != nil {
		return nanoflare.ObjectInfo{}, err
	}
	_, err = m.client.PutObject(context.Background(), m.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return nanoflare.ObjectInfo{}, err
	}
	return m.Head(appID, objectPath)
}

func (m *MinIO) Get(appID, objectPath string) (nanoflare.ObjectBody, error) {
	key, err := objectKey(appID, objectPath)
	if err != nil {
		return nanoflare.ObjectBody{}, err
	}
	object, err := m.client.GetObject(context.Background(), m.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nanoflare.ObjectBody{}, err
	}
	defer object.Close()
	info, err := object.Stat()
	if err != nil {
		return nanoflare.ObjectBody{}, minioNotFound(err)
	}
	body, err := io.ReadAll(object)
	if err != nil {
		return nanoflare.ObjectBody{}, minioNotFound(err)
	}
	return nanoflare.ObjectBody{
		ObjectInfo: objectInfo(objectPath, info),
		Body:       body,
	}, nil
}

func (m *MinIO) Head(appID, objectPath string) (nanoflare.ObjectInfo, error) {
	key, err := objectKey(appID, objectPath)
	if err != nil {
		return nanoflare.ObjectInfo{}, err
	}
	info, err := m.client.StatObject(context.Background(), m.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return nanoflare.ObjectInfo{}, minioNotFound(err)
	}
	return objectInfo(objectPath, info), nil
}

func (m *MinIO) List(appID, prefix string) ([]nanoflare.ObjectInfo, error) {
	key, err := objectKey(appID, prefix)
	if err != nil {
		return nil, err
	}
	objects := make([]nanoflare.ObjectInfo, 0)
	for object := range m.client.ListObjects(context.Background(), m.bucket, minio.ListObjectsOptions{
		Prefix:    key + "/",
		Recursive: true,
	}) {
		if object.Err != nil {
			return nil, object.Err
		}
		relative := strings.TrimPrefix(object.Key, path.Join("apps", appID)+"/")
		objects = append(objects, objectInfo(relative, object))
	}
	return objects, nil
}

func (m *MinIO) Delete(appID, objectPath string) error {
	key, err := objectKey(appID, objectPath)
	if err != nil {
		return err
	}
	return m.client.RemoveObject(context.Background(), m.bucket, key, minio.RemoveObjectOptions{})
}

func objectInfo(objectPath string, info minio.ObjectInfo) nanoflare.ObjectInfo {
	return nanoflare.ObjectInfo{
		Key:      objectPath,
		Size:     info.Size,
		ETag:     strings.Trim(info.ETag, `"`),
		HTTPETag: info.ETag,
		Uploaded: info.LastModified.UTC(),
		HTTPMetadata: nanoflare.ObjectHTTPMetadata{
			ContentType: info.ContentType,
		},
	}
}

func minioNotFound(err error) error {
	var response minio.ErrorResponse
	if errors.As(err, &response) && response.StatusCode == 404 {
		return nanoflare.ErrObjectNotFound
	}
	return err
}

func objectKey(appID, objectPath string) (string, error) {
	if objectPath == "" || strings.Contains(objectPath, "..") {
		return "", errors.New("object path must not be empty or contain '..'")
	}
	clean := strings.TrimPrefix(path.Clean("/"+objectPath), "/")
	if clean == "." || clean == "" {
		return "", errors.New("object path is required")
	}
	return path.Join("apps", appID, clean), nil
}
