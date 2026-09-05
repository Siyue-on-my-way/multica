package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	// reportSnapshotCompression is the algorithm stamped on
	// report_snapshot.compression. Only "gzip" is produced or understood here.
	reportSnapshotCompression = "gzip"
	// reportSnapshotRetention is how long the compressed raw-evidence payload
	// is kept before the TTL sweep reclaims it. The Markdown summary in
	// report_history.content is retained permanently regardless of this value,
	// so expiring a snapshot never breaks the report list or basic display.
	// Tunable: a quarter is a long enough window to revisit evidence for a
	// recent report while bounding the disk footprint of historical snapshots.
	reportSnapshotRetention = 90 * 24 * time.Hour
)

// reportSnapshotManifest is the tiny pointer stored in
// report_history.data_snapshot once the heavy evidence has moved to the
// report_snapshot table. It is valid JSONB (the column is NOT NULL) but only
// a few hundred bytes, so the summary row stays small and permanently
// readable. ResolveReportSnapshot inflates the real payload on demand.
type reportSnapshotManifest struct {
	V               int    `json:"v"`
	Storage         string `json:"storage"`
	Compression     string `json:"compression,omitempty"`
	RawBytes        int64  `json:"raw_bytes,omitempty"`
	CompressedBytes int64  `json:"compressed_bytes,omitempty"`
	Truncated       bool   `json:"truncated,omitempty"`
	// Expired is set by ResolveReportSnapshot when the compressed payload has
	// been reclaimed by the TTL (or not yet written). It is never persisted.
	Expired bool `json:"expired,omitempty"`
}

// compressReportSnapshot gzip-encodes the marshalled report snapshot for
// storage in the report_snapshot.payload BYTEA column.
func compressReportSnapshot(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil, fmt.Errorf("gzip report snapshot: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("close gzip report snapshot: %w", err)
	}
	return buf.Bytes(), nil
}

// decompressReportSnapshot reverses compressReportSnapshot for on-demand
// inflation of a stored evidence payload.
func decompressReportSnapshot(compressed []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("open gzip report snapshot: %w", err)
	}
	defer zr.Close()
	data, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("read gzip report snapshot: %w", err)
	}
	return data, nil
}

// prepareReportSnapshotStorage produces the gzip-compressed payload and the
// tiny manifest pointer that callers persist into report_snapshot and
// report_history.data_snapshot respectively. Splitting encode from persist
// lets the write path order the two writes so a report is only marked done
// after its evidence is durable.
func prepareReportSnapshotStorage(snapshotJSON []byte, truncated bool) (manifest, compressed []byte, err error) {
	compressed, err = compressReportSnapshot(snapshotJSON)
	if err != nil {
		return nil, nil, err
	}
	manifest, err = json.Marshal(reportSnapshotManifest{
		V:               2,
		Storage:         "report_snapshot",
		Compression:     reportSnapshotCompression,
		RawBytes:        int64(len(snapshotJSON)),
		CompressedBytes: int64(len(compressed)),
		Truncated:       truncated,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("encode report snapshot manifest: %w", err)
	}
	return manifest, compressed, nil
}

// persistReportSnapshot upserts the gzip-compressed evidence into the
// report_snapshot table and stamps its TTL. Best-effort relative to the
// report_history row: a failure here is logged by the caller rather than
// turning a finished report into a failed one, because the Markdown summary
// is still complete; a missing snapshot degrades only the evidence view,
// which ResolveReportSnapshot surfaces as an expired manifest.
func (g *ReportGenerator) persistReportSnapshot(
	ctx context.Context,
	reportID pgtype.UUID,
	project db.Project,
	compressed, raw []byte,
	truncated bool,
) error {
	return g.Queries.UpsertReportSnapshot(ctx, db.UpsertReportSnapshotParams{
		ReportID:         reportID,
		WorkspaceID:      project.WorkspaceID,
		ProjectID:        project.ID,
		Payload:          compressed,
		Compression:      reportSnapshotCompression,
		RawBytes:         int64(len(raw)),
		CompressedBytes:  int64(len(compressed)),
		StorageTruncated: truncated,
		ExpiresAt:        pgtype.Timestamptz{Time: time.Now().Add(reportSnapshotRetention), Valid: true},
	})
}

// ResolveReportSnapshot returns the raw-evidence bytes to serve as
// data_snapshot for a report detail response.
//
// New reports (post-SIY-83) store the heavy evidence gzip-compressed in the
// report_snapshot table and keep only a tiny manifest in
// report_history.data_snapshot; this function transparently inflates the
// compressed payload. Legacy reports (pre-SIY-83) still carry the full
// payload inline and are returned unchanged. If the compressed snapshot has
// been reclaimed by the TTL (or not yet written), the manifest is returned
// with Expired=true so callers can distinguish "no detail available" from a
// real payload while the summary in report_history.content stays readable.
func ResolveReportSnapshot(ctx context.Context, queries *db.Queries, reportID pgtype.UUID, inline []byte) ([]byte, error) {
	manifest, isManifest := decodeReportSnapshotManifest(inline)
	if !isManifest {
		// Legacy inline payload (or the pre-generation "{}" placeholder).
		if len(inline) == 0 {
			return []byte("{}"), nil
		}
		return inline, nil
	}

	row, err := queries.GetReportSnapshot(ctx, reportID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("load report snapshot: %w", err)
		}
		// Reclaimed by TTL or not yet persisted.
		manifest.Expired = true
		return json.Marshal(manifest)
	}
	payload, err := decompressReportSnapshot(row.Payload)
	if err != nil {
		return nil, fmt.Errorf("decompress report snapshot: %w", err)
	}
	return payload, nil
}

// decodeReportSnapshotManifest reports whether inline looks like a v2
// report_snapshot manifest written by prepareReportSnapshotStorage. Anything
// else (legacy full payload, the "{}" placeholder) is treated as inline.
func decodeReportSnapshotManifest(inline []byte) (reportSnapshotManifest, bool) {
	var m reportSnapshotManifest
	if len(inline) == 0 || inline[0] != '{' {
		return reportSnapshotManifest{}, false
	}
	if err := json.Unmarshal(inline, &m); err != nil {
		return reportSnapshotManifest{}, false
	}
	if m.V != 2 || m.Storage != "report_snapshot" {
		return reportSnapshotManifest{}, false
	}
	return m, true
}
