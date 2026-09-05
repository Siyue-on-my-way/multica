package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type countReportLLM struct {
	calls int
}

func (l *countReportLLM) Enabled() bool { return true }

func (l *countReportLLM) GenerateJSON(context.Context, string, string, string, float64, int64) (string, error) {
	l.calls++
	return `{"content":"## 报告\n\n后台生成成功"}`, nil
}

func TestProjectReportGenerationJobCompletesOneTimeJob(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	userID := uuid.New()
	workspaceID := uuid.New()
	projectID := uuid.New()
	jobScope := Scope{Kind: ScopeKindProjectReport, ID: uuid.NewString()}

	t.Cleanup(func() {
		cleanup := context.Background()
		if _, err := pool.Exec(cleanup, `
			DELETE FROM sys_cron_executions
			 WHERE job_name = $1 AND scope_kind = $2 AND scope_id = $3
		`, JobNameProjectReportGenerate, ScopeKindProjectReport, jobScope.ID); err != nil {
			t.Errorf("delete report execution: %v", err)
		}
		pool.Exec(cleanup, `DELETE FROM report_history WHERE project_id = $1`, projectID)
		pool.Exec(cleanup, `DELETE FROM project WHERE id = $1`, projectID)
		pool.Exec(cleanup, `DELETE FROM workspace WHERE id = $1`, workspaceID)
		pool.Exec(cleanup, `DELETE FROM "user" WHERE id = $1`, userID)
	})

	if _, err := pool.Exec(ctx, `INSERT INTO "user" (id, name, email) VALUES ($1, $2, $3)`,
		userID, "Project Report Scheduler User", "project-report-scheduler-"+userID.String()+"@test.local"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workspace (id, name, slug, issue_prefix) VALUES ($1, $2, $3, 'RPT')`,
		workspaceID, "Project Report Scheduler Workspace", "project-report-scheduler-"+workspaceID.String()); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO project (id, workspace_id, title) VALUES ($1, $2, $3)`,
		projectID, workspaceID, "Project Report Scheduler Project"); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	rangeStart := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	rangeEnd := rangeStart.Add(24 * time.Hour)
	var reportID pgtype.UUID
	var createdAt time.Time
	if err := pool.QueryRow(ctx, `
		INSERT INTO report_history (
			workspace_id, project_id, period_type, range_start, range_end, timezone,
			generated_by_type, generated_by_id, data_snapshot, content
		) VALUES ($1, $2, 'daily', $3, $4, 'UTC', 'member', $5, '{}', '')
		RETURNING id, created_at
	`, workspaceID, projectID, rangeStart, rangeEnd, userID).Scan(&reportID, &createdAt); err != nil {
		t.Fatalf("seed report job: %v", err)
	}
	jobScope.ID = uuid.UUID(reportID.Bytes).String()

	queries := db.New(pool)
	llm := &countReportLLM{}
	job := ProjectReportGenerationJob(pool, queries, llm)
	manager := NewManager(pool, Options{RunnerID: "project-report-scheduler-test"})
	manager.processPlan(ctx, &job, jobScope, createdAt.UTC(), time.Now().UTC())

	var content string
	if err := pool.QueryRow(ctx, `SELECT content FROM report_history WHERE id = $1`, reportID).Scan(&content); err != nil {
		t.Fatalf("load generated report: %v", err)
	}
	if !strings.Contains(content, "## 今日摘要") || !strings.Contains(content, "## 数据指标") {
		t.Fatalf("unexpected generated report: %q", content)
	}

	var status string
	var attempt, maxAttempts int
	var result string
	if err := pool.QueryRow(ctx, `
		SELECT status, attempt, max_attempts, result::text
		  FROM sys_cron_executions
		 WHERE job_name = $1 AND scope_kind = $2 AND scope_id = $3
	`, JobNameProjectReportGenerate, jobScope.Kind, jobScope.ID).Scan(&status, &attempt, &maxAttempts, &result); err != nil {
		t.Fatalf("load report execution: %v", err)
	}
	if status != "SUCCESS" || attempt != 1 || maxAttempts != ReportJobMaxAttempts {
		t.Fatalf("unexpected execution state: status=%s attempt=%d max=%d", status, attempt, maxAttempts)
	}
	if !strings.Contains(result, jobScope.ID) {
		t.Fatalf("execution result does not identify report %q: %s", jobScope.ID, result)
	}

	manager.processPlan(ctx, &job, jobScope, createdAt.UTC(), time.Now().UTC())
	if llm.calls != 0 {
		t.Fatalf("empty report window invoked LLM %d times, want 0", llm.calls)
	}
}
