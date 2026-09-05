package service

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestCompressDecompressReportSnapshotRoundTrips(t *testing.T) {
	// A payload that mirrors a real compacted snapshot: JSON with issues and
	// repeated discussion content that gzip shrinks substantially.
	payload := []byte(`{"period_type":"weekly","issues":[{"identifier":"RPT-1","title":"`)
	payload = append(payload, bytes.Repeat([]byte("讨论内容。"), 2000)...)
	payload = append(payload, []byte(`"}]}`)...)

	compressed, err := compressReportSnapshot(payload)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	if len(compressed) >= len(payload) {
		t.Fatalf("gzip did not reduce payload: raw=%d compressed=%d", len(payload), len(compressed))
	}
	round, err := decompressReportSnapshot(compressed)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if !bytes.Equal(payload, round) {
		t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(round), len(payload))
	}
}

func TestPrepareReportSnapshotStorageProducesSmallManifest(t *testing.T) {
	payload := []byte(`{"period_type":"weekly","issues":[]}`)
	manifest, compressed, err := prepareReportSnapshotStorage(payload, false)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var m reportSnapshotManifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatalf("manifest not valid json: %v", err)
	}
	if m.V != 2 || m.Storage != "report_snapshot" || m.Compression != "gzip" {
		t.Fatalf("unexpected manifest: %+v", m)
	}
	if m.RawBytes != int64(len(payload)) {
		t.Fatalf("raw_bytes: got %d want %d", m.RawBytes, len(payload))
	}
	if m.CompressedBytes != int64(len(compressed)) {
		t.Fatalf("compressed_bytes: got %d want %d", m.CompressedBytes, len(compressed))
	}
	if m.Truncated {
		t.Fatalf("truncated should be false")
	}
	// The manifest pointer must be tiny relative to the real evidence so the
	// report_history row stops carrying the heavy JSONB.
	if len(manifest) > 512 {
		t.Fatalf("manifest too large: %d bytes", len(manifest))
	}
}

func TestDecodeReportSnapshotManifestDistinguishesManifestFromLegacy(t *testing.T) {
	manifest, _, err := prepareReportSnapshotStorage([]byte(`{"issues":[]}`), true)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, ok := decodeReportSnapshotManifest(manifest); !ok {
		t.Fatalf("v2 manifest not detected")
	}
	if _, ok := decodeReportSnapshotManifest([]byte(`{"issues":[1,2,3]}`)); ok {
		t.Fatalf("legacy payload misdetected as manifest")
	}
	if _, ok := decodeReportSnapshotManifest([]byte(`{}`)); ok {
		t.Fatalf("placeholder {} misdetected as manifest")
	}
	if _, ok := decodeReportSnapshotManifest(nil); ok {
		t.Fatalf("nil misdetected as manifest")
	}
}

// The legacy and placeholder branches of ResolveReportSnapshot never reach the
// database, so a nil *db.Queries is safe here and lets the pure-Go fallbacks
// be covered without a live Postgres. The compressed-row and TTL-expired
// branches are exercised by the DB-gated tests in report_test.go.
func TestResolveReportSnapshotReturnsLegacyInline(t *testing.T) {
	legacy := []byte(`{"period_type":"weekly","issues":[{"identifier":"RPT-1"}]}`)
	got, err := ResolveReportSnapshot(context.Background(), nil, pgtype.UUID{}, legacy)
	if err != nil {
		t.Fatalf("resolve legacy: %v", err)
	}
	if !bytes.Equal(got, legacy) {
		t.Fatalf("legacy inline not returned verbatim")
	}
}

func TestResolveReportSnapshotPlaceholderBecomesEmptyObject(t *testing.T) {
	got, err := ResolveReportSnapshot(context.Background(), nil, pgtype.UUID{}, []byte(`{}`))
	if err != nil {
		t.Fatalf("resolve placeholder: %v", err)
	}
	if string(got) != `{}` {
		t.Fatalf("placeholder should normalize to {}: got %s", got)
	}
}
