package objects

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/clas/nanoflare/internal/nanoflare"
	"github.com/minio/minio-go/v7"
)

const objectTransferScope = "object-storage-buckets"
const objectTransferMetadata = "X-Amz-Meta-Nanoflare-Upload"

func (m *MinIO) PublicPresignedUploads() bool {
	return m.uploadSigner != nil
}

func (m *MinIO) CreateMultipartObjectTransfer(ctx context.Context, transfer nanoflare.ObjectTransfer) (string, error) {
	ctx, cancel := m.operationContext(ctx)
	defer cancel()
	key, err := objectKey(objectTransferScope, transfer.UploadKey())
	if err != nil {
		return "", err
	}
	return (minio.Core{Client: m.client}).NewMultipartUpload(ctx, m.bucket, key, minio.PutObjectOptions{
		UserMetadata: map[string]string{"Nanoflare-Upload": transfer.ID},
	})
}

func (m *MinIO) SignObjectTransfer(ctx context.Context, transfer nanoflare.ObjectTransfer, part int) (string, error) {
	ctx, cancel := m.operationContext(ctx)
	defer cancel()
	key, err := objectKey(objectTransferScope, transfer.UploadKey())
	if err != nil {
		return "", err
	}
	var query url.Values
	headers := make(http.Header)
	size := transfer.Size
	if part == 0 {
		// The single-part body becomes immutable as soon as its first PUT succeeds.
		headers.Set("If-None-Match", "*")
	} else {
		size = transfer.Parts[part-1]
		query = url.Values{"uploadId": {transfer.UploadID}, "partNumber": {strconv.Itoa(part)}}
	}
	headers.Set("Content-Length", strconv.FormatInt(size, 10))
	expiry := time.Until(transfer.ExpiresAt).Truncate(time.Second)
	if expiry < time.Second {
		return "", nanoflare.ErrObjectTransferNotFound
	}
	signed, err := m.uploadSigner.PresignHeader(ctx, http.MethodPut, m.bucket, key, expiry, query, headers)
	if err != nil {
		return "", err
	}
	signedHeaders := signed.Query().Get("X-Amz-SignedHeaders")
	if !strings.Contains(signedHeaders, "content-length") || (part == 0 && !strings.Contains(signedHeaders, "if-none-match")) {
		return "", errors.New("storage provider signer omitted upload constraints")
	}
	return signed.String(), nil
}

func (m *MinIO) CompleteMultipartObjectTransfer(ctx context.Context, transfer nanoflare.ObjectTransfer) error {
	ctx, cancel := m.operationContext(ctx)
	defer cancel()
	key, err := objectKey(objectTransferScope, transfer.UploadKey())
	if err != nil {
		return err
	}
	if info, err := m.client.StatObject(ctx, m.bucket, key, minio.StatObjectOptions{}); err == nil {
		if !transfer.DirectMultipart || info.Metadata.Get(objectTransferMetadata) == transfer.ID {
			return nil
		}
	} else if !errors.Is(minioNotFound(err), nanoflare.ErrObjectNotFound) {
		return err
	}
	core := minio.Core{Client: m.client}
	parts := make([]minio.CompletePart, 0, len(transfer.Parts))
	marker := 0
	for {
		page, err := core.ListObjectParts(ctx, m.bucket, key, transfer.UploadID, marker, 1000)
		if err != nil {
			return err
		}
		for _, part := range page.ObjectParts {
			index := len(parts)
			if index >= len(transfer.Parts) || part.PartNumber != index+1 || part.Size != transfer.Parts[index] || part.ETag == "" {
				return fmt.Errorf("%w: unexpected size or ETag for part %d", nanoflare.ErrObjectTransferInput, part.PartNumber)
			}
			parts = append(parts, minio.CompletePart{PartNumber: part.PartNumber, ETag: part.ETag})
		}
		if !page.IsTruncated {
			break
		}
		if page.NextPartNumberMarker <= marker {
			return errors.New("storage provider did not advance part listing")
		}
		marker = page.NextPartNumberMarker
	}
	if len(parts) != len(transfer.Parts) {
		return fmt.Errorf("%w: upload is missing parts", nanoflare.ErrObjectTransferInput)
	}
	_, err = core.CompleteMultipartUpload(ctx, m.bucket, key, transfer.UploadID, parts, minio.PutObjectOptions{})
	return err
}

