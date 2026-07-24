// Package storage implements the domain.BlobStore port on MinIO / any
// S3-compatible object store.
package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/unisghimire/strato/internal/config"
	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/pkg/retry"
)

// MinioStore is the production BlobStore.
type MinioStore struct {
	client *minio.Client
	bucket string
}

// NewMinioStore connects, and creates the bucket if missing (idempotent, so
// replicas racing at startup are fine).
func NewMinioStore(ctx context.Context, cfg config.S3) (*MinioStore, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("creating minio client: %w", err)
	}

	s := &MinioStore{client: client, bucket: cfg.Bucket}
	err = retry.Do(ctx, retry.DefaultPolicy(), func(ctx context.Context) error {
		exists, err := client.BucketExists(ctx, cfg.Bucket)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
		err = client.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{Region: cfg.Region})
		if err != nil {
			// Lost the creation race to another replica: that's success.
			if exists, checkErr := client.BucketExists(ctx, cfg.Bucket); checkErr == nil && exists {
				return nil
			}
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("ensuring bucket %q: %w", cfg.Bucket, err)
	}
	return s, nil
}

var _ domain.BlobStore = (*MinioStore)(nil)

// Put streams r into the object. size must be exact; PutObject uses it to
// pick single-shot vs multipart and to set Content-Length (no buffering).
func (s *MinioStore) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	if size < 0 {
		return fmt.Errorf("storage: exact size required, got %d", size)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	_, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return fmt.Errorf("storage put %q: %w", key, err)
	}
	return nil
}

// Get opens a streaming reader. minio defers the actual request to the first
// Read, so we Stat first to surface not-found errors at call time.
func (s *MinioStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("storage get %q: %w", key, err)
	}
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, fmt.Errorf("storage get %q: %w", key, domain.ErrNotFound)
		}
		return nil, fmt.Errorf("storage stat %q: %w", key, err)
	}
	return obj, nil
}

// Delete removes an object. Deleting a missing key is not an error
// (idempotent GC).
func (s *MinioStore) Delete(ctx context.Context, key string) error {
	err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("storage delete %q: %w", key, err)
	}
	return nil
}

// SignedGetURL mints a presigned GET with a download filename. Used only for
// content the server does not need to transform; encrypted blobs stream
// through the API instead.
func (s *MinioStore) SignedGetURL(ctx context.Context, key, filename string, ttl time.Duration) (string, error) {
	params := make(url.Values)
	params.Set("response-content-disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	u, err := s.client.PresignedGetObject(ctx, s.bucket, key, ttl, params)
	if err != nil {
		return "", fmt.Errorf("storage presign %q: %w", key, err)
	}
	return u.String(), nil
}
