package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// HybridStorage makes an object store the only write target while keeping the
// old local upload volume readable during migration. The local backend is
// deliberately not exposed as a general Storage implementation to callers:
// Upload and UploadStream always dispatch to primary, and GetReader falls
// back only for a confirmed missing object.
type HybridStorage struct {
	primary Storage
	legacy  *LocalStorage
}

func NewHybridStorage(primary Storage, legacy *LocalStorage) *HybridStorage {
	if primary == nil {
		return nil
	}
	return &HybridStorage{primary: primary, legacy: legacy}
}

func (s *HybridStorage) CdnDomain() string {
	return s.primary.CdnDomain()
}

func (s *HybridStorage) ObjectURL(key string) string {
	return s.primary.ObjectURL(key)
}

// ResolveURL selects the backend that originally produced a persisted URL.
// It is used by read-only compatibility code such as avatar resolution. New
// object URLs are always recognized by the primary backend; legacy /uploads
// URLs are routed to the local backend even when the same logical key has
// already been copied to MinIO.
func (s *HybridStorage) ResolveURL(rawURL string) (Storage, string) {
	if s.primary != nil {
		key := s.primary.KeyFromURL(rawURL)
		if key != "" && s.primary.ObjectURL(key) == rawURL {
			return s.primary, key
		}
	}
	if s.legacy != nil && isLegacyUploadURL(rawURL) {
		return s.legacy, s.legacy.KeyFromURL(rawURL)
	}
	if s.primary != nil {
		return s.primary, s.primary.KeyFromURL(rawURL)
	}
	return s.legacy, s.legacy.KeyFromURL(rawURL)
}

func (s *HybridStorage) KeyFromURL(rawURL string) string {
	_, key := s.ResolveURL(rawURL)
	return key
}

func (s *HybridStorage) Upload(ctx context.Context, key string, data []byte, contentType string, filename string) (string, error) {
	return s.primary.Upload(ctx, key, data, contentType, filename)
}

// UploadStream keeps streaming producers (channel media and source-context
// clones) on the object-store path. A small compatibility fallback buffers
// only when a custom primary implementation does not advertise streaming.
func (s *HybridStorage) UploadStream(ctx context.Context, key string, data io.Reader, sizeBytes int64, contentType string, filename string) (string, error) {
	if streamer, ok := s.primary.(interface {
		UploadStream(context.Context, string, io.Reader, int64, string, string) (string, error)
	}); ok {
		return streamer.UploadStream(ctx, key, data, sizeBytes, contentType, filename)
	}
	contents, err := io.ReadAll(data)
	if err != nil {
		return "", fmt.Errorf("hybrid storage buffer stream: %w", err)
	}
	return s.primary.Upload(ctx, key, contents, contentType, filename)
}

func (s *HybridStorage) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	presigner, ok := s.primary.(Presigner)
	if !ok {
		return "", fmt.Errorf("primary storage does not support presigned downloads")
	}
	return presigner.PresignGet(ctx, key, ttl)
}

func (s *HybridStorage) PresignGetWithContentDisposition(ctx context.Context, key string, ttl time.Duration, contentDisposition string) (string, error) {
	presigner, ok := s.primary.(DownloadPresigner)
	if !ok {
		return "", fmt.Errorf("primary storage does not support presigned downloads")
	}
	return presigner.PresignGetWithContentDisposition(ctx, key, ttl, contentDisposition)
}

func (s *HybridStorage) GetReader(ctx context.Context, key string) (io.ReadCloser, error) {
	reader, err := s.primary.GetReader(ctx, key)
	if err == nil {
		return reader, nil
	}
	if s.legacy == nil || !isNotFoundError(err) {
		return nil, err
	}
	legacyReader, legacyErr := s.legacy.GetReader(ctx, key)
	if legacyErr == nil {
		slog.Debug("hybrid storage served legacy local object", "key", key)
		return legacyReader, nil
	}
	return nil, errors.Join(err, legacyErr)
}

func (s *HybridStorage) Delete(ctx context.Context, key string) {
	if err := s.DeleteObject(ctx, key); err != nil {
		slog.Error("hybrid storage Delete failed", "key", key, "error", err)
	}
}

// DeleteObject removes the primary object and the legacy copy. Deleting both
// is intentional: an attachment row may still point at either URL during the
// migration window, and both deletes are idempotent for their backends.
func (s *HybridStorage) DeleteObject(ctx context.Context, key string) error {
	var errs []error
	if err := s.primary.DeleteObject(ctx, key); err != nil {
		errs = append(errs, fmt.Errorf("primary object: %w", err))
	}
	if s.legacy != nil {
		if err := s.legacy.DeleteObject(ctx, key); err != nil {
			errs = append(errs, fmt.Errorf("legacy object: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (s *HybridStorage) DeleteKeys(ctx context.Context, keys []string) {
	for _, key := range keys {
		s.Delete(ctx, key)
	}
}

// ServeFile is the compatibility implementation for the public /uploads/*
// route. It never writes to local disk and is absent when no legacy backend
// was configured.
func (s *HybridStorage) ServeFile(w http.ResponseWriter, r *http.Request, filename string) {
	if s.legacy == nil {
		http.NotFound(w, r)
		return
	}
	s.legacy.ServeFile(w, r, filename)
}

func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) || os.IsNotExist(err) || isS3NotFoundError(err) {
		return true
	}
	return false
}

func isLegacyUploadURL(rawURL string) bool {
	if strings.HasPrefix(rawURL, "/uploads/") {
		return true
	}
	parsed, err := url.Parse(rawURL)
	return err == nil && strings.HasPrefix(parsed.Path, "/uploads/")
}
