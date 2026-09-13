package storage

import (
	"bytes"
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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

type S3Storage struct {
	client            *s3.Client
	bucket            string
	region            string // used to construct virtual-hosted-style public URLs when no CDN/endpoint is set
	cdnDomain         string // if set, returned URLs use this instead of bucket name
	endpointURL       string // custom S3-compatible endpoint (e.g. MinIO)
	publicEndpointURL string // optional reader-facing endpoint; uploads still use endpointURL
	usePathStyle      bool   // controls path-style S3 addressing
}

// NewS3StorageFromEnv creates an S3Storage from environment variables.
// Returns nil if neither S3_BUCKET nor MINIO_BUCKET is set.
//
// Environment variables:
//   - MINIO_BUCKET / MINIO_ENDPOINT / MINIO_ACCESS_KEY / MINIO_SECRET_KEY /
//     MINIO_USE_SSL (optional MinIO-native names; take precedence when set)
//   - S3_BUCKET (required when MinIO is not configured)
//   - S3_REGION (default: us-west-2; MinIO defaults to us-east-1)
//   - AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY (optional; falls back to default credential chain)
//   - AWS_ENDPOINT_URL (optional S3-compatible endpoint)
//   - MINIO_PUBLIC_ENDPOINT (optional reader-facing MinIO endpoint; when it
//     differs from MINIO_ENDPOINT, uploads still use MINIO_ENDPOINT)
//   - S3_USE_PATH_STYLE (optional; defaults to true when a custom endpoint is set)
func NewS3StorageFromEnv() *S3Storage {
	minioConfigured := firstNonEmptyEnv("MINIO_BUCKET", "MINIO_ENDPOINT", "MINIO_ACCESS_KEY", "MINIO_SECRET_KEY") != ""
	bucket := firstNonEmptyEnv("MINIO_BUCKET", "S3_BUCKET")
	if bucket == "" {
		slog.Info("S3_BUCKET/MINIO_BUCKET not set, cloud upload disabled")
		return nil
	}
	if looksLikeS3Hostname(bucket) {
		slog.Warn(
			"S3_BUCKET looks like a hostname rather than a bucket name — uploads and public URLs will likely both fail. Use only the bucket name (e.g. \"my-bucket\"), not \"<bucket>.s3.<region>.amazonaws.com\".",
			"value", bucket,
		)
	}

	region := firstNonEmptyEnv("MINIO_REGION", "S3_REGION")
	if region == "" {
		region = "us-west-2"
		if minioConfigured {
			region = "us-east-1"
		}
	}

	opts := []func(*config.LoadOptions) error{
		config.WithRegion(region),
	}

	accessKey := firstNonEmptyEnv("MINIO_ACCESS_KEY", "AWS_ACCESS_KEY_ID")
	secretKey := firstNonEmptyEnv("MINIO_SECRET_KEY", "AWS_SECRET_ACCESS_KEY")
	if accessKey != "" && secretKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		))
	}

	cfg, err := config.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		slog.Error("failed to load AWS config", "error", err)
		return nil
	}

	cdnDomain := os.Getenv("CLOUDFRONT_DOMAIN")

	endpointURL := firstNonEmptyEnv("MINIO_ENDPOINT", "AWS_ENDPOINT_URL")
	if minioConfigured {
		endpointURL = normalizeS3Endpoint(endpointURL, envBoolOrDefault("MINIO_USE_SSL", false))
	} else {
		endpointURL = normalizeS3Endpoint(endpointURL, true)
	}
	publicEndpointURL := endpointURL
	if publicEndpoint := firstNonEmptyEnv("MINIO_PUBLIC_ENDPOINT"); publicEndpoint != "" {
		publicEndpointURL = normalizeS3Endpoint(publicEndpoint, envBoolOrDefault("MINIO_USE_SSL", false))
	}
	usePathStyle := s3UsePathStyleFromEnv(endpointURL)
	if minioConfigured && !hasEnvValue("MINIO_USE_PATH_STYLE") && !hasEnvValue("S3_USE_PATH_STYLE") {
		usePathStyle = true
	}
	s3Opts := []func(*s3.Options){}
	if endpointURL != "" || usePathStyle {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			if endpointURL != "" {
				o.BaseEndpoint = aws.String(endpointURL)
			}
			o.UsePathStyle = usePathStyle
		})
	}

	slog.Info("S3 storage initialized", "bucket", bucket, "region", region, "cdn_domain", cdnDomain, "endpoint_url", endpointURL, "public_endpoint_url", publicEndpointURL, "use_path_style", usePathStyle)
	return &S3Storage{
		client:            s3.NewFromConfig(cfg, s3Opts...),
		bucket:            bucket,
		region:            region,
		cdnDomain:         cdnDomain,
		endpointURL:       endpointURL,
		publicEndpointURL: publicEndpointURL,
		usePathStyle:      usePathStyle,
	}
}

func firstNonEmptyEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func hasEnvValue(name string) bool {
	value, ok := os.LookupEnv(name)
	return ok && strings.TrimSpace(value) != ""
}

func envBoolOrDefault(name string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := parseBoolEnv(raw)
	if err != nil {
		slog.Warn("invalid boolean storage environment value, using default", "name", name, "value", raw, "default", fallback)
		return fallback
	}
	return value
}

func normalizeS3Endpoint(endpoint string, useSSL bool) string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" || strings.Contains(endpoint, "://") {
		return endpoint
	}
	scheme := "http"
	if useSSL {
		scheme = "https"
	}
	return scheme + "://" + endpoint
}

func (s *S3Storage) CdnDomain() string {
	return s.cdnDomain
}

// looksLikeS3Hostname returns true when the configured S3_BUCKET value looks
// like an S3 endpoint hostname rather than a bucket name. Real bucket names
// can never legitimately contain "amazonaws.com", so this is an unambiguous
// misconfiguration signal — the most common form being users pasting
// "<bucket>.s3.<region>.amazonaws.com" into S3_BUCKET.
func looksLikeS3Hostname(bucket string) bool {
	return strings.Contains(bucket, "amazonaws.com")
}

func s3UsePathStyleFromEnv(endpointURL string) bool {
	defaultValue := endpointURL != ""
	raw, ok := os.LookupEnv("S3_USE_PATH_STYLE")
	if !ok || strings.TrimSpace(raw) == "" {
		return defaultValue
	}
	parsed, err := parseBoolEnv(raw)
	if err != nil {
		slog.Warn("invalid S3_USE_PATH_STYLE value, using default", "value", raw, "default", defaultValue)
		return defaultValue
	}
	return parsed
}

func parseBoolEnv(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "t", "true", "y", "yes", "on":
		return true, nil
	case "0", "f", "false", "n", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid bool %q", raw)
	}
}

// awsEndpointDomains are the parent domains AWS serves S3 from. VPC, dualstack
// and FIPS hostnames all live under them, as does the China partition.
var awsEndpointDomains = []string{"amazonaws.com", "amazonaws.com.cn"}

