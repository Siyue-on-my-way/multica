// migrate_local_files_to_minio copies the legacy local upload volume to the
// configured S3-compatible object store and then rewrites the durable
// references in PostgreSQL. It deliberately lives outside the API process:
// operators can stop the service, take a backup, and run the migration with a
// bounded, auditable change window.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/storage"
)

const defaultRetainFor = "30 days"

type objectReference struct {
	objectType  string
	objectID    string
	workspaceID string
	ownerTable  string
	ownerID     string
	sourceURL   string
	sourceKey   string
	filename    string
	contentType string
	sizeBytes   int64
}

type localObject struct {
	key         string
	path        string
	sizeBytes   int64
	sha256      string
	filename    string
	contentType string
}

type referenceQuery struct {
	objectType string
	ownerTable string
	sql        string
}

func main() {
	var (
		apply        = flag.Bool("apply", false, "upload objects and rewrite database references")
		deleteLegacy = flag.Bool("delete-legacy", false, "delete local objects whose retention window has elapsed; requires --apply")
		retainFor    = flag.String("retain-for", envOrDefault("MIGRATE_MINIO_RETAIN_FOR", defaultRetainFor), "PostgreSQL interval to keep the legacy object after a successful migration")
	)
	flag.Parse()

	if *deleteLegacy && !*apply {
		fatal(errors.New("--delete-legacy requires --apply"))
	}

	ctx := context.Background()
	root := strings.TrimSpace(os.Getenv("LOCAL_UPLOAD_DIR"))
	if root == "" {
		root = "./data/uploads"
	}
	root, err := filepath.Abs(root)
	if err != nil {
		fatal(fmt.Errorf("resolve LOCAL_UPLOAD_DIR: %w", err))
	}

	objects, err := scanLocalObjects(root)
	if err != nil {
		fatal(err)
	}

	databaseURL := envOrDefault("DATABASE_URL", os.Getenv("POSTGRES_URL"))
	if databaseURL == "" {
		fatal(errors.New("DATABASE_URL or POSTGRES_URL is required"))
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		fatal(fmt.Errorf("connect PostgreSQL: %w", err))
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		fatal(fmt.Errorf("ping PostgreSQL: %w", err))
	}

	refs, err := loadReferences(ctx, pool)
	if err != nil {
		fatal(err)
	}
	metadata := metadataByKey(refs, objects)
	candidates := make([]localObject, 0, len(objects))
	for _, object := range objects {
		if meta, ok := metadata[object.key]; ok {
			object.filename = meta.filename
			object.contentType = meta.contentType
		}
		if object.filename == "" {
			object.filename = filepath.Base(object.key)
		}
		if object.contentType == "" || strings.Contains(object.contentType, "*") {
			object.contentType = detectContentType(object.path, object.key)
		}
		candidates = append(candidates, object)
	}

	missing := missingReferenceKeys(refs, objects)
	fmt.Printf(
		"local objects: %d; database references: %d; referenced objects: %d; missing local objects: %d\n",
		len(objects), len(refs), len(metadata), len(missing),
	)
	for _, key := range missing {
		fmt.Printf("missing legacy object: %s\n", key)
	}
	if len(missing) > 0 {
		fatal(errors.New("one or more database references have no local file; no database rows were changed"))
	}

	store := storage.NewS3StorageFromEnv()
	if store == nil {
		fatal(errors.New("MinIO/S3 storage is not configured; set MINIO_BUCKET and credentials"))
	}
	targetURLs := make(map[string]string, len(candidates))
	for _, object := range candidates {
		targetURLs[object.key] = store.ObjectURL(object.key)
	}

	if !*apply {
		fmt.Println("dry-run only: no MinIO uploads, database updates, or local deletions were performed")
		return
	}

	for _, object := range candidates {
		if err := uploadAndVerify(ctx, store, object); err != nil {
			fatal(fmt.Errorf("migrate %s: %w", object.key, err))
		}
	}
	if err := rewriteReferences(ctx, pool, refs, objects, targetURLs, *retainFor); err != nil {
		fatal(err)
	}
	fmt.Printf("uploaded and rewrote %d database references; legacy retention: %s\n", len(refs), *retainFor)

	if *deleteLegacy {
		deleted, skipped, err := deleteEligibleLegacyObjects(ctx, pool, root)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("legacy deletion: deleted %d object(s), skipped %d object(s)\n", deleted, skipped)
	} else {
		fmt.Println("legacy files were retained; run again with --delete-legacy --apply after the retention window and stability checks")
	}
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}

