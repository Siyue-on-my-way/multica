package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	JobNameProjectReportGenerate = "project_report_generate"
	ScopeKindProjectReport       = "project_report"
	maxPendingReportScopes       = 20
	ReportJobMaxAttempts         = 3
	// The narrative pipeline can process six batches of five Stage-1 calls
	// (30 issues × 45s) and then one Stage-2 call (45s). Keep enough room for
	// aggregation and the final report-history write; the old two-minute
	// deadline expired while saving otherwise.
	projectReportRunTimeout   = 8 * time.Minute
	projectReportStaleTimeout = 10 * time.Minute
)

type ProjectReportGenerator interface {
	GenerateInto(
		ctx context.Context,
		project db.Project,
		reportID pgtype.UUID,
		templateID pgtype.UUID,
		periodType string,
		rangeStart time.Time,
		rangeEnd time.Time,
		timezoneName string,
	) error
}

func ProjectReportGenerationJob(pool *pgxpool.Pool, queries *db.Queries, llm service.ReportLLM) JobSpec {
	generator := &service.ReportGenerator{Queries: queries, LLM: llm}
	return JobSpec{
		Name:              JobNameProjectReportGenerate,
		RunTimeout:        projectReportRunTimeout,
		StaleTimeout:      projectReportStaleTimeout,
		HeartbeatInterval: 30 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       ReportJobMaxAttempts,
		RetryBackoff: []time.Duration{
			30 * time.Second,
			2 * time.Minute,
			5 * time.Minute,
		},
		MaxPlansPerTick: 1,
		Scopes:          projectReportScopes(pool),
		PlansForScope:   projectReportPlans(pool),
		Handler:         projectReportHandler(queries, generator),
	}
}

func projectReportScopes(pool *pgxpool.Pool) ScopeProvider {
	return func(ctx context.Context, _ time.Time) ([]Scope, error) {
		rows, err := pool.Query(ctx, `
			SELECT r.id::text
			  FROM report_history r
			  LEFT JOIN LATERAL (
			       SELECT e.status, e.attempt, e.max_attempts
			         FROM sys_cron_executions e
			        WHERE e.job_name = $1
			          AND e.scope_kind = $2
			          AND e.scope_id = r.id::text
			        ORDER BY e.plan_time DESC
			        LIMIT 1
			  ) execution ON true
			 WHERE r.content = ''
			   AND (
			        execution.status IS NULL
			        OR execution.status <> 'FAILED'
			        OR execution.attempt < execution.max_attempts
			   )
			 ORDER BY r.created_at ASC
			 LIMIT $3
		`, JobNameProjectReportGenerate, ScopeKindProjectReport, maxPendingReportScopes)
		if err != nil {
			return nil, fmt.Errorf("scheduler: list pending report scopes: %w", err)
		}
		defer rows.Close()

		scopes := make([]Scope, 0, maxPendingReportScopes)
		for rows.Next() {
			var reportID string
			if err := rows.Scan(&reportID); err != nil {
				return nil, fmt.Errorf("scheduler: scan pending report scope: %w", err)
			}
			scopes = append(scopes, Scope{Kind: ScopeKindProjectReport, ID: reportID})
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("scheduler: iterate pending report scopes: %w", err)
		}
		return scopes, nil
	}
}

func projectReportPlans(pool *pgxpool.Pool) func(
	ctx context.Context,
	scope Scope,
	now time.Time,
	latest LatestPlanInfo,
) ([]time.Time, error) {
	return func(ctx context.Context, scope Scope, _ time.Time, _ LatestPlanInfo) ([]time.Time, error) {
		var planTime time.Time
		err := pool.QueryRow(ctx, `
			SELECT created_at
			  FROM report_history
			 WHERE id = $1::uuid AND content = ''
		`, scope.ID).Scan(&planTime)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("scheduler: read report job plan: %w", err)
		}
		return []time.Time{planTime.UTC()}, nil
	}
}

func projectReportHandler(queries *db.Queries, generator ProjectReportGenerator) Handler {
	return func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
		reportID, err := util.ParseUUID(in.Scope.ID)
		if err != nil {
			return HandlerResult{}, fmt.Errorf("invalid report job scope %q: %w", in.Scope.ID, err)
		}

		report, err := queries.GetReport(ctx, reportID)
		if errors.Is(err, pgx.ErrNoRows) {
			return HandlerResult{}, nil
		}
		if err != nil {
			return HandlerResult{}, fmt.Errorf("load report job: %w", err)
		}
		if report.Content != "" {
			return HandlerResult{RowsAffected: 1, Result: reportJobResult(reportID)}, nil
		}

		// Upstream retired the bare GetProject in favour of the workspace-scoped
		// variant; report rows carry both ids, so scope the read the same way.
		project, err := queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
			ID:          report.ProjectID,
			WorkspaceID: report.WorkspaceID,
		})
		if err != nil {
			return HandlerResult{}, fmt.Errorf("load report project: %w", err)
		}
		if err := generator.GenerateInto(
			ctx,
			project,
			reportID,
			report.TemplateID,
			report.PeriodType,
			report.RangeStart.Time,
			report.RangeEnd.Time,
			report.Timezone,
		); err != nil {
			return HandlerResult{}, err
		}
		return HandlerResult{RowsAffected: 1, Result: reportJobResult(reportID)}, nil
	}
}

func reportJobResult(reportID pgtype.UUID) map[string]any {
	return map[string]any{"report_id": util.UUIDToString(reportID)}
}