// usesAWSEndpoint reports whether uploads go to real AWS S3 — either the
// default endpoint or an explicitly configured AWS one. The match is on the
// host label boundary, so "notamazonaws.com" and "s3.amazonaws.com.evil.net"
// are correctly treated as third-party endpoints.
func (s *S3Storage) usesAWSEndpoint() bool {
	if s.endpointURL == "" {
		return true
	}
	host := endpointHostname(s.endpointURL)
	for _, domain := range awsEndpointDomains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// endpointHostname extracts the comparable hostname from a configured
// endpoint: lowercased, without the port, and without a trailing root dot.
// A value written without a scheme is retried as an https URL, since
// url.Parse otherwise reads the whole thing as a path.
func endpointHostname(endpointURL string) string {
	parsed, err := url.Parse(endpointURL)
	if err != nil {
		return ""
	}
	if parsed.Hostname() == "" {
		parsed, err = url.Parse("https://" + endpointURL)
		if err != nil {
			return ""
		}
	}
	return strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
}

// uploadChecksumOptions returns the per-request options for the buffered
// upload path. The SDK defaults RequestChecksumCalculation to WhenSupported,
// which sends the body as aws-chunked with a CRC32 trailer; Aliyun OSS and
// Tencent COS do not implement that encoding and reject the request outright.
//
// Downgrading to WhenRequired drops the trailer, but it also drops the
// client-side checksum entirely, so it is applied only to non-AWS endpoints.
// Real AWS S3 accepts the trailer today, and buckets carrying a default Object
// Lock retention actually require a checksum, so those requests are left
// exactly as the SDK built them — including any AWS_REQUEST_CHECKSUM_CALCULATION
// the operator configured.
func (s *S3Storage) uploadChecksumOptions() []func(*s3.Options) {
	if s.usesAWSEndpoint() {
		return nil
	}
	return []func(*s3.Options){func(opts *s3.Options) {
		opts.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	}}
}

// storageClass returns the appropriate S3 storage class.
// Custom endpoints (e.g. MinIO) only support STANDARD; real AWS defaults to INTELLIGENT_TIERING.
func (s *S3Storage) storageClass() types.StorageClass {
	if s.endpointURL != "" {
		return types.StorageClassStandard
	}
	return types.StorageClassIntelligentTiering
}

// KeyFromURL extracts the S3 object key from a CDN or bucket URL.
// e.g. "https://multica-static.copilothub.ai/abc123.png" → "abc123.png"
//
//	"https://my-bucket.s3.us-east-1.amazonaws.com/uploads/x/y.png" → "uploads/x/y.png"
func (s *S3Storage) KeyFromURL(rawURL string) string {
	for _, endpointURL := range []string{s.publicEndpointURL, s.endpointURL} {
		if endpointURL == "" {
			continue
		}
		for _, prefix := range []string{
			customEndpointObjectPrefix(endpointURL, s.bucket, true),
			customEndpointObjectPrefix(endpointURL, s.bucket, false),
		} {
			if strings.HasPrefix(rawURL, prefix) {
				return strings.TrimPrefix(rawURL, prefix)
			}
		}
	}

	// Strip known "https://host/" prefixes. Order matters: the more specific
	// region-qualified hosts come first so they win over the legacy bucket-only
	// prefix that we used to write before the suffix bug was fixed.
	prefixes := make([]string, 0, 5)
	if s.cdnDomain != "" {
		prefixes = append(prefixes, "https://"+s.cdnDomain+"/")
	}
	if s.region != "" {
		// virtual-hosted-style: https://<bucket>.s3.<region>.amazonaws.com/<key>
		prefixes = append(prefixes,
			"https://"+s.bucket+".s3."+s.region+".amazonaws.com/",
			// path-style: https://s3.<region>.amazonaws.com/<bucket>/<key>
			"https://s3."+s.region+".amazonaws.com/"+s.bucket+"/",
		)
	}
	// Legacy / fallback: the buggy "https://<bucket>/<key>" form that older
	// records may still hold, plus a generic bucket-host prefix.
	prefixes = append(prefixes, "https://"+s.bucket+"/")

	for _, prefix := range prefixes {
		if strings.HasPrefix(rawURL, prefix) {
			return strings.TrimPrefix(rawURL, prefix)
		}
	}
	// Fallback: take everything after the last "/".
	if i := strings.LastIndex(rawURL, "/"); i >= 0 {
		return rawURL[i+1:]
	}
	return rawURL
}

// GetReader streams the object body back to the caller. The returned
// ReadCloser must be closed; closing it terminates the underlying HTTP
// connection to S3. A missing key surfaces as an *types.NoSuchKey error
// wrapped in the SDK's smithy wrapper — callers can use errors.As to
// distinguish "not found" from a transport failure.
func (s *S3Storage) GetReader(ctx context.Context, key string) (io.ReadCloser, error) {
	if key == "" {
		return nil, fmt.Errorf("s3 GetReader: empty key")
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 GetObject: %w", err)
	}
	return out.Body, nil
}

// isS3NotFoundError classifies the SDK's API and HTTP wrappers without
// depending on one particular S3-compatible server's error type. It is used
// by the migration facade to fall back to a legacy local object only when the
// primary object is genuinely absent; network/authentication failures must
// remain visible instead of silently serving stale disk data.
func isS3NotFoundError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch strings.ToLower(apiErr.ErrorCode()) {
		case "nosuchkey", "notfound", "nosuchobject", "nosuchbucket", "no such key":
			return true
		}
	}
	var responseErr *smithyhttp.ResponseError
	return errors.As(err, &responseErr) && responseErr.HTTPStatusCode() == http.StatusNotFound
}

func (s *S3Storage) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return s.PresignGetWithContentDisposition(ctx, key, ttl, "")
}

func (s *S3Storage) PresignGetWithContentDisposition(ctx context.Context, key string, ttl time.Duration, contentDisposition string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("s3 PresignGet: empty key")
	}
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	input := &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}
	if contentDisposition != "" {
		input.ResponseContentDisposition = aws.String(contentDisposition)
	}
	out, err := s3.NewPresignClient(s.client).PresignGetObject(ctx, input, func(opts *s3.PresignOptions) {
		opts.Expires = ttl
	})
	if err != nil {
		return "", fmt.Errorf("s3 PresignGetObject: %w", err)
	}
	return out.URL, nil
}

// Delete removes an object from S3. Errors are logged but not fatal.
func (s *S3Storage) Delete(ctx context.Context, key string) {
	if err := s.DeleteObject(ctx, key); err != nil {
		slog.Error("s3 DeleteObject failed", "key", key, "error", err)
	}
}