func fatal(err error) {
	log.Printf("migration failed: %v", err)
	os.Exit(1)
}

func scanLocalObjects(root string) (map[string]localObject, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat legacy upload directory %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("legacy upload path is not a directory: %s", root)
	}

	objects := make(map[string]localObject)
	err = filepath.WalkDir(root, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(root, filePath)
		if err != nil {
			return fmt.Errorf("relative path for %s: %w", filePath, err)
		}
		key := filepath.ToSlash(rel)
		if isInternalLocalFile(key) {
			return nil
		}
		stat, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat legacy object %s: %w", key, err)
		}
		digest, size, err := fileDigest(filePath)
		if err != nil {
			return fmt.Errorf("hash legacy object %s: %w", key, err)
		}
		objects[key] = localObject{
			key:       key,
			path:      filePath,
			sizeBytes: size,
			sha256:    digest,
		}
		if stat.Size() != size {
			return fmt.Errorf("legacy object %s changed while hashing", key)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan legacy upload directory: %w", err)
	}
	return objects, nil
}

func isInternalLocalFile(key string) bool {
	if strings.HasSuffix(key, ".meta.json") {
		return true
	}
	base := path.Base(key)
	return strings.HasPrefix(base, ".") && strings.HasSuffix(base, ".tmp")
}

func fileDigest(filePath string) (string, int64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func detectContentType(filePath, key string) string {
	if value := mime.TypeByExtension(strings.ToLower(path.Ext(key))); value != "" {
		return value
	}
	file, err := os.Open(filePath)
	if err != nil {
		return "application/octet-stream"
	}
	defer file.Close()
	header := make([]byte, 512)
	count, err := file.Read(header)
	if err != nil && !errors.Is(err, io.EOF) {
		return "application/octet-stream"
	}
	return http.DetectContentType(header[:count])
}

func loadReferences(ctx context.Context, pool *pgxpool.Pool) ([]objectReference, error) {
	queries := []referenceQuery{
		{
			objectType: "attachment",
			ownerTable: "attachment",
			sql: `SELECT id::text, COALESCE(workspace_id::text, ''), url, filename, content_type, size_bytes::bigint, id::text
			      FROM attachment
			      WHERE url LIKE '%/uploads/%'`,
		},
		{
			objectType: "avatar",
			ownerTable: "user",
			sql: `SELECT id::text, '', avatar_url, '', 'image/*', 0::bigint, id::text
			      FROM "user"
			      WHERE avatar_url LIKE '%/uploads/%'`,
		},
		{
			objectType: "avatar",
			ownerTable: "agent",
			sql: `SELECT id::text, COALESCE(workspace_id::text, ''), avatar_url, '', 'image/*', 0::bigint, id::text
			      FROM agent
			      WHERE avatar_url LIKE '%/uploads/%'`,
		},
		{
			objectType: "avatar",
			ownerTable: "squad",
			sql: `SELECT id::text, COALESCE(workspace_id::text, ''), avatar_url, '', 'image/*', 0::bigint, id::text
			      FROM squad
			      WHERE avatar_url LIKE '%/uploads/%'`,
		},
		{
			objectType: "avatar",
			ownerTable: "workspace",
			sql: `SELECT id::text, id::text, avatar_url, '', 'image/*', 0::bigint, id::text
			      FROM workspace
			      WHERE avatar_url LIKE '%/uploads/%'`,
		},
		{
			objectType: "channel_media",
			ownerTable: "channel_media_pending_object",
			sql: `SELECT '', COALESCE(workspace_id::text, ''), storage_url, '', 'application/octet-stream', 0::bigint, chat_message_id::text
			      FROM channel_media_pending_object
			      WHERE storage_url LIKE '%/uploads/%'`,
		},
		{
			objectType: "source_context",
			ownerTable: "issue_source_context_object_intent",
			sql: `SELECT source_context_id::text, COALESCE(workspace_id::text, ''), object_url, '', 'application/octet-stream', 0::bigint, attachment_id::text
			      FROM issue_source_context_object_intent
			      WHERE object_url LIKE '%/uploads/%'`,
		},
	}

	var refs []objectReference
	for _, query := range queries {
		rows, err := pool.Query(ctx, query.sql)
		if err != nil {
			return nil, fmt.Errorf("load %s references: %w", query.ownerTable, err)
		}
		for rows.Next() {
			var ref objectReference
			if err := rows.Scan(
				&ref.objectID,
				&ref.workspaceID,
				&ref.sourceURL,
				&ref.filename,
				&ref.contentType,
				&ref.sizeBytes,
				&ref.ownerID,
			); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan %s reference: %w", query.ownerTable, err)
			}
			key, err := legacyKeyFromURL(ref.sourceURL)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("parse %s legacy URL %q: %w", query.ownerTable, ref.sourceURL, err)
			}
			ref.objectType = query.objectType
			ref.ownerTable = query.ownerTable
			ref.sourceKey = key
			refs = append(refs, ref)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read %s references: %w", query.ownerTable, err)
		}
		rows.Close()
	}
	return refs, nil
}

