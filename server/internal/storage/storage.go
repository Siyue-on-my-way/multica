package storage

import (
	"context"
	"io"
	"net/http"
	"time"
)

type Storage interface {
	Upload(ctx context.Context, key string, data []byte, contentType string, filename string) (string, error)
	Delete(ctx context.Context, key string)
	// DeleteObject is Delete with the error surfaced — the channel-media
	// reconciler schedules retries on failure instead of assuming success.
	DeleteObject(ctx context.Context, key string) error
	DeleteKeys(ctx context.Context, keys []string)
	KeyFromURL(rawURL string) string
	// ObjectURL is the URL a successful Upload of key would return — a pure
	// function of configuration, so the media intent ledger can persist it
	// BEFORE the upload.
	ObjectURL(key string) string
	CdnDomain() string
	// GetReader streams an object back to the caller. Used by the attachment
	// preview proxy (GET /api/attachments/{id}/content) to bypass CloudFront
	// CORS and the inline/attachment Content-Disposition decision. Caller
	// must Close the returned reader.
	GetReader(ctx context.Context, key string) (io.ReadCloser, error)
}

type Presigner interface {
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
}

type DownloadPresigner interface {
	PresignGetWithContentDisposition(ctx context.Context, key string, ttl time.Duration, contentDisposition string) (string, error)
}

// URLResolver is implemented by a storage facade that can distinguish the
// backend which produced a persisted object URL. It is intentionally
// optional: the common single-backend storage implementations only need the
// Storage methods above, while a migration facade can use it for legacy URLs
// without changing the public storage contract.
type URLResolver interface {
	ResolveURL(rawURL string) (Storage, string)
}

// LocalUploadServer is the small optional surface used by the compatibility
// /uploads/* route. A hybrid backend implements it with its legacy local
// reader; the primary backend remains responsible for every new write.
type LocalUploadServer interface {
	ServeFile(w http.ResponseWriter, r *http.Request, filename string)
}
