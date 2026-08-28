package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func newReportHistoryPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database unreachable: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestReportHistoryCRUD(t *testing.T) {
	pool := newReportHistoryPool(t)
	ctx := context.Background()
	workspaceID := uuid.New()
	otherWorkspaceID := uuid.New()
	projectID := uuid.New()
	otherProjectID := uuid.New()
	actorID := uuid.New()

	_, err := pool.Exec(ctx, `INSERT INTO workspace (id, name, slug) VALUES ($1, $2, $3)`,
		workspaceID, "report-history-ws", "report-history-ws-"+workspaceID.String())
	if err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO workspace (id, name, slug) VALUES ($1, $2, $3)`,
		otherWorkspaceID, "report-history-other-ws", "report-history-other-ws-"+otherWorkspaceID.String())
	if err != nil {
		t.Fatalf("seed other workspace: %v", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO project (id, workspace_id, title) VALUES ($1, $2, $3)`,
		projectID, workspaceID, "Report History Project")
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO project (id, workspace_id, title) VALUES ($1, $2, $3)`,
		otherProjectID, otherWorkspaceID, "Other Report History Project")
	if err != nil {
		t.Fatalf("seed other project: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM report_history WHERE project_id = $1`, projectID)
		pool.Exec(context.Background(), `DELETE FROM report_history WHERE project_id = $1`, otherProjectID)
		pool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID)
		pool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, otherProjectID)
		pool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
		pool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, otherWorkspaceID)
	})

	service := NewReportHistoryService(db.New(pool))
	snapshot := map[string]any{
		"completed_issues": 3,
		"summary":          "中文摘要\n第二行",
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	content := "# 项目日报\n\n中文内容\n换行"

	oldDaily, err := service.Create(ctx, db.CreateReportHistoryParams{
		WorkspaceID:     pgtype.UUID{Bytes: workspaceID, Valid: true},
		ProjectID:       pgtype.UUID{Bytes: projectID, Valid: true},
		PeriodType:      "daily",
		RangeStart:      pgtype.Timestamptz{Time: time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC), Valid: true},
		RangeEnd:        pgtype.Timestamptz{Time: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC), Valid: true},
		Timezone:        "Asia/Shanghai",
		GeneratedByType: "member",
		GeneratedByID:   pgtype.UUID{Bytes: actorID, Valid: true},
		DataSnapshot:    snapshotJSON,
		Content:         content,
	})
	if err != nil {
		t.Fatalf("create old report: %v", err)
	}
	latestDaily, err := service.Create(ctx, db.CreateReportHistoryParams{
		WorkspaceID:     pgtype.UUID{Bytes: workspaceID, Valid: true},
		ProjectID:       pgtype.UUID{Bytes: projectID, Valid: true},
		PeriodType:      "daily",
		RangeStart:      pgtype.Timestamptz{Time: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC), Valid: true},
		RangeEnd:        pgtype.Timestamptz{Time: time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC), Valid: true},
		Timezone:        "Asia/Shanghai",
		GeneratedByType: "agent",
		GeneratedByID:   pgtype.UUID{Bytes: actorID, Valid: true},
		DataSnapshot:    snapshotJSON,
		Content:         content,
	})
	if err != nil {
		t.Fatalf("create latest report: %v", err)
	}
	if _, err := service.Create(ctx, db.CreateReportHistoryParams{
		WorkspaceID:     pgtype.UUID{Bytes: otherWorkspaceID, Valid: true},
		ProjectID:       pgtype.UUID{Bytes: otherProjectID, Valid: true},
		PeriodType:      "weekly",
		RangeStart:      pgtype.Timestamptz{Time: time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC), Valid: true},
		RangeEnd:        pgtype.Timestamptz{Time: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), Valid: true},
		Timezone:        "Asia/Shanghai",
		GeneratedByType: "member",
		GeneratedByID:   pgtype.UUID{Bytes: actorID, Valid: true},
		DataSnapshot:    snapshotJSON,
		Content:         content,
	}); err != nil {
		t.Fatalf("create other report: %v", err)
	}

	got, err := service.GetInWorkspace(ctx, db.GetReportHistoryInWorkspaceParams{
		ID:          latestDaily.ID,
		WorkspaceID: latestDaily.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	var storedSnapshot map[string]any
	if err := json.Unmarshal(got.DataSnapshot, &storedSnapshot); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if got.Content != content {
		t.Fatalf("content = %q, want %q", got.Content, content)
	}
	if fmt.Sprint(storedSnapshot["summary"]) != fmt.Sprint(snapshot["summary"]) {
		t.Fatalf("snapshot summary = %v, want %v", storedSnapshot["summary"], snapshot["summary"])
	}
	if storedSnapshot["completed_issues"] != float64(3) {
		t.Fatalf("snapshot completed_issues = %v, want 3", storedSnapshot["completed_issues"])
	}

	_, err = service.GetInWorkspace(ctx, db.GetReportHistoryInWorkspaceParams{
		ID:          latestDaily.ID,
		WorkspaceID: pgtype.UUID{Bytes: otherWorkspaceID, Valid: true},
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("wrong workspace get error = %v, want no rows", err)
	}

	pageOne, err := service.ListByProject(ctx, db.ListReportHistoryByProjectParams{
		WorkspaceID: latestDaily.WorkspaceID,
		ProjectID:   latestDaily.ProjectID,
		Limit:       1,
	})
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(pageOne) != 1 || pageOne[0].ID != latestDaily.ID {
		t.Fatalf("first page = %+v, want latest report only", pageOne)
	}
	pageTwo, err := service.ListByProject(ctx, db.ListReportHistoryByProjectParams{
		WorkspaceID: latestDaily.WorkspaceID,
		ProjectID:   latestDaily.ProjectID,
		Limit:       1,
		Offset:      1,
	})
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if len(pageTwo) != 1 || pageTwo[0].ID != oldDaily.ID {
		t.Fatalf("second page = %+v, want old report only", pageTwo)
	}
}