func legacyKeyFromURL(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", errors.New("empty URL")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	urlPath := parsed.Path
	if urlPath == "" && strings.HasPrefix(rawURL, "/uploads/") {
		urlPath = rawURL
	}
	const prefix = "/uploads/"
	index := strings.Index(urlPath, prefix)
	if index < 0 {
		return "", errors.New("URL is not under /uploads/")
	}
	key, err := url.PathUnescape(urlPath[index+len(prefix):])
	if err != nil {
		return "", fmt.Errorf("unescape key: %w", err)
	}
	key = path.Clean(strings.TrimPrefix(key, "/"))
	if key == "." || key == ".." || strings.HasPrefix(key, "../") || strings.ContainsRune(key, 0) {
		return "", errors.New("unsafe object key")
	}
	return key, nil
}

func metadataByKey(refs []objectReference, objects map[string]localObject) map[string]localObject {
	metadata := make(map[string]localObject)
	for _, ref := range refs {
		if _, ok := objects[ref.sourceKey]; !ok {
			continue
		}
		meta := metadata[ref.sourceKey]
		if meta.filename == "" && ref.filename != "" {
			meta.filename = filepath.Base(ref.filename)
		}
		if meta.contentType == "" && ref.contentType != "" && !strings.Contains(ref.contentType, "*") {
			meta.contentType = ref.contentType
		}
		metadata[ref.sourceKey] = meta
	}
	return metadata
}

func missingReferenceKeys(refs []objectReference, objects map[string]localObject) []string {
	seen := make(map[string]struct{})
	var missing []string
	for _, ref := range refs {
		if _, ok := objects[ref.sourceKey]; ok {
			continue
		}
		if _, ok := seen[ref.sourceKey]; ok {
			continue
		}
		seen[ref.sourceKey] = struct{}{}
		missing = append(missing, ref.sourceKey)
	}
	return missing
}

func uploadAndVerify(ctx context.Context, store *storage.S3Storage, object localObject) error {
	streamer, ok := any(store).(interface {
		UploadStream(context.Context, string, io.Reader, int64, string, string) (string, error)
	})
	if !ok {
		return errors.New("configured storage does not support streaming uploads")
	}
	file, err := os.Open(object.path)
	if err != nil {
		return err
	}
	_, uploadErr := streamer.UploadStream(ctx, object.key, file, object.sizeBytes, object.contentType, object.filename)
	closeErr := file.Close()
	if uploadErr != nil {
		return uploadErr
	}
	if closeErr != nil {
		return closeErr
	}

	reader, err := store.GetReader(ctx, object.key)
	if err != nil {
		return fmt.Errorf("verify object: %w", err)
	}
	defer reader.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, reader)
	if err != nil {
		return fmt.Errorf("read uploaded object for verification: %w", err)
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if size != object.sizeBytes || got != object.sha256 {
		return fmt.Errorf("verification mismatch: size %d/%d, sha256 %s/%s", size, object.sizeBytes, got, object.sha256)
	}
	return nil
}

const insertMigrationSQL = `
INSERT INTO file_storage_migration (
    object_type, object_id, workspace_id, owner_table, owner_id,
    source_url, source_key, target_key, target_url, status,
    sha256, size_bytes, legacy_delete_after, migrated_at, updated_at
) VALUES (
    $1, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, $4, NULLIF($5, '')::uuid,
    $6, $7, $8, $9, 'updated', $10, $11,
    now() + $12::interval, now(), now()
)
ON CONFLICT (object_type, source_url) DO UPDATE SET
    object_id = EXCLUDED.object_id,
    workspace_id = EXCLUDED.workspace_id,
    owner_table = EXCLUDED.owner_table,
    owner_id = EXCLUDED.owner_id,
    source_key = EXCLUDED.source_key,
    target_key = EXCLUDED.target_key,
    target_url = EXCLUDED.target_url,
    status = 'updated',
    sha256 = EXCLUDED.sha256,
    size_bytes = EXCLUDED.size_bytes,
    legacy_delete_after = EXCLUDED.legacy_delete_after,
    migrated_at = EXCLUDED.migrated_at,
    legacy_deleted_at = NULL,
    last_error = NULL,
    updated_at = now()
`

