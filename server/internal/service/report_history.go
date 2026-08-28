package service

import (
	"context"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type ReportHistoryService struct {
	Queries *db.Queries
}

func NewReportHistoryService(queries *db.Queries) *ReportHistoryService {
	return &ReportHistoryService{Queries: queries}
}

func (s *ReportHistoryService) Create(ctx context.Context, arg db.CreateReportHistoryParams) (db.ReportHistory, error) {
	return s.Queries.CreateReportHistory(ctx, arg)
}

func (s *ReportHistoryService) GetInWorkspace(ctx context.Context, arg db.GetReportHistoryInWorkspaceParams) (db.ReportHistory, error) {
	return s.Queries.GetReportHistoryInWorkspace(ctx, arg)
}

func (s *ReportHistoryService) ListByProject(ctx context.Context, arg db.ListReportHistoryByProjectParams) ([]db.ReportHistory, error) {
	return s.Queries.ListReportHistoryByProject(ctx, arg)
}
