package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	// JobNameReportSnapshotRetention is the canonical audit-row name. Stable
	// across releases — do not rename without a migration.
	JobNameReportSnapshotRetention = "report_snapshot_retention"

	reportSnapshotRetentionCadence       = 1 * time.Hour
	reportSnapshotRetentionRunTimeout    = 5 * time.Minute
	reportSnapshotRetentionStaleTimeout  = 10 * time.Minute
	reportSnapshotRetentionScheduleDelay = 1 * time.Hour
)

// ReportSnapshotRetentionJob reclaims gzip-compressed report evidence whose
// retention window has elapsed (SIY-83). It only deletes report_snapshot
// rows with expires_at < now(); report_history — the Markdown summary — is
// never touched, so historical report lists and basic display keep working
// indefinitely. Recent and in-progress reports have expires_at in the future
// (or no snapshot yet) and are never affected, which honors the constraint
// that compression and cleanup must not impact in-progress or recent
// reports. When a report_history row is pruned by PruneProjectReportHistory,
// the report_snapshot row cascades away via the ON DELETE CASCADE FK, so
// this job only needs to handle the saved-report TTL path.
func ReportSnapshotRetentionJob(pool *pgxpool.Pool) JobSpec {
	return JobSpec{
		Name:              JobNameReportSnapshotRetention,
		Cadence:           reportSnapshotRetentionCadence,
		ScheduleDelay:     reportSnapshotRetentionScheduleDelay,
		CatchUpMode:       CatchUpLatestOnly,
		CatchUpWindow:     24 * time.Hour,
		RunTimeout:        reportSnapshotRetentionRunTimeout,
		StaleTimeout:      reportSnapshotRetentionStaleTimeout,
		HeartbeatInterval: 30 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       3,
		RetryBackoff: []time.Duration{
			1 * time.Minute,
			5 * time.Minute,
			15 * time.Minute,
		},
		Scopes:  StaticScopes(ScopeGlobal),
		Handler: makeReportSnapshotRetentionHandler(pool),
	}
}

func makeReportSnapshotRetentionHandler(pool *pgxpool.Pool) Handler {
	queries := db.New(pool)
	return func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
		reclaimed, err := queries.DeleteExpiredReportSnapshots(ctx)
		if err != nil {
			return HandlerResult{}, fmt.Errorf("delete expired report snapshots: %w", err)
		}
		// Light heartbeat at the end keeps stale_after fresh for jobs that
		// ran much shorter than HeartbeatInterval.
		if in.Heartbeat != nil {
			_ = in.Heartbeat(ctx)
		}
		return HandlerResult{
			RowsAffected: reclaimed,
			Result: map[string]any{
				"reclaimed_snapshots": reclaimed,
			},
		}, nil
	}
}