func rewriteReferences(
	ctx context.Context,
	pool *pgxpool.Pool,
	refs []objectReference,
	objects map[string]localObject,
	targetURLs map[string]string,
	retainFor string,
) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, ref := range refs {
		object := objects[ref.sourceKey]
		targetURL := targetURLs[ref.sourceKey]
		result, err := updateLegacyReference(ctx, tx, ref, targetURL)
		if err != nil {
			return fmt.Errorf("rewrite %s %s: %w", ref.ownerTable, ref.sourceURL, err)
		}
		if result.RowsAffected() == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, insertMigrationSQL,
			ref.objectType,
			ref.objectID,
			ref.workspaceID,
			ref.ownerTable,
			ref.ownerID,
			ref.sourceURL,
			ref.sourceKey,
			ref.sourceKey,
			targetURL,
			object.sha256,
			object.sizeBytes,
			retainFor,
		); err != nil {
			return fmt.Errorf("record %s migration for %s: %w", ref.ownerTable, ref.sourceURL, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit file migration: %w", err)
	}
	return nil
}

func updateLegacyReference(ctx context.Context, tx pgx.Tx, ref objectReference, targetURL string) (pgconn.CommandTag, error) {
	var (
		command pgconn.CommandTag
		err     error
	)
	switch ref.ownerTable {
	case "attachment":
		command, err = tx.Exec(ctx, `UPDATE attachment SET url = $1 WHERE id = $2::uuid AND url = $3`, targetURL, ref.ownerID, ref.sourceURL)
	case "user":
		command, err = tx.Exec(ctx, `UPDATE "user" SET avatar_url = $1 WHERE id = $2::uuid AND avatar_url = $3`, targetURL, ref.ownerID, ref.sourceURL)
	case "agent":
		command, err = tx.Exec(ctx, `UPDATE agent SET avatar_url = $1 WHERE id = $2::uuid AND avatar_url = $3`, targetURL, ref.ownerID, ref.sourceURL)
	case "squad":
		command, err = tx.Exec(ctx, `UPDATE squad SET avatar_url = $1 WHERE id = $2::uuid AND avatar_url = $3`, targetURL, ref.ownerID, ref.sourceURL)
	case "workspace":
		command, err = tx.Exec(ctx, `UPDATE workspace SET avatar_url = $1 WHERE id = $2::uuid AND avatar_url = $3`, targetURL, ref.ownerID, ref.sourceURL)
	case "channel_media_pending_object":
		command, err = tx.Exec(ctx, `UPDATE channel_media_pending_object SET storage_url = $1 WHERE workspace_id = $2::uuid AND storage_key = $3 AND storage_url = $4`, targetURL, ref.workspaceID, ref.sourceKey, ref.sourceURL)
	case "issue_source_context_object_intent":
		command, err = tx.Exec(ctx, `UPDATE issue_source_context_object_intent SET object_url = $1 WHERE workspace_id = $2::uuid AND storage_key = $3 AND object_url = $4`, targetURL, ref.workspaceID, ref.sourceKey, ref.sourceURL)
	default:
		return pgconn.CommandTag{}, fmt.Errorf("unsupported owner table %q", ref.ownerTable)
	}
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	return command, nil
}

