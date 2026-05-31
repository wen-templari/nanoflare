package objects

import (
	"context"
	"errors"
	"path"
	"strings"
	"time"

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

func (m *MinIO) Delete(appID, objectPath string) error {
	key, err := objectKey(appID, objectPath)
	if err != nil {
		return err
	}
	return m.client.RemoveObject(context.Background(), m.bucket, key, minio.RemoveObjectOptions{})
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