func (m *MinIO) PublishObjectTransfer(ctx context.Context, transfer nanoflare.ObjectTransfer, destination string) (nanoflare.ObjectInfo, error) {
	ctx, cancel := m.operationContext(ctx)
	defer cancel()
	source, err := objectKey(objectTransferScope, transfer.StagingKey())
	if err != nil {
		return nanoflare.ObjectInfo{}, err
	}
	key, err := objectKey(objectTransferScope, destination)
	if err != nil {
		return nanoflare.ObjectInfo{}, err
	}
	current, err := m.client.StatObject(ctx, m.bucket, key, minio.StatObjectOptions{})
	if err == nil && current.Size == transfer.Size && current.Metadata.Get(objectTransferMetadata) == transfer.ID {
		return objectInfo(destination, current), nil
	}
	if err != nil && !errors.Is(minioNotFound(err), nanoflare.ErrObjectNotFound) {
		return nanoflare.ObjectInfo{}, err
	}
	if transfer.DirectMultipart {
		return nanoflare.ObjectInfo{}, errors.New("completed multipart object is missing or has unexpected metadata")
	}
	info, err := m.client.StatObject(ctx, m.bucket, source, minio.StatObjectOptions{})
	if errors.Is(minioNotFound(err), nanoflare.ErrObjectNotFound) {
		return nanoflare.ObjectInfo{}, fmt.Errorf("%w: staged upload is missing", nanoflare.ErrObjectTransferInput)
	}
	if err != nil {
		return nanoflare.ObjectInfo{}, err
	}
	if info.Size != transfer.Size || info.ETag == "" || info.Metadata.Get("X-Amz-Meta-Nanoflare-Discarded") != "" {
		return nanoflare.ObjectInfo{}, fmt.Errorf("%w: staged object does not match reserved upload", nanoflare.ErrObjectTransferInput)
	}
	src := minio.CopySrcOptions{Bucket: m.bucket, Object: source, VersionID: info.VersionID, MatchETag: info.ETag}
	dst := minio.CopyDestOptions{
		Bucket: m.bucket, Object: key, ReplaceMetadata: true,
		ContentType: info.ContentType, UserMetadata: map[string]string{"Nanoflare-Upload": transfer.ID},
	}
	if info.Size <= 5<<30 {
		_, err = m.client.CopyObject(ctx, dst, src)
	} else {
		_, err = m.client.ComposeObject(ctx, dst, src)
	}
	if err != nil {
		return nanoflare.ObjectInfo{}, err
	}
	return m.HeadContext(ctx, objectTransferScope, destination)
}

func (m *MinIO) CleanupObjectTransfer(ctx context.Context, transfer nanoflare.ObjectTransfer) error {
	ctx, cancel := m.operationContext(ctx)
	defer cancel()
	key, err := objectKey(objectTransferScope, transfer.UploadKey())
	if err != nil {
		return err
	}
	if transfer.DirectMultipart && transfer.UploadID == "" {
		return nil
	}
	core := minio.Core{Client: m.client}
	if transfer.UploadID != "" {
		err := core.AbortMultipartUpload(ctx, m.bucket, key, transfer.UploadID)
		if err != nil && minio.ToErrorResponse(err).Code != "NoSuchUpload" {
			return err
		}
	} else if len(transfer.Parts) != 0 {
		// Recover an upload created just before the process lost its durable save.
		uploads, err := core.ListMultipartUploads(ctx, m.bucket, key, "", "", "", 1000)
		if err != nil {
			return err
		}
		for _, upload := range uploads.Uploads {
			if upload.Key == key {
				err := core.AbortMultipartUpload(ctx, m.bucket, key, upload.UploadID)
				if err != nil && minio.ToErrorResponse(err).Code != "NoSuchUpload" {
					return err
				}
			}
		}
	}
	if transfer.DirectMultipart {
		return nil
	}
	if len(transfer.Parts) == 0 && time.Now().Before(transfer.ExpiresAt.Add(time.Hour)) {
		// Keep a zero-byte tombstone so a still-valid conditional PUT cannot
		// recreate the staging payload after completion or abort.
		_, err := m.client.PutObject(ctx, m.bucket, key, strings.NewReader(""), 0, minio.PutObjectOptions{
			UserMetadata: map[string]string{"Nanoflare-Discarded": "true"},
		})
		return err
	}
	return m.client.RemoveObject(ctx, m.bucket, key, minio.RemoveObjectOptions{})
}