func deleteEligibleLegacyObjects(ctx context.Context, pool *pgxpool.Pool, root string) (int, int, error) {
	rows, err := pool.Query(ctx, `
		SELECT source_key, sha256
		FROM file_storage_migration
		WHERE status = 'updated'
		  AND legacy_delete_after IS NOT NULL
		  AND legacy_delete_after <= now()
		GROUP BY source_key, sha256
		ORDER BY source_key`)
	if err != nil {
		return 0, 0, fmt.Errorf("load legacy deletion candidates: %w", err)
	}
	defer rows.Close()

	deleted, skipped := 0, 0
	seen := make(map[string]struct{})
	for rows.Next() {
		var key, expectedHash string
		if err := rows.Scan(&key, &expectedHash); err != nil {
			return deleted, skipped, fmt.Errorf("scan legacy deletion candidate: %w", err)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		safe, err := legacyObjectIsDeletable(ctx, pool, key)
		if err != nil {
			return deleted, skipped, err
		}
		if !safe {
			skipped++
			continue
		}
		filePath, err := safeLocalPath(root, key)
		if err != nil {
			return deleted, skipped, err
		}
		stat, statErr := os.Stat(filePath)
		if errors.Is(statErr, os.ErrNotExist) {
			if err := markLegacyDeleted(ctx, pool, key); err != nil {
				return deleted, skipped, err
			}
			deleted++
			continue
		}
		if statErr != nil {
			return deleted, skipped, fmt.Errorf("stat legacy deletion candidate %s: %w", key, statErr)
		}
		if stat.IsDir() {
			skipped++
			continue
		}
		actualHash, _, err := fileDigest(filePath)
		if err != nil {
			return deleted, skipped, fmt.Errorf("hash legacy deletion candidate %s: %w", key, err)
		}
		if expectedHash == "" || actualHash != expectedHash {
			skipped++
			continue
		}
		if err := os.Remove(filePath); err != nil {
			return deleted, skipped, fmt.Errorf("delete legacy object %s: %w", key, err)
		}
		_ = os.Remove(filePath + ".meta.json")
		_ = os.Remove(filepath.Join(filepath.Dir(filePath), "."+filepath.Base(filePath)+".tmp"))
		if err := markLegacyDeleted(ctx, pool, key); err != nil {
			return deleted, skipped, err
		}
		deleted++
	}
	if err := rows.Err(); err != nil {
		return deleted, skipped, fmt.Errorf("read legacy deletion candidates: %w", err)
	}
	return deleted, skipped, nil
}

func legacyObjectIsDeletable(ctx context.Context, pool *pgxpool.Pool, key string) (bool, error) {
	var referenced bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM attachment
		  WHERE url IN (
		    SELECT source_url FROM file_storage_migration WHERE source_key = $1
		  )
		) OR EXISTS (
		  SELECT 1 FROM "user"
		  WHERE avatar_url IN (
		    SELECT source_url FROM file_storage_migration WHERE source_key = $1
		  )
		) OR EXISTS (
		  SELECT 1 FROM agent
		  WHERE avatar_url IN (
		    SELECT source_url FROM file_storage_migration WHERE source_key = $1
		  )
		) OR EXISTS (
		  SELECT 1 FROM squad
		  WHERE avatar_url IN (
		    SELECT source_url FROM file_storage_migration WHERE source_key = $1
		  )
		) OR EXISTS (
		  SELECT 1 FROM workspace
		  WHERE avatar_url IN (
		    SELECT source_url FROM file_storage_migration WHERE source_key = $1
		  )
		) OR EXISTS (
		  SELECT 1 FROM channel_media_pending_object
		  WHERE storage_url IN (
		    SELECT source_url FROM file_storage_migration WHERE source_key = $1
		  )
		) OR EXISTS (
		  SELECT 1 FROM issue_source_context_object_intent
		  WHERE object_url IN (
		    SELECT source_url FROM file_storage_migration WHERE source_key = $1
		  )
		)`, key).Scan(&referenced); err != nil {
		return false, fmt.Errorf("check legacy references for %s: %w", key, err)
	}
	if referenced {
		return false, nil
	}

	var waiting bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1
		  FROM file_storage_migration
		  WHERE source_key = $1
		    AND status = 'updated'
		    AND (legacy_delete_after IS NULL OR legacy_delete_after > now())
		)`, key).Scan(&waiting); err != nil {
		return false, fmt.Errorf("check retention window for %s: %w", key, err)
	}
	return !waiting, nil
}

func safeLocalPath(root, key string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, filepath.FromSlash(key))
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe legacy key %q", key)
	}
	return target, nil
}

func markLegacyDeleted(ctx context.Context, pool *pgxpool.Pool, key string) error {
	_, err := pool.Exec(ctx, `
		UPDATE file_storage_migration
		SET status = 'legacy_deleted', legacy_deleted_at = now(), updated_at = now()
		WHERE source_key = $1
		  AND status = 'updated'
		  AND legacy_delete_after IS NOT NULL
		  AND legacy_delete_after <= now()`, key)
	if err != nil {
		return fmt.Errorf("mark legacy object %s deleted: %w", key, err)
	}
	return nil
}