// DeleteObject is Delete with the error surfaced — the media reconciler needs
// it to keep the ledger row and schedule a retry instead of assuming success.
func (s *S3Storage) DeleteObject(ctx context.Context, key string) error {
	if key == "" {
		return nil
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err
}

// ObjectURL returns the URL a successful Upload/UploadStream of key would
// return. It is a pure function of configuration, so the media intent ledger
// can persist the URL BEFORE the upload for the durable reference check.
func (s *S3Storage) ObjectURL(key string) string {
	return s.uploadedURL(key)
}

// DeleteKeys removes multiple objects from S3. Best-effort, errors are logged.
func (s *S3Storage) DeleteKeys(ctx context.Context, keys []string) {
	for _, key := range keys {
		s.Delete(ctx, key)
	}
}

func (s *S3Storage) Upload(ctx context.Context, key string, data []byte, contentType string, filename string) (string, error) {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:             aws.String(s.bucket),
		Key:                aws.String(key),
		Body:               bytes.NewReader(data),
		ContentType:        aws.String(contentType),
		ContentDisposition: aws.String(ContentDisposition(contentType, filename)),
		CacheControl:       aws.String("max-age=432000,public"),
		StorageClass:       s.storageClass(),
	}, s.uploadChecksumOptions()...)
	if err != nil {
		return "", fmt.Errorf("s3 PutObject: %w", err)
	}
	return s.uploadedURL(key), nil
}

func (s *S3Storage) UploadStream(ctx context.Context, key string, data io.Reader, sizeBytes int64, contentType string, filename string) (string, error) {
	if sizeBytes < 0 {
		return "", fmt.Errorf("s3 PutObject: content length is required for streaming upload")
	}
	input := &s3.PutObjectInput{
		Bucket:             aws.String(s.bucket),
		Key:                aws.String(key),
		Body:               data,
		ContentLength:      aws.Int64(sizeBytes),
		ContentType:        aws.String(contentType),
		ContentDisposition: aws.String(ContentDisposition(contentType, filename)),
		CacheControl:       aws.String("max-age=432000,public"),
		StorageClass:       s.storageClass(),
	}
	_, err := s.client.PutObject(ctx, input, func(opts *s3.Options) {
		// A non-seekable stream cannot be rewound for SigV4 payload hashing.
		// S3 supports UNSIGNED-PAYLOAD for authenticated requests. Avoid the
		// optional trailing-checksum path as well: it requires another rewind
		// or aws-chunked framing that S3-compatible backends handle unevenly.
		opts.APIOptions = append(opts.APIOptions, v4.SwapComputePayloadSHA256ForUnsignedPayloadMiddleware)
		opts.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})
	if err != nil {
		return "", fmt.Errorf("s3 PutObject: %w", err)
	}
	return s.uploadedURL(key), nil
}

// uploadedURL returns the URL stored for client consumption after an upload.
// Priority: CDN domain > custom endpoint > AWS S3 region-qualified host. The CDN
// domain wins even when a custom endpoint is set so S3-compatible backends
// (MinIO, R2, B2, Wasabi, etc.) can be paired with a separate public-read
// domain — writes still go through the SDK with the custom endpoint; only the
// reader-facing URL changes.
//
// For the default AWS S3 case, virtual-hosted-style is preferred:
// https://<bucket>.s3.<region>.amazonaws.com/<key>. When the bucket name
// contains dots, the AWS-issued wildcard TLS certificate (`*.s3.amazonaws.com`)
// fails to validate the host, so we fall back to path-style:
// https://s3.<region>.amazonaws.com/<bucket>/<key>.
func (s *S3Storage) uploadedURL(key string) string {
	if s.cdnDomain != "" {
		return fmt.Sprintf("https://%s/%s", s.cdnDomain, key)
	}
	if endpointURL := s.publicEndpoint(); endpointURL != "" {
		return customEndpointObjectURL(endpointURL, s.bucket, key, s.usePathStyle)
	}
	if s.usePathStyle || strings.Contains(s.bucket, ".") {
		return fmt.Sprintf("https://s3.%s.amazonaws.com/%s/%s", s.region, s.bucket, key)
	}
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", s.bucket, s.region, key)
}

func (s *S3Storage) publicEndpoint() string {
	if s.publicEndpointURL != "" {
		return s.publicEndpointURL
	}
	return s.endpointURL
}

func customEndpointObjectURL(endpointURL, bucket, key string, usePathStyle bool) string {
	return customEndpointObjectPrefix(endpointURL, bucket, usePathStyle) + key
}

func customEndpointObjectPrefix(endpointURL, bucket string, usePathStyle bool) string {
	trimmed := strings.TrimRight(endpointURL, "/")
	if usePathStyle {
		return trimmed + "/" + bucket + "/"
	}

	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return trimmed + "/"
	}
	u.Host = bucket + "." + u.Host
	u.Path = strings.TrimRight(u.Path, "/") + "/"
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
