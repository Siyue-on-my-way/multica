package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/llm"
)

const (
	reportLLMTimeout            = 45 * time.Second
	reportLLMTemperature        = 0.2
	reportLLMMaxCompletionToken = 3072
	reportPromptMaxEvents       = 80
	reportPromptMaxChars        = 24000
	reportSummaryVersion        = 1
	reportAnalysisVersion       = 2
	// Report history is user-visible evidence, so it is bounded independently
	// from the LLM prompt. PostgreSQL already compresses JSONB, but storing the
	// same raw task payloads and bookkeeping rows still makes every report
	// unnecessarily large.
	reportSnapshotMaxBytes            = 512 * 1024
	reportSnapshotMaxIssues           = 200
	reportSnapshotMaxReferences       = 100
	reportSnapshotEvidenceBudget      = 320 * 1024
	reportSnapshotMaxEventsPerIssue   = 80
	reportSnapshotMaxCharsPerIssue    = 12000
	reportSnapshotMaxEventContent     = 1200
	reportSnapshotMaxIssueDescription = 1200
	reportSnapshotMaxWarnings         = 8
	reportContentMaxBytes             = 256 * 1024
	reportContentMaxEvidenceEvents    = 400
)

type ReportLLM interface {
	Enabled() bool
	GenerateJSON(ctx context.Context, model, systemPrompt, userPrompt string, temperature float64, maxCompletionTokens int64) (string, error)
}

// ReportTimelineEvent is the normalized, issue-local representation of one
// source record. Context comments are retained with InRange=false so a reply
// can be understood without counting an older discussion in the period.
type ReportTimelineEvent struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	OccurredAt  time.Time       `json:"occurred_at"`
	InRange     bool            `json:"in_range"`
	AuthorType  string          `json:"author_type,omitempty"`
	AuthorID    string          `json:"author_id,omitempty"`
	Content     string          `json:"content,omitempty"`
	CommentType string          `json:"comment_type,omitempty"`
	ParentID    string          `json:"parent_id,omitempty"`
	Action      string          `json:"action,omitempty"`
	Details     json.RawMessage `json:"details,omitempty"`
}

type ReportIssueSummary struct {
	IssueID       string   `json:"issue_id"`
	Problem       string   `json:"problem"`
	Actions       []string `json:"actions"`
	Outcome       string   `json:"outcome"`
	OpenItems     []string `json:"open_items"`
	WorkTypes     []string `json:"work_types,omitempty"`
	WorkDone      []string `json:"work_done,omitempty"`
	Decision      string   `json:"decision,omitempty"`
	Deliverables  []string `json:"deliverables,omitempty"`
	Verification  []string `json:"verification,omitempty"`
	CurrentState  string   `json:"current_state,omitempty"`
	Dependencies  []string `json:"dependencies,omitempty"`
	Risks         []string `json:"risks,omitempty"`
	Artifacts     []string `json:"artifacts,omitempty"`
	Impact        string   `json:"impact,omitempty"`
	EvidenceIDs   []string `json:"evidence_ids,omitempty"`
	Confidence    string   `json:"confidence,omitempty"`
	SummarySource string   `json:"summary_source,omitempty"`
}

type ReportWorkItem struct {
	ID             string   `json:"id"`
	IssueID        string   `json:"issue_id"`
	Identifier     string   `json:"identifier"`
	IssueTitle     string   `json:"issue_title"`
	BusinessDomain string   `json:"business_domain,omitempty"`
	Milestone      string   `json:"milestone,omitempty"`
	Milestones     []string `json:"milestones,omitempty"`
	Category       string   `json:"category"`
	Categories     []string `json:"categories,omitempty"`
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	WorkDone       []string `json:"work_done,omitempty"`
	Decision       string   `json:"decision,omitempty"`
	Deliverables   []string `json:"deliverables,omitempty"`
	Verification   []string `json:"verification,omitempty"`
	CurrentState   string   `json:"current_state,omitempty"`
	Dependencies   []string `json:"dependencies,omitempty"`
	Risks          []string `json:"risks,omitempty"`
	Outcome        string   `json:"outcome"`
	Impact         string   `json:"impact,omitempty"`
	BusinessImpact string   `json:"business_impact,omitempty"`
	Status         string   `json:"status"`
	EvidenceIDs    []string `json:"evidence_ids,omitempty"`
	Confidence     string   `json:"confidence,omitempty"`
	Source         string   `json:"source,omitempty"`
}

type ReportMilestone struct {
	ID             string   `json:"id"`
	BusinessDomain string   `json:"business_domain"`
	Title          string   `json:"title"`
	Summary        string   `json:"summary"`
	WorkItemIDs    []string `json:"work_item_ids,omitempty"`
	Status         string   `json:"status"`
	EvidenceIDs    []string `json:"evidence_ids,omitempty"`
	Confidence     string   `json:"confidence,omitempty"`
	Source         string   `json:"source,omitempty"`
}

type ReportBusinessDomain struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Summary        string            `json:"summary"`
	WorkItemIDs    []string          `json:"work_item_ids,omitempty"`
	Milestones     []ReportMilestone `json:"milestones,omitempty"`
	BusinessImpact string            `json:"business_impact,omitempty"`
	EvidenceIDs    []string          `json:"evidence_ids,omitempty"`
	Confidence     string            `json:"confidence,omitempty"`
	Source         string            `json:"source,omitempty"`
}

type ReportAnalysisNote struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	Confidence  string   `json:"confidence,omitempty"`
	Source      string   `json:"source,omitempty"`
}

type ReportProjectChange struct {
	ID          string   `json:"id"`
	Category    string   `json:"category"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Impact      string   `json:"impact,omitempty"`
	Status      string   `json:"status"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	Confidence  string   `json:"confidence,omitempty"`
	Source      string   `json:"source,omitempty"`
}

type ReportProjectAnalysis struct {
	Summary         string                 `json:"summary"`
	BusinessDomains []ReportBusinessDomain `json:"business_domains,omitempty"`
	Milestones      []ReportMilestone      `json:"milestones,omitempty"`
	Changes         []ReportProjectChange  `json:"changes,omitempty"`
	Risks           []ReportAnalysisNote   `json:"risks,omitempty"`
	NextSteps       []ReportAnalysisNote   `json:"next_steps,omitempty"`
	EvidenceIDs     []string               `json:"evidence_ids,omitempty"`
	Confidence      string                 `json:"confidence,omitempty"`
	Source          string                 `json:"source,omitempty"`
}

type ReportIssue struct {
	IssueID           string                `json:"issue_id,omitempty"`
	Identifier        string                `json:"identifier"`
	Title             string                `json:"title"`
	Description       string                `json:"description,omitempty"`
	BusinessDomain    string                `json:"business_domain,omitempty"`
	Status            string                `json:"status,omitempty"`
	DueDate           string                `json:"due_date,omitempty"`
	Summary           ReportIssueSummary    `json:"summary,omitempty"`
	Timeline          []ReportTimelineEvent `json:"timeline,omitempty"`
	TimelineTruncated bool                  `json:"timeline_truncated,omitempty"`
}

type ReportSnapshot struct {
	PeriodType         string                 `json:"period_type"`
	RangeStart         time.Time              `json:"range_start"`
	RangeEnd           time.Time              `json:"range_end"`
	Timezone           string                 `json:"timezone"`
	ProjectTitle       string                 `json:"project_title,omitempty"`
	ProjectDescription string                 `json:"project_description,omitempty"`
	GeneratedAt        time.Time              `json:"generated_at"`
	SummaryVersion     int                    `json:"summary_version"`
	AnalysisVersion    int                    `json:"analysis_version,omitempty"`
	Issues             []ReportIssue          `json:"issues"`
	ActiveIssueCount   int                    `json:"active_issue_count"`
	WorkItems          []ReportWorkItem       `json:"work_items,omitempty"`
	ProjectAnalysis    ReportProjectAnalysis  `json:"project_analysis,omitempty"`
	AnalysisWarnings   []string               `json:"analysis_warnings,omitempty"`
	NarrativeVersion   int                    `json:"narrative_version,omitempty"`
	Narratives         []ReportIssueNarrative `json:"narratives,omitempty"`
	ExecutiveSummary   string                 `json:"executive_summary,omitempty"`
	StorageTruncated   bool                   `json:"storage_truncated,omitempty"`

	// These fields are retained for existing consumers of report history. New
	// issue-centered clients should use Issues and its current status values.
	Completed       []ReportIssue `json:"completed"`
	InProgress      []ReportIssue `json:"in_progress"`
	Blocked         []ReportIssue `json:"blocked"`
	Overdue         []ReportIssue `json:"overdue"`
	Cancelled       []ReportIssue `json:"cancelled"`
	CompletedCount  int           `json:"completed_count"`
	InProgressCount int           `json:"in_progress_count"`
	BlockedCount    int           `json:"blocked_count"`
	OverdueCount    int           `json:"overdue_count"`
	CancelledCount  int           `json:"cancelled_count"`
}

type ReportGenerator struct {
	Queries *db.Queries
	LLM     ReportLLM
}

func (g *ReportGenerator) Generate(
	ctx context.Context,
	project db.Project,
	templateID pgtype.UUID,
	periodType string,
	rangeStart time.Time,
	rangeEnd time.Time,
	timezoneName string,
	generatedByType string,
	generatedByID pgtype.UUID,
) (db.ReportHistory, error) {
	snapshotJSON, content, storageTruncated, err := g.build(ctx, project, templateID, periodType, rangeStart, rangeEnd, timezoneName)
	if err != nil {
		return db.ReportHistory{}, err
	}
	manifest, compressed, err := prepareReportSnapshotStorage(snapshotJSON, storageTruncated)
	if err != nil {
		return db.ReportHistory{}, fmt.Errorf("encode report snapshot: %w", err)
	}
	report, err := g.Queries.CreateReportHistory(ctx, db.CreateReportHistoryParams{
		WorkspaceID:     project.WorkspaceID,
		ProjectID:       project.ID,
		TemplateID:      templateID,
		PeriodType:      periodType,
		RangeStart:      pgtype.Timestamptz{Time: rangeStart, Valid: true},
		RangeEnd:        pgtype.Timestamptz{Time: rangeEnd, Valid: true},
		Timezone:        timezoneName,
		GeneratedByType: generatedByType,
		GeneratedByID:   generatedByID,
		DataSnapshot:    manifest,
		Content:         content,
	})
	if err != nil {
		return db.ReportHistory{}, fmt.Errorf("save report history: %w", err)
	}
	// Persist the compressed evidence after the row exists (FK) but before the
	// report is returned. A failure here degrades only the evidence view —
	// ResolveReportSnapshot surfaces an expired manifest — never the summary,
	// so it is logged rather than failing an otherwise-complete report.
	if err := g.persistReportSnapshot(ctx, report.ID, project, compressed, snapshotJSON, storageTruncated); err != nil {
		slog.Warn("project report: snapshot persistence skipped", "project_id", project.ID, "error", err)
	}
	g.pruneReportHistory(ctx, project)
	return report, nil
}

func (g *ReportGenerator) GenerateInto(
	ctx context.Context,
	project db.Project,
	reportID pgtype.UUID,
	templateID pgtype.UUID,
	periodType string,
	rangeStart time.Time,
	rangeEnd time.Time,
	timezoneName string,
) error {
	snapshotJSON, content, storageTruncated, err := g.build(ctx, project, templateID, periodType, rangeStart, rangeEnd, timezoneName)
	if err != nil {
		return err
	}
	manifest, compressed, err := prepareReportSnapshotStorage(snapshotJSON, storageTruncated)
	if err != nil {
		return fmt.Errorf("encode report snapshot: %w", err)
	}
	// Persist the compressed evidence before flipping content to non-empty so
	// that once a report is "done" its snapshot is guaranteed durable. A
	// failure leaves the row pending (content='') and the scheduler retries.
	if err := g.persistReportSnapshot(ctx, reportID, project, compressed, snapshotJSON, storageTruncated); err != nil {
		return fmt.Errorf("persist report snapshot: %w", err)
	}
	if err := g.Queries.UpdateReportHistoryGeneration(ctx, db.UpdateReportHistoryGenerationParams{
		ID:           reportID,
		DataSnapshot: manifest,
		Content:      content,
	}); err != nil {
		return fmt.Errorf("save generated report: %w", err)
	}
	g.pruneReportHistory(ctx, project)
	return nil
}

func (g *ReportGenerator) build(
	ctx context.Context,
	project db.Project,
	templateID pgtype.UUID,
	periodType string,
	rangeStart time.Time,
	rangeEnd time.Time,
	timezoneName string,
) ([]byte, string, bool, error) {
	location, err := time.LoadLocation(timezoneName)
	if err != nil {
		return nil, "", false, fmt.Errorf("invalid timezone %q: %w", timezoneName, err)
	}

	now := time.Now().In(location)
	aggregator := &ProjectIssueAggregator{Queries: g.Queries}
	snapshot, err := aggregator.Aggregate(ctx, project, rangeStart, rangeEnd, now)
	if err != nil {
		return nil, "", false, err
	}
	snapshot.PeriodType = periodType
	snapshot.RangeStart = rangeStart.In(location)
	snapshot.RangeEnd = rangeEnd.In(location)
	snapshot.Timezone = timezoneName
	snapshot.ProjectTitle = project.Title
	if project.Description.Valid {
		snapshot.ProjectDescription = project.Description.String
	}

	var content string
	var templatePrompt string
	if templateID.Valid {
		template, err := g.Queries.GetReportTemplate(ctx, db.GetReportTemplateParams{
			ID:          templateID,
			WorkspaceID: project.WorkspaceID,
		})
		if err == nil {
			templatePrompt = template.SystemPrompt
		}
	}

	// Narrative pipeline: Stage 0+1 per-issue conversation summaries, then
	// Stage 2 project-level executive narrative. A custom template prompt takes
	// over the Stage-2 system prompt. The same deterministic path is used when
	// there are no discussion/task records, so a status-only window never sends
	// the legacy status buckets to the LLM.
	snapshot = g.withNarratives(ctx, snapshot)
	snapshot = g.withExecutiveSummary(ctx, snapshot, templatePrompt)
	content = buildNarrativeReportContent(snapshot)

	snapshotJSON, storageTruncated, err := marshalReportSnapshot(snapshot)
	if err != nil {
		return nil, "", false, fmt.Errorf("encode report snapshot: %w", err)
	}
	slog.Info("project report: storage footprint",
		"project_id", project.ID,
		"snapshot_input_events", reportSnapshotEventCount(snapshot.Issues),
		"snapshot_bytes", len(snapshotJSON),
		"content_bytes", len(content),
		"total_bytes", len(snapshotJSON)+len(content),
		"storage_truncated", storageTruncated,
	)
	return snapshotJSON, content, storageTruncated, nil
}

func (g *ReportGenerator) pruneReportHistory(ctx context.Context, project db.Project) {
	if err := g.Queries.PruneProjectReportHistory(ctx, db.PruneProjectReportHistoryParams{
		WorkspaceID: project.WorkspaceID,
		ProjectID:   project.ID,
	}); err != nil {
		// Cleanup is deliberately best effort. A report that was already saved
		// must not be turned into a failed report merely because retention
		// maintenance could not run.
		slog.Warn("project report: history cleanup skipped", "project_id", project.ID, "error", err)
	}
}

// marshalReportSnapshot is the storage boundary for report evidence. Prompts
// may use a larger in-memory view, but the persisted snapshot only needs the
// conversation that supports the narrative. Status/activity bookkeeping is
// represented by status fields, counters, and the narrative status span; raw
// JSON payloads are not useful evidence and can be very large.
func marshalReportSnapshot(snapshot ReportSnapshot) ([]byte, bool, error) {
	compacted := compactReportSnapshot(snapshot)
	encoded, err := json.Marshal(compacted)
	if err != nil {
		return nil, compacted.StorageTruncated, err
	}
	if len(encoded) <= reportSnapshotMaxBytes {
		return encoded, compacted.StorageTruncated, nil
	}

	// Keep the L1/L2 report useful even for an unusually large project. The
	// evidence appendix is the only part that can be dropped without changing
	// counts or the executive narrative.
	compacted.StorageTruncated = true
	for index := range compacted.Issues {
		if len(compacted.Issues[index].Timeline) > 0 {
			compacted.Issues[index].Timeline = nil
			compacted.Issues[index].TimelineTruncated = true
		}
	}
	encoded, err = json.Marshal(compacted)
	if err != nil {
		return nil, compacted.StorageTruncated, err
	}
	if len(encoded) <= reportSnapshotMaxBytes {
		return encoded, compacted.StorageTruncated, nil
	}

	// The analysis/work-item structures repeat issue facts. Retain the
	// executive summary and issue headers as a final bounded fallback; the full
	// Markdown content remains available in report_history.content.
	compacted.WorkItems = nil
	compacted.ProjectAnalysis = ReportProjectAnalysis{
		Summary:    compactReportText(compacted.ProjectAnalysis.Summary, 2000),
		Confidence: compacted.ProjectAnalysis.Confidence,
		Source:     compacted.ProjectAnalysis.Source,
	}
	compacted.Narratives = compactReportNarratives(compacted.Narratives, 100)
	for index := range compacted.Issues {
		compacted.Issues[index] = compactReportIssueHeader(compacted.Issues[index])
	}
	encoded, err = json.Marshal(compacted)
	if err != nil {
		return nil, compacted.StorageTruncated, err
	}
	if len(encoded) <= reportSnapshotMaxBytes {
		return encoded, compacted.StorageTruncated, nil
	}

	// This path is intentionally lossier but deterministic. It protects the
	// report write from a single pathological description or a project with an
	// unusually high issue count.
	compacted.ProjectDescription = ""
	compacted.Narratives = nil
	if len(compacted.Issues) > 50 {
		compacted.Issues = compacted.Issues[:50]
	}
	encoded, err = json.Marshal(compacted)
	if err != nil {
		return nil, compacted.StorageTruncated, err
	}
	if len(encoded) <= reportSnapshotMaxBytes {
		return encoded, compacted.StorageTruncated, nil
	}

	// A final metadata-only representation makes the size limit a hard
	// contract even if a future field introduces an unexpectedly large slice or
	// string. The complete Markdown report is stored separately in content.
	minimal := minimalReportSnapshot(compacted)
	encoded, err = json.Marshal(minimal)
	if err != nil {
		return nil, true, err
	}
	if len(encoded) > reportSnapshotMaxBytes {
		return nil, true, fmt.Errorf("minimal report snapshot is %d bytes, limit is %d", len(encoded), reportSnapshotMaxBytes)
	}
	return encoded, true, nil
}

func compactReportSnapshot(snapshot ReportSnapshot) ReportSnapshot {
	compacted := snapshot
	compacted.PeriodType = compactReportText(compacted.PeriodType, 64)
	compacted.Timezone = compactReportText(compacted.Timezone, 128)
	compacted.ProjectTitle = compactReportText(compacted.ProjectTitle, 500)
	compacted.ProjectDescription = compactReportText(compacted.ProjectDescription, 2000)
	compacted.AnalysisWarnings = compactReportStringList(compacted.AnalysisWarnings, reportSnapshotMaxWarnings, 300)

	if len(compacted.Issues) > reportSnapshotMaxIssues {
		compacted.Issues = compacted.Issues[:reportSnapshotMaxIssues]
		compacted.StorageTruncated = true
	}
	issues := make([]ReportIssue, 0, len(compacted.Issues))
	remainingEvidence := reportSnapshotEvidenceBudget
	for _, issue := range compacted.Issues {
		compact := compactReportIssue(issue)
		if remainingEvidence <= 0 {
			if len(compact.Timeline) > 0 {
				compact.Timeline = nil
				compact.TimelineTruncated = true
				compacted.StorageTruncated = true
			}
		} else if eventBytes := reportTimelineBytes(compact.Timeline); eventBytes > remainingEvidence {
			compact.Timeline = fitTimelineToCharacterBudget(compact.Timeline, remainingEvidence)
			compact.TimelineTruncated = true
			compacted.StorageTruncated = true
			remainingEvidence = 0
		} else {
			remainingEvidence -= eventBytes
		}
		if compact.TimelineTruncated {
			compacted.StorageTruncated = true
		}
		issues = append(issues, compact)
	}
	compacted.Issues = issues
	compacted.Completed = compactReportIssueReferencesForStorage(compacted.Completed, &compacted.StorageTruncated)
	compacted.InProgress = compactReportIssueReferencesForStorage(compacted.InProgress, &compacted.StorageTruncated)
	compacted.Blocked = compactReportIssueReferencesForStorage(compacted.Blocked, &compacted.StorageTruncated)
	compacted.Overdue = compactReportIssueReferencesForStorage(compacted.Overdue, &compacted.StorageTruncated)
	compacted.Cancelled = compactReportIssueReferencesForStorage(compacted.Cancelled, &compacted.StorageTruncated)
	compacted.WorkItems = compactReportWorkItems(compacted.WorkItems)
	compacted.ProjectAnalysis = compactReportProjectAnalysis(compacted.ProjectAnalysis)
	compacted.Narratives = compactReportNarratives(compacted.Narratives, reportSnapshotMaxIssues)
	compacted.ExecutiveSummary = compactReportText(compacted.ExecutiveSummary, 4000)
	return compacted
}

func compactReportIssue(issue ReportIssue) ReportIssue {
	issue.IssueID = compactReportText(issue.IssueID, 100)
	issue.Identifier = compactReportText(issue.Identifier, 100)
	issue.Title = compactReportText(issue.Title, 500)
	issue.Description = compactReportText(issue.Description, reportSnapshotMaxIssueDescription)
	issue.BusinessDomain = compactReportText(issue.BusinessDomain, 200)
	issue.Status = compactReportText(issue.Status, 64)
	issue.DueDate = compactReportText(issue.DueDate, 32)
	issue.Summary = compactReportIssueSummary(issue.Summary)
	issue.Timeline = compactReportTimeline(issue.Timeline, &issue.TimelineTruncated)
	return issue
}

func compactReportIssueHeader(issue ReportIssue) ReportIssue {
	return ReportIssue{
		IssueID:        compactReportText(issue.IssueID, 100),
		Identifier:     compactReportText(issue.Identifier, 100),
		Title:          compactReportText(issue.Title, 500),
		BusinessDomain: compactReportText(issue.BusinessDomain, 200),
		Status:         compactReportText(issue.Status, 64),
		DueDate:        compactReportText(issue.DueDate, 32),
	}
}

func compactReportIssueReferences(issues []ReportIssue) []ReportIssue {
	if len(issues) == 0 {
		return []ReportIssue{}
	}
	result := make([]ReportIssue, 0, len(issues))
	for _, issue := range issues {
		result = append(result, compactReportIssueHeader(issue))
	}
	return result
}

func compactReportIssueReferencesForStorage(issues []ReportIssue, truncated *bool) []ReportIssue {
	result := compactReportIssueReferences(issues)
	if len(result) <= reportSnapshotMaxReferences {
		return result
	}
	if truncated != nil {
		*truncated = true
	}
	return result[:reportSnapshotMaxReferences]
}

func minimalReportSnapshot(snapshot ReportSnapshot) ReportSnapshot {
	summary := strings.TrimSpace(snapshot.ExecutiveSummary)
	if summary == "" {
		summary = snapshot.ProjectAnalysis.Summary
	}
	summary = compactReportText(summary, 4000)
	return ReportSnapshot{
		PeriodType:       compactReportText(snapshot.PeriodType, 64),
		RangeStart:       snapshot.RangeStart,
		RangeEnd:         snapshot.RangeEnd,
		Timezone:         compactReportText(snapshot.Timezone, 128),
		ProjectTitle:     compactReportText(snapshot.ProjectTitle, 500),
		GeneratedAt:      snapshot.GeneratedAt,
		SummaryVersion:   snapshot.SummaryVersion,
		AnalysisVersion:  snapshot.AnalysisVersion,
		ActiveIssueCount: snapshot.ActiveIssueCount,
		ProjectAnalysis: ReportProjectAnalysis{
			Summary: summary,
		},
		NarrativeVersion: snapshot.NarrativeVersion,
		ExecutiveSummary: summary,
		StorageTruncated: true,
		Completed:        []ReportIssue{},
		InProgress:       []ReportIssue{},
		Blocked:          []ReportIssue{},
		Overdue:          []ReportIssue{},
		Cancelled:        []ReportIssue{},
		CompletedCount:   snapshot.CompletedCount,
		InProgressCount:  snapshot.InProgressCount,
		BlockedCount:     snapshot.BlockedCount,
		OverdueCount:     snapshot.OverdueCount,
		CancelledCount:   snapshot.CancelledCount,
	}
}

func compactReportIssueSummary(summary ReportIssueSummary) ReportIssueSummary {
	summary.Problem = compactReportText(summary.Problem, 600)
	summary.Outcome = compactReportText(summary.Outcome, 600)
	summary.Decision = compactReportText(summary.Decision, 500)
	summary.CurrentState = compactReportText(summary.CurrentState, 300)
	summary.Impact = compactReportText(summary.Impact, 500)
	summary.Actions = compactReportStringList(summary.Actions, 4, 300)
	summary.OpenItems = compactReportStringList(summary.OpenItems, 4, 300)
	summary.WorkTypes = compactReportStringList(summary.WorkTypes, 6, 100)
	summary.WorkDone = compactReportStringList(summary.WorkDone, 4, 300)
	summary.Deliverables = compactReportStringList(summary.Deliverables, 4, 300)
	summary.Verification = compactReportStringList(summary.Verification, 4, 300)
	summary.Dependencies = compactReportStringList(summary.Dependencies, 4, 300)
	summary.Risks = compactReportStringList(summary.Risks, 4, 300)
	summary.Artifacts = compactReportStringList(summary.Artifacts, 4, 300)
	summary.EvidenceIDs = compactReportStringList(summary.EvidenceIDs, 12, 100)
	return summary
}

func compactReportTimeline(events []ReportTimelineEvent, truncated *bool) []ReportTimelineEvent {
	if len(events) == 0 {
		return nil
	}
	conversationEvents := reportPromptTimelineEvents(events)
	selected := cloneAndLimitTimeline(conversationEvents, truncated)
	if len(conversationEvents) > len(selected) && truncated != nil {
		*truncated = true
	}
	result := make([]ReportTimelineEvent, 0, minReportInt(len(selected), reportSnapshotMaxEventsPerIssue))
	for _, event := range selected {
		content := reportConversationEventContent(event)
		if content == "" {
			continue
		}
		event.Content = compactReportText(content, reportSnapshotMaxEventContent)
		event.Action = ""
		event.Details = nil
		if event.Type != "comment" {
			event.AuthorID = ""
			event.CommentType = ""
			event.ParentID = ""
		}
		result = append(result, event)
	}
	if len(result) > reportSnapshotMaxEventsPerIssue {
		result = result[len(result)-reportSnapshotMaxEventsPerIssue:]
		if truncated != nil {
			*truncated = true
		}
	}
	if reportTimelineBytes(result) > reportSnapshotMaxCharsPerIssue {
		result = fitTimelineToCharacterBudget(result, reportSnapshotMaxCharsPerIssue)
		if truncated != nil {
			*truncated = true
		}
	}
	return result
}

func compactReportWorkItems(items []ReportWorkItem) []ReportWorkItem {
	if len(items) == 0 {
		return nil
	}
	result := make([]ReportWorkItem, 0, minReportInt(len(items), reportSnapshotMaxIssues))
	for index, item := range items {
		if index >= reportSnapshotMaxIssues {
			break
		}
		item.IssueTitle = compactReportText(item.IssueTitle, 500)
		item.BusinessDomain = compactReportText(item.BusinessDomain, 200)
		item.Milestone = compactReportText(item.Milestone, 300)
		item.Title = compactReportText(item.Title, 500)
		item.Description = compactReportText(item.Description, 600)
		item.Decision = compactReportText(item.Decision, 400)
		item.CurrentState = compactReportText(item.CurrentState, 300)
		item.Outcome = compactReportText(item.Outcome, 600)
		item.Impact = compactReportText(item.Impact, 500)
		item.BusinessImpact = compactReportText(item.BusinessImpact, 500)
		item.Milestones = compactReportStringList(item.Milestones, 4, 200)
		item.Categories = compactReportStringList(item.Categories, 6, 100)
		item.WorkDone = compactReportStringList(item.WorkDone, 4, 300)
		item.Deliverables = compactReportStringList(item.Deliverables, 4, 300)
		item.Verification = compactReportStringList(item.Verification, 4, 300)
		item.Dependencies = compactReportStringList(item.Dependencies, 4, 300)
		item.Risks = compactReportStringList(item.Risks, 4, 300)
		item.EvidenceIDs = compactReportStringList(item.EvidenceIDs, 12, 100)
		result = append(result, item)
	}
	return result
}

func compactReportProjectAnalysis(analysis ReportProjectAnalysis) ReportProjectAnalysis {
	analysis.Summary = compactReportText(analysis.Summary, 2000)
	analysis.EvidenceIDs = compactReportStringList(analysis.EvidenceIDs, 30, 100)
	if len(analysis.BusinessDomains) > 50 {
		analysis.BusinessDomains = analysis.BusinessDomains[:50]
	}
	for index := range analysis.BusinessDomains {
		domain := &analysis.BusinessDomains[index]
		domain.ID = compactReportText(domain.ID, 120)
		domain.Name = compactReportText(domain.Name, 200)
		domain.Summary = compactReportText(domain.Summary, 600)
		domain.BusinessImpact = compactReportText(domain.BusinessImpact, 500)
		domain.WorkItemIDs = compactReportStringList(domain.WorkItemIDs, 50, 100)
		domain.EvidenceIDs = compactReportStringList(domain.EvidenceIDs, 20, 100)
		if len(domain.Milestones) > 20 {
			domain.Milestones = domain.Milestones[:20]
		}
		for milestoneIndex := range domain.Milestones {
			domain.Milestones[milestoneIndex] = compactReportMilestone(domain.Milestones[milestoneIndex])
		}
	}
	if len(analysis.Milestones) > 100 {
		analysis.Milestones = analysis.Milestones[:100]
	}
	for index := range analysis.Milestones {
		analysis.Milestones[index] = compactReportMilestone(analysis.Milestones[index])
	}
	if len(analysis.Changes) > 100 {
		analysis.Changes = analysis.Changes[:100]
	}
	for index := range analysis.Changes {
		change := &analysis.Changes[index]
		change.ID = compactReportText(change.ID, 120)
		change.Category = compactReportText(change.Category, 100)
		change.Title = compactReportText(change.Title, 300)
		change.Description = compactReportText(change.Description, 600)
		change.Impact = compactReportText(change.Impact, 500)
		change.EvidenceIDs = compactReportStringList(change.EvidenceIDs, 20, 100)
	}
	analysis.Risks = compactReportAnalysisNotes(analysis.Risks)
	analysis.NextSteps = compactReportAnalysisNotes(analysis.NextSteps)
	return analysis
}

func compactReportMilestone(milestone ReportMilestone) ReportMilestone {
	milestone.ID = compactReportText(milestone.ID, 120)
	milestone.BusinessDomain = compactReportText(milestone.BusinessDomain, 200)
	milestone.Title = compactReportText(milestone.Title, 300)
	milestone.Summary = compactReportText(milestone.Summary, 600)
	milestone.WorkItemIDs = compactReportStringList(milestone.WorkItemIDs, 50, 100)
	milestone.EvidenceIDs = compactReportStringList(milestone.EvidenceIDs, 20, 100)
	return milestone
}

func compactReportAnalysisNotes(notes []ReportAnalysisNote) []ReportAnalysisNote {
	if len(notes) > 20 {
		notes = notes[:20]
	}
	result := make([]ReportAnalysisNote, 0, len(notes))
	for _, note := range notes {
		note.Title = compactReportText(note.Title, 300)
		note.Description = compactReportText(note.Description, 600)
		note.EvidenceIDs = compactReportStringList(note.EvidenceIDs, 20, 100)
		result = append(result, note)
	}
	return result
}

func compactReportNarratives(narratives []ReportIssueNarrative, maxItems int) []ReportIssueNarrative {
	if len(narratives) > maxItems {
		narratives = narratives[:maxItems]
	}
	result := make([]ReportIssueNarrative, 0, len(narratives))
	for _, narrative := range narratives {
		narrative.Identifier = compactReportText(narrative.Identifier, 100)
		narrative.Title = compactReportText(narrative.Title, 500)
		narrative.Business = compactReportText(narrative.Business, 200)
		narrative.StatusFrom = compactReportText(narrative.StatusFrom, 100)
		narrative.StatusTo = compactReportText(narrative.StatusTo, 100)
		narrative.Done = compactReportText(narrative.Done, 600)
		narrative.Outcome = compactReportText(narrative.Outcome, 600)
		narrative.Evidence = compactReportStringList(narrative.Evidence, 8, 120)
		narrative.Risks = compactReportStringList(narrative.Risks, 4, 300)
		result = append(result, narrative)
	}
	return result
}

func compactReportStringList(values []string, maxItems, maxLength int) []string {
	if len(values) == 0 || maxItems <= 0 {
		return nil
	}
	result := make([]string, 0, minReportInt(len(values), maxItems))
	for _, value := range values {
		value = compactReportText(value, maxLength)
		if value == "" {
			continue
		}
		result = append(result, value)
		if len(result) == maxItems {
			break
		}
	}
	return result
}

func compactReportText(value string, maxLength int) string {
	return truncateReportText(strings.TrimSpace(sanitizeSensitiveText(value)), maxLength)
}

func reportTimelineBytes(events []ReportTimelineEvent) int {
	total := 0
	for _, event := range events {
		total += eventChars(event)
	}
	return total
}

func reportSnapshotEventCount(issues []ReportIssue) int {
	total := 0
	for _, issue := range issues {
		total += len(issue.Timeline)
	}
	return total
}

// generateContent keeps the legacy path available for old saved snapshots and
// focused compatibility tests. New snapshots use the narrative pipeline in
// build, which always returns deterministic output when the model is absent.
func (g *ReportGenerator) generateContent(ctx context.Context, snapshot ReportSnapshot, templatePrompt string) (string, error) {
	if len(snapshot.Issues) > 0 {
		snapshot = g.withIssueSummaries(ctx, snapshot)
		snapshot = g.withProjectAnalysis(ctx, snapshot)
		return buildIssueReportContent(snapshot), nil
	}

	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("encode report prompt: %w", err)
	}
	if g.LLM == nil || !g.LLM.Enabled() {
		return buildDeterministicReportContent(snapshot), nil
	}

	sysPrompt := legacyReportSystemPrompt
	if templatePrompt != "" {
		sysPrompt = templatePrompt
	}

	generateCtx, cancel := context.WithTimeout(ctx, reportLLMTimeout)
	defer cancel()
	raw, err := g.LLM.GenerateJSON(
		generateCtx,
		"",
		sysPrompt,
		string(snapshotJSON),
		reportLLMTemperature,
		reportLLMMaxCompletionToken,
	)
	if err != nil {
		return deterministicReportFallback(snapshot, "LLM request failed", err)
	}
	raw = llm.StripJSONFence(raw)
	var payload struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return deterministicReportFallback(snapshot, "LLM returned invalid JSON", err)
	}
	content := strings.TrimSpace(buildReportContent(payload.Content, snapshot))
	if content == "" {
		return deterministicReportFallback(snapshot, "LLM returned empty content", nil)
	}
	return content, nil
}

func (g *ReportGenerator) withIssueSummaries(ctx context.Context, snapshot ReportSnapshot) ReportSnapshot {
	for index := range snapshot.Issues {
		snapshot.Issues[index].TimelineTruncated = timelineNeedsTruncation(snapshot.Issues[index].Timeline)
		snapshot.Issues[index].Summary = deterministicIssueSummary(snapshot.Issues[index])
	}
	if g.LLM == nil || !g.LLM.Enabled() || len(snapshot.Issues) == 0 {
		return snapshot
	}

	prompt := prepareReportPrompt(snapshot)
	promptJSON, err := json.Marshal(prompt)
	if err != nil {
		slog.Warn("project report: unable to encode issue prompt", "error", err)
		return snapshot
	}

	generateCtx, cancel := context.WithTimeout(ctx, reportLLMTimeout)
	defer cancel()
	raw, err := g.LLM.GenerateJSON(
		generateCtx,
		"",
		reportSystemPrompt,
		string(promptJSON),
		reportLLMTemperature,
		reportLLMMaxCompletionToken,
	)
	if err != nil {
		slog.Warn("project report: using per-issue fallback", "reason", "LLM request failed", "error", err)
		return snapshot
	}

	summaries, err := parseIssueSummaries(raw, snapshot.Issues)
	if err != nil {
		slog.Warn("project report: using per-issue fallback", "reason", "LLM returned invalid summaries", "error", err)
		return snapshot
	}
	for index := range snapshot.Issues {
		issueID := snapshot.Issues[index].IssueID
		if summary, ok := summaries[issueID]; ok {
			summary = mergeIssueSummary(summary, deterministicIssueSummary(snapshot.Issues[index]))
			summary.SummarySource = "ai"
			snapshot.Issues[index].Summary = summary
		}
	}
	return snapshot
}

func mergeIssueSummary(ai, fallback ReportIssueSummary) ReportIssueSummary {
	ai.Problem = strings.TrimSpace(sanitizeSensitiveText(ai.Problem))
	ai.Outcome = strings.TrimSpace(sanitizeSensitiveText(ai.Outcome))
	if ai.Problem == "" {
		ai.Problem = fallback.Problem
	}
	if ai.Outcome == "" {
		ai.Outcome = fallback.Outcome
	}
	if len(ai.Actions) == 0 {
		ai.Actions = fallback.Actions
	}
	if len(ai.OpenItems) == 0 {
		ai.OpenItems = fallback.OpenItems
	}
	if len(ai.WorkTypes) == 0 {
		ai.WorkTypes = fallback.WorkTypes
	}
	if len(ai.WorkDone) == 0 {
		ai.WorkDone = fallback.WorkDone
	}
	if strings.TrimSpace(ai.Decision) == "" {
		ai.Decision = fallback.Decision
	}
	if len(ai.Deliverables) == 0 {
		ai.Deliverables = fallback.Deliverables
	}
	if len(ai.Verification) == 0 {
		ai.Verification = fallback.Verification
	}
	if strings.TrimSpace(ai.CurrentState) == "" {
		ai.CurrentState = fallback.CurrentState
	}
	if len(ai.Dependencies) == 0 {
		ai.Dependencies = fallback.Dependencies
	}
	if len(ai.Risks) == 0 {
		ai.Risks = fallback.Risks
	}
	if len(ai.Artifacts) == 0 {
		ai.Artifacts = fallback.Artifacts
	}
	if strings.TrimSpace(ai.Impact) == "" {
		ai.Impact = fallback.Impact
	}
	if len(ai.EvidenceIDs) == 0 {
		ai.EvidenceIDs = fallback.EvidenceIDs
	}
	ai.Confidence = normalizeReportConfidence(ai.Confidence)
	return sanitizeReportIssueSummary(ai)
}

func (g *ReportGenerator) withProjectAnalysis(ctx context.Context, snapshot ReportSnapshot) ReportSnapshot {
	snapshot.AnalysisVersion = reportAnalysisVersion
	snapshot.WorkItems = buildReportWorkItems(snapshot)
	snapshot.ProjectAnalysis = deterministicProjectAnalysis(snapshot)
	if g.LLM == nil || !g.LLM.Enabled() || len(snapshot.WorkItems) == 0 {
		return snapshot
	}

	prompt, err := json.Marshal(prepareProjectPrompt(snapshot))
	if err != nil {
		return appendReportAnalysisWarning(snapshot, "项目业务分析输入编码失败，已使用确定性摘要。")
	}

	generateCtx, cancel := context.WithTimeout(ctx, reportLLMTimeout)
	defer cancel()
	raw, err := g.LLM.GenerateJSON(
		generateCtx,
		"",
		projectReportSystemPrompt,
		string(prompt),
		reportLLMTemperature,
		reportLLMMaxCompletionToken,
	)
	if err != nil {
		slog.Warn("project report: using deterministic project analysis", "reason", "LLM request failed", "error", err)
		return appendReportAnalysisWarning(snapshot, "项目业务分析暂不可用，已使用确定性摘要。")
	}

	analysis, err := parseProjectAnalysis(raw, snapshot)
	if err != nil {
		slog.Warn("project report: using deterministic project analysis", "reason", "LLM returned invalid project analysis", "error", err)
		return appendReportAnalysisWarning(snapshot, "项目业务分析结果未通过证据校验，已使用确定性摘要。")
	}
	snapshot.ProjectAnalysis = mergeProjectAnalysis(analysis, snapshot.ProjectAnalysis)
	return snapshot
}

type reportProjectPrompt struct {
	PeriodType         string               `json:"period_type"`
	RangeStart         time.Time            `json:"range_start"`
	RangeEnd           time.Time            `json:"range_end"`
	Timezone           string               `json:"timezone"`
	ProjectTitle       string               `json:"project_title,omitempty"`
	ProjectDescription string               `json:"project_description,omitempty"`
	ActiveIssueCount   int                  `json:"active_issue_count"`
	WorkItems          []ReportWorkItem     `json:"work_items"`
	Risks              []ReportAnalysisNote `json:"risks"`
}

func prepareProjectPrompt(snapshot ReportSnapshot) reportProjectPrompt {
	workItems := make([]ReportWorkItem, 0, len(snapshot.WorkItems))
	for _, item := range snapshot.WorkItems {
		prepared := item
		prepared.IssueTitle = sanitizeSensitiveText(prepared.IssueTitle)
		prepared.BusinessDomain = sanitizeSensitiveText(prepared.BusinessDomain)
		prepared.Milestone = sanitizeSensitiveText(prepared.Milestone)
		prepared.Title = sanitizeSensitiveText(prepared.Title)
		prepared.Description = sanitizeSensitiveText(prepared.Description)
		prepared.Decision = sanitizeSensitiveText(prepared.Decision)
		prepared.Outcome = sanitizeSensitiveText(prepared.Outcome)
		prepared.Impact = sanitizeSensitiveText(prepared.Impact)
		prepared.BusinessImpact = sanitizeSensitiveText(prepared.BusinessImpact)
		prepared.WorkDone = sanitizeReportList(prepared.WorkDone)
		prepared.Deliverables = sanitizeReportList(prepared.Deliverables)
		prepared.Verification = sanitizeReportList(prepared.Verification)
		prepared.Dependencies = sanitizeReportList(prepared.Dependencies)
		prepared.Risks = sanitizeReportList(prepared.Risks)
		workItems = append(workItems, prepared)
	}
	deterministic := snapshot.ProjectAnalysis
	risks := append([]ReportAnalysisNote(nil), deterministic.Risks...)
	for index := range risks {
		risks[index].Title = sanitizeSensitiveText(risks[index].Title)
		risks[index].Description = sanitizeSensitiveText(risks[index].Description)
	}
	return reportProjectPrompt{
		PeriodType:         snapshot.PeriodType,
		RangeStart:         snapshot.RangeStart,
		RangeEnd:           snapshot.RangeEnd,
		Timezone:           snapshot.Timezone,
		ProjectTitle:       sanitizeSensitiveText(snapshot.ProjectTitle),
		ProjectDescription: sanitizeSensitiveText(snapshot.ProjectDescription),
		ActiveIssueCount:   snapshot.ActiveIssueCount,
		WorkItems:          workItems,
		Risks:              risks,
	}
}

func parseProjectAnalysis(raw string, snapshot ReportSnapshot) (ReportProjectAnalysis, error) {
	raw = llm.StripJSONFence(raw)
	var analysis ReportProjectAnalysis
	if err := json.Unmarshal([]byte(raw), &analysis); err != nil {
		return ReportProjectAnalysis{}, err
	}
	analysis.Summary = strings.TrimSpace(sanitizeSensitiveText(analysis.Summary))
	if analysis.Summary == "" || len(analysis.Summary) > 3000 {
		return ReportProjectAnalysis{}, fmt.Errorf("project analysis summary is missing or too long")
	}

	validEvidence := reportInRangeEvidenceSet(snapshot)
	analysis.EvidenceIDs = validateReportEvidenceIDs(analysis.EvidenceIDs, validEvidence)
	if len(validEvidence) > 0 && len(analysis.EvidenceIDs) == 0 {
		return ReportProjectAnalysis{}, fmt.Errorf("project analysis has no valid evidence")
	}
	validWorkItems := make(map[string]struct{}, len(snapshot.WorkItems))
	for _, item := range snapshot.WorkItems {
		if strings.TrimSpace(item.ID) != "" {
			validWorkItems[item.ID] = struct{}{}
		}
	}
	var err error
	analysis.BusinessDomains, err = normalizeBusinessDomains(analysis.BusinessDomains, validEvidence, validWorkItems)
	if err != nil {
		return ReportProjectAnalysis{}, err
	}
	analysis.Milestones, err = normalizeMilestones(analysis.Milestones, validEvidence, validWorkItems)
	if err != nil {
		return ReportProjectAnalysis{}, err
	}
	analysis.Changes, err = normalizeProjectChanges(analysis.Changes, validEvidence)
	if err != nil {
		return ReportProjectAnalysis{}, err
	}
	analysis.Risks, err = normalizeAnalysisNotes(analysis.Risks, validEvidence)
	if err != nil {
		return ReportProjectAnalysis{}, err
	}
	analysis.NextSteps, err = normalizeAnalysisNotes(analysis.NextSteps, validEvidence)
	if err != nil {
		return ReportProjectAnalysis{}, err
	}
	analysis.Confidence = normalizeReportConfidence(analysis.Confidence)
	analysis.Source = "ai"
	return analysis, nil
}

func normalizeBusinessDomains(domains []ReportBusinessDomain, validEvidence, validWorkItems map[string]struct{}) ([]ReportBusinessDomain, error) {
	if len(domains) > 20 {
		return nil, fmt.Errorf("too many business domains")
	}
	result := make([]ReportBusinessDomain, 0, len(domains))
	for index, domain := range domains {
		domain.ID = strings.TrimSpace(domain.ID)
		if domain.ID == "" {
			domain.ID = fmt.Sprintf("domain-%d", index+1)
		}
		domain.Name = strings.TrimSpace(sanitizeSensitiveText(domain.Name))
		domain.Summary = strings.TrimSpace(sanitizeSensitiveText(domain.Summary))
		domain.BusinessImpact = strings.TrimSpace(sanitizeSensitiveText(domain.BusinessImpact))
		if domain.Name == "" || domain.Summary == "" || len(domain.Name) > 200 || len(domain.Summary) > 2000 || len(domain.BusinessImpact) > 1500 {
			return nil, fmt.Errorf("invalid business domain at index %d", index)
		}
		domain.WorkItemIDs = validateReportIDs(domain.WorkItemIDs, validWorkItems, 50)
		domain.EvidenceIDs = validateReportEvidenceIDs(domain.EvidenceIDs, validEvidence)
		if len(validWorkItems) > 0 && len(domain.WorkItemIDs) == 0 {
			return nil, fmt.Errorf("business domain %q has no valid work items", domain.Name)
		}
		if len(validEvidence) > 0 && len(domain.EvidenceIDs) == 0 {
			return nil, fmt.Errorf("business domain %q has no valid evidence", domain.Name)
		}
		var err error
		domain.Milestones, err = normalizeMilestones(domain.Milestones, validEvidence, validWorkItems)
		if err != nil {
			return nil, err
		}
		domain.Confidence = normalizeReportConfidence(domain.Confidence)
		domain.Source = "ai"
		result = append(result, domain)
	}
	return result, nil
}

func normalizeMilestones(milestones []ReportMilestone, validEvidence, validWorkItems map[string]struct{}) ([]ReportMilestone, error) {
	if len(milestones) > 40 {
		return nil, fmt.Errorf("too many milestones")
	}
	result := make([]ReportMilestone, 0, len(milestones))
	for index, milestone := range milestones {
		milestone.ID = strings.TrimSpace(milestone.ID)
		if milestone.ID == "" {
			milestone.ID = fmt.Sprintf("milestone-%d", index+1)
		}
		milestone.BusinessDomain = strings.TrimSpace(sanitizeSensitiveText(milestone.BusinessDomain))
		milestone.Title = strings.TrimSpace(sanitizeSensitiveText(milestone.Title))
		milestone.Summary = strings.TrimSpace(sanitizeSensitiveText(milestone.Summary))
		milestone.Status = strings.TrimSpace(sanitizeSensitiveText(milestone.Status))
		if milestone.Status == "" {
			milestone.Status = "待确认"
		}
		if milestone.Title == "" || milestone.Summary == "" || len(milestone.Title) > 300 || len(milestone.Summary) > 2500 || len(milestone.BusinessDomain) > 200 {
			return nil, fmt.Errorf("invalid milestone at index %d", index)
		}
		milestone.WorkItemIDs = validateReportIDs(milestone.WorkItemIDs, validWorkItems, 50)
		milestone.EvidenceIDs = validateReportEvidenceIDs(milestone.EvidenceIDs, validEvidence)
		if len(validWorkItems) > 0 && len(milestone.WorkItemIDs) == 0 {
			return nil, fmt.Errorf("milestone %q has no valid work items", milestone.Title)
		}
		if len(validEvidence) > 0 && len(milestone.EvidenceIDs) == 0 {
			return nil, fmt.Errorf("milestone %q has no valid evidence", milestone.Title)
		}
		milestone.Confidence = normalizeReportConfidence(milestone.Confidence)
		milestone.Source = "ai"
		result = append(result, milestone)
	}
	return result, nil
}

func validateReportIDs(ids []string, valid map[string]struct{}, limit int) []string {
	result := make([]string, 0, minReportInt(len(ids), limit))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if len(valid) > 0 {
			if _, ok := valid[id]; !ok {
				continue
			}
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
		if len(result) == limit {
			break
		}
	}
	return result
}

func normalizeProjectChanges(changes []ReportProjectChange, validEvidence map[string]struct{}) ([]ReportProjectChange, error) {
	if len(changes) > 20 {
		return nil, fmt.Errorf("too many project changes")
	}
	result := make([]ReportProjectChange, 0, len(changes))
	for index, change := range changes {
		change.ID = strings.TrimSpace(change.ID)
		if change.ID == "" {
			change.ID = fmt.Sprintf("change-%d", index+1)
		}
		change.Category = normalizeReportCategory(change.Category)
		change.Title = strings.TrimSpace(sanitizeSensitiveText(change.Title))
		change.Description = strings.TrimSpace(sanitizeSensitiveText(change.Description))
		change.Impact = strings.TrimSpace(sanitizeSensitiveText(change.Impact))
		change.Status = strings.TrimSpace(sanitizeSensitiveText(change.Status))
		if change.Status == "" {
			change.Status = "待确认"
		}
		if change.Title == "" || change.Description == "" || len(change.Title) > 500 || len(change.Description) > 2000 || len(change.Impact) > 1500 {
			return nil, fmt.Errorf("invalid project change at index %d", index)
		}
		change.EvidenceIDs = validateReportEvidenceIDs(change.EvidenceIDs, validEvidence)
		if len(change.EvidenceIDs) == 0 {
			return nil, fmt.Errorf("project change %q has no valid evidence", change.Title)
		}
		change.Confidence = normalizeReportConfidence(change.Confidence)
		change.Source = "ai"
		result = append(result, change)
	}
	return result, nil
}

func normalizeAnalysisNotes(notes []ReportAnalysisNote, validEvidence map[string]struct{}) ([]ReportAnalysisNote, error) {
	if len(notes) > 20 {
		return nil, fmt.Errorf("too many analysis notes")
	}
	result := make([]ReportAnalysisNote, 0, len(notes))
	for index, note := range notes {
		note.Title = strings.TrimSpace(sanitizeSensitiveText(note.Title))
		note.Description = strings.TrimSpace(sanitizeSensitiveText(note.Description))
		if note.Title == "" || note.Description == "" || len(note.Title) > 500 || len(note.Description) > 2000 {
			return nil, fmt.Errorf("invalid analysis note at index %d", index)
		}
		note.EvidenceIDs = validateReportEvidenceIDs(note.EvidenceIDs, validEvidence)
		if len(note.EvidenceIDs) == 0 {
			return nil, fmt.Errorf("analysis note %q has no valid evidence", note.Title)
		}
		note.Confidence = normalizeReportConfidence(note.Confidence)
		note.Source = "ai"
		result = append(result, note)
	}
	return result, nil
}

func mergeProjectAnalysis(ai, fallback ReportProjectAnalysis) ReportProjectAnalysis {
	if len(ai.BusinessDomains) == 0 {
		ai.BusinessDomains = fallback.BusinessDomains
	}
	if len(ai.Milestones) == 0 {
		ai.Milestones = fallback.Milestones
	}
	if len(ai.Changes) == 0 {
		ai.Changes = fallback.Changes
	}
	if len(ai.Risks) == 0 {
		ai.Risks = fallback.Risks
	}
	if len(ai.NextSteps) == 0 {
		ai.NextSteps = fallback.NextSteps
	}
	if len(ai.EvidenceIDs) == 0 {
		ai.EvidenceIDs = fallback.EvidenceIDs
	}
	if ai.Confidence == "" {
		ai.Confidence = "medium"
	}
	ai.Source = "ai"
	return ai
}

func appendReportAnalysisWarning(snapshot ReportSnapshot, warning string) ReportSnapshot {
	if warning == "" {
		return snapshot
	}
	for _, existing := range snapshot.AnalysisWarnings {
		if existing == warning {
			return snapshot
		}
	}
	snapshot.AnalysisWarnings = append(snapshot.AnalysisWarnings, warning)
	return snapshot
}

var reportCategoryKeywords = map[string][]string{
	"bug_fix": {
		"bug", "bugfix", "fix", "defect", "regression", "error", "crash", "异常", "报错", "缺陷", "修复", "故障",
	},
	"feature": {
		"feature", "capability", "enhancement", "功能", "能力", "新增", "增加", "支持", "上线",
	},
	"architecture": {
		"architecture", "architect", "refactor", "schema", "performance", "scalability", "infra", "架构", "重构", "性能", "扩展性", "基础设施",
	},
	"design": {
		"design", "proposal", "solution", "方案", "设计", "决策", "技术选型", "规范",
	},
	"research": {
		"research", "investigate", "investigation", "spike", "调研", "研究", "分析", "排查", "验证",
	},
	"operations": {
		"deploy", "deployment", "release", "build", "config", "configuration", "ops", "运维", "部署", "发布", "构建", "配置", "重启",
	},
	"discussion": {
		"discussion", "comment", "review", "feedback", "discuss", "讨论", "评审", "审核", "沟通",
	},
	"risk": {
		"blocked", "blocker", "risk", "dependency", "阻塞", "风险", "依赖", "待确认",
	},
}

var reportCategoryOrder = []string{
	"bug_fix",
	"feature",
	"architecture",
	"design",
	"research",
	"operations",
	"discussion",
	"risk",
	"misc",
}

func normalizeReportCategory(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "bug", "bugfix", "bug_fix", "defect", "fix":
		return "bug_fix"
	case "feature", "function", "capability", "enhancement":
		return "feature"
	case "architecture", "architect", "refactor":
		return "architecture"
	case "design", "proposal", "solution":
		return "design"
	case "research", "investigation", "analysis":
		return "research"
	case "operations", "operation", "ops", "deployment", "release":
		return "operations"
	case "discussion", "review", "communication":
		return "discussion"
	case "risk", "blocker", "blocked":
		return "risk"
	case "misc", "other":
		return "misc"
	default:
		return "misc"
	}
}

func normalizeReportConfidence(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "high":
		return "high"
	case "medium", "中":
		return "medium"
	case "low", "低":
		return "low"
	default:
		return "low"
	}
}

func reportInRangeEvidenceSet(snapshot ReportSnapshot) map[string]struct{} {
	valid := make(map[string]struct{})
	for _, issue := range snapshot.Issues {
		for _, event := range issue.Timeline {
			if event.InRange && strings.TrimSpace(event.ID) != "" {
				valid[event.ID] = struct{}{}
			}
		}
	}
	return valid
}

func validateReportEvidenceIDs(ids []string, valid map[string]struct{}) []string {
	if len(ids) == 0 || len(valid) == 0 {
		return nil
	}
	result := make([]string, 0, minReportInt(len(ids), 20))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := valid[id]; !ok {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
		if len(result) == 20 {
			break
		}
	}
	return result
}

func minReportInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func reportIssueEvidence(issue ReportIssue) []string {
	result := make([]string, 0, len(issue.Timeline))
	seen := make(map[string]struct{}, len(issue.Timeline))
	for _, event := range issue.Timeline {
		if !event.InRange || strings.TrimSpace(event.ID) == "" || reportConversationEventContent(event) == "" {
			continue
		}
		if _, duplicate := seen[event.ID]; duplicate {
			continue
		}
		seen[event.ID] = struct{}{}
		result = append(result, event.ID)
	}
	return result
}

func reportTextForClassification(issue ReportIssue) string {
	parts := []string{issue.Title, issue.Description, issue.Status}
	for _, event := range issue.Timeline {
		if !event.InRange {
			continue
		}
		parts = append(parts, event.Content, event.Action, string(event.Details))
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func inferReportCategories(issue ReportIssue) []string {
	text := reportTextForClassification(issue)
	categories := make([]string, 0, len(reportCategoryOrder))
	for _, category := range reportCategoryOrder[:len(reportCategoryOrder)-1] {
		for _, keyword := range reportCategoryKeywords[category] {
			if strings.Contains(text, strings.ToLower(keyword)) {
				categories = append(categories, category)
				break
			}
		}
	}
	if len(categories) == 0 {
		categories = append(categories, "misc")
	}
	return categories
}

func normalizeReportCategories(values []string, fallback []string) []string {
	result := make([]string, 0, minReportInt(len(values), 8))
	seen := make(map[string]struct{}, 8)
	for _, value := range values {
		category := normalizeReportCategory(value)
		if category == "misc" && strings.TrimSpace(value) == "" {
			continue
		}
		if _, duplicate := seen[category]; duplicate {
			continue
		}
		seen[category] = struct{}{}
		result = append(result, category)
		if len(result) == 8 {
			break
		}
	}
	if len(result) == 0 {
		for _, category := range fallback {
			if _, duplicate := seen[category]; duplicate {
				continue
			}
			seen[category] = struct{}{}
			result = append(result, category)
			if len(result) == 8 {
				break
			}
		}
	}
	if len(result) == 0 {
		return []string{"misc"}
	}
	return result
}

func reportEventWorkDescription(event ReportTimelineEvent) string {
	if value := reportConversationEventContent(event); value != "" {
		return value
	}

	// Status transitions and generic activity-log rows are bookkeeping. They
	// remain available in the snapshot for counters and auditability, but must
	// not become work facts in the narrative or deterministic fallback.
	if event.Type == "issue_status_history" || event.Type == "activity_log" {
		return ""
	}

	var value string
	switch event.Type {
	default:
		value = strings.TrimSpace(strings.Join([]string{event.Action, event.Content}, " · "))
	}
	value = strings.Join(strings.Fields(sanitizeSensitiveText(value)), " ")
	return truncateReportText(value, 500)
}

// reportConversationEventContent is the Stage-0 allowlist. Only discussion
// bodies and useful agent-task outcomes are allowed to become work facts or
// LLM prompt lines. Status changes and generic activity records deliberately
// return an empty string.
func reportConversationEventContent(event ReportTimelineEvent) string {
	switch event.Type {
	case "comment":
		return truncateReportText(strings.Join(strings.Fields(sanitizeSensitiveText(event.Content)), " "), 500)
	case "agent_task_queue", "agent_task":
		if content := strings.TrimSpace(sanitizeSensitiveText(event.Content)); content != "" {
			return truncateReportText(strings.Join(strings.Fields(content), " "), 500)
		}
		return reportAgentTaskOutcome(event)
	default:
		return ""
	}
}

func reportAgentTaskOutcome(event ReportTimelineEvent) string {
	var details struct {
		Status string          `json:"status"`
		Error  string          `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if len(event.Details) > 0 {
		if err := json.Unmarshal(event.Details, &details); err != nil {
			return ""
		}
	}

	if errorText := reportJSONValueText(details.Error); errorText != "" {
		return truncateReportText("任务失败："+errorText, 500)
	}
	if resultText := reportJSONValueText(details.Result); resultText != "" {
		return truncateReportText("任务结果："+resultText, 500)
	}

	// A terminal task without a result still carries a useful, bounded fact.
	switch strings.ToLower(strings.TrimSpace(details.Status)) {
	case "completed", "succeeded", "success", "done":
		return "执行完成"
	case "failed", "error":
		return "执行失败"
	}
	return ""
}

func reportJSONValueText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.Join(strings.Fields(sanitizeSensitiveText(typed)), " ")
	case json.RawMessage:
		if len(typed) == 0 || string(typed) == "null" {
			return ""
		}
		var stringValue string
		if json.Unmarshal(typed, &stringValue) == nil {
			return strings.Join(strings.Fields(sanitizeSensitiveText(stringValue)), " ")
		}
		var generic any
		if json.Unmarshal(typed, &generic) != nil {
			return ""
		}
		encoded, err := json.Marshal(generic)
		if err != nil || string(encoded) == "null" {
			return ""
		}
		return strings.Join(strings.Fields(sanitizeSensitiveText(string(encoded))), " ")
	default:
		return strings.Join(strings.Fields(sanitizeSensitiveText(fmt.Sprint(typed))), " ")
	}
}

func reportStatusLabel(status string) string {
	return map[string]string{
		"backlog":     "待规划",
		"todo":        "待处理",
		"in_progress": "进行中",
		"in_review":   "待审核",
		"done":        "已完成",
		"blocked":     "阻塞",
		"cancelled":   "已取消",
	}[status]
}

func reportMilestoneLabel(category string) string {
	switch normalizeReportCategory(category) {
	case "bug_fix":
		return "问题定位与修复"
	case "feature":
		return "功能与能力交付"
	case "architecture":
		return "架构与配置改造"
	case "design":
		return "需求与方案决策"
	case "research":
		return "调研与问题分析"
	case "operations":
		return "验证与发布运维"
	case "discussion":
		return "讨论与协作决策"
	case "risk":
		return "风险与依赖处理"
	default:
		return "工作推进"
	}
}

func reportMilestonesForCategories(categories []string) []string {
	result := make([]string, 0, len(categories))
	for _, category := range categories {
		result = appendUniqueReportText(result, reportMilestoneLabel(category), 8)
	}
	if len(result) == 0 {
		return []string{"工作推进"}
	}
	return result
}

func buildReportWorkItems(snapshot ReportSnapshot) []ReportWorkItem {
	items := make([]ReportWorkItem, 0, len(snapshot.Issues))
	for _, issue := range snapshot.Issues {
		fallbackCategories := inferReportCategories(issue)
		categories := normalizeReportCategories(issue.Summary.WorkTypes, fallbackCategories)
		category := categories[0]
		workDone := make([]string, 0, 8)
		for _, value := range issue.Summary.WorkDone {
			value = strings.TrimSpace(sanitizeSensitiveText(value))
			workDone = appendUniqueReportText(workDone, truncateReportText(value, 500), 8)
		}
		if len(workDone) == 0 {
			workDone = reportIssueWorkFacts(issue)
		}
		if len(workDone) == 0 {
			workDone = append(workDone, "当前没有可进一步拆分的具体工作内容，需人工确认。")
		}
		description := strings.Join(workDone, "；")
		outcome := strings.TrimSpace(sanitizeSensitiveText(issue.Summary.Outcome))
		if outcome == "" {
			label := reportStatusLabel(issue.Status)
			if label == "" {
				label = issue.Status
			}
			outcome = "当前状态：" + label + "。"
		}
		confidence := normalizeReportConfidence(issue.Summary.Confidence)
		impact := strings.TrimSpace(sanitizeSensitiveText(issue.Summary.Impact))
		if impact == "" {
			impact = "业务影响待确认。"
		}
		businessImpact := strings.TrimSpace(sanitizeSensitiveText(issue.Summary.Impact))
		if businessImpact == "" {
			businessImpact = impact
		}
		evidenceIDs := validateReportEvidenceIDs(issue.Summary.EvidenceIDs, reportEvidenceSetForIssue(issue))
		if len(evidenceIDs) == 0 {
			evidenceIDs = reportIssueEvidence(issue)
		}
		title := strings.TrimSpace(issue.Title)
		if title == "" {
			title = issue.Identifier
		}
		milestones := reportMilestonesForCategories(categories)
		currentState := strings.TrimSpace(issue.Summary.CurrentState)
		if currentState == "" {
			currentState = reportStatusLabel(issue.Status)
		}
		if currentState == "" {
			currentState = issue.Status
		}
		decision := strings.TrimSpace(sanitizeSensitiveText(issue.Summary.Decision))
		if decision == "" {
			decision = reportFirstMatchingFact(issue, []string{"方案", "决定", "确认", "拍板", "采用", "设计"})
		}
		deliverables := normalizeReportStringList(issue.Summary.Deliverables, 8, 1000)
		if len(deliverables) == 0 {
			deliverables = reportMatchingFacts(issue, []string{"实现", "新增", "完成", "支持", "产出", "生成", "上线"})
		}
		verification := normalizeReportStringList(issue.Summary.Verification, 8, 1000)
		if len(verification) == 0 {
			verification = reportMatchingFacts(issue, []string{"验证", "测试", "实测", "通过", "回归", "成功", "200"})
		}
		dependencies := normalizeReportStringList(issue.Summary.Dependencies, 8, 1000)
		if len(dependencies) == 0 {
			dependencies = reportMatchingFacts(issue, []string{"依赖", "等待", "前置", "阻塞", "需要"})
		}
		risks := normalizeReportStringList(issue.Summary.Risks, 8, 1000)
		if len(risks) == 0 {
			risks = reportMatchingFacts(issue, []string{"失败", "错误", "超时", "风险", "鉴权", "配置", "不稳定"})
		}
		items = append(items, ReportWorkItem{
			ID:             issue.IssueID,
			IssueID:        issue.IssueID,
			Identifier:     issue.Identifier,
			IssueTitle:     title,
			BusinessDomain: nonEmptyReportText(issue.BusinessDomain, reportUnclassifiedBusinessDomain),
			Milestone:      milestones[0],
			Milestones:     milestones,
			Category:       category,
			Categories:     categories,
			Title:          title,
			Description:    description,
			WorkDone:       workDone,
			Decision:       decision,
			Deliverables:   deliverables,
			Verification:   verification,
			CurrentState:   currentState,
			Dependencies:   dependencies,
			Risks:          risks,
			Outcome:        outcome,
			Impact:         impact,
			BusinessImpact: businessImpact,
			Status:         issue.Status,
			EvidenceIDs:    evidenceIDs,
			Confidence:     confidence,
			Source:         issue.Summary.SummarySource,
		})
	}
	return items
}

func reportEvidenceSetForIssue(issue ReportIssue) map[string]struct{} {
	valid := make(map[string]struct{})
	for _, event := range issue.Timeline {
		if event.InRange && strings.TrimSpace(event.ID) != "" && reportConversationEventContent(event) != "" {
			valid[event.ID] = struct{}{}
		}
	}
	return valid
}

func reportUniqueEvidenceIDs(items []ReportWorkItem) []string {
	result := make([]string, 0)
	seen := make(map[string]struct{})
	for _, item := range items {
		for _, evidenceID := range item.EvidenceIDs {
			if _, duplicate := seen[evidenceID]; duplicate {
				continue
			}
			seen[evidenceID] = struct{}{}
			result = append(result, evidenceID)
		}
	}
	return result
}

func deterministicProjectAnalysis(snapshot ReportSnapshot) ReportProjectAnalysis {
	items := snapshot.WorkItems
	analysis := ReportProjectAnalysis{
		Summary:         "本周期的项目变化依据 issue 工作事实和里程碑生成；业务收益和外部影响仍需结合实际结果确认。",
		BusinessDomains: make([]ReportBusinessDomain, 0),
		Milestones:      make([]ReportMilestone, 0),
		Changes:         make([]ReportProjectChange, 0, minReportInt(len(items), 20)),
		Risks:           make([]ReportAnalysisNote, 0),
		NextSteps:       make([]ReportAnalysisNote, 0),
		EvidenceIDs:     reportUniqueEvidenceIDs(items),
		Confidence:      "medium",
		Source:          "deterministic",
	}

	type milestoneAccumulator struct {
		milestone    ReportMilestone
		category     string
		summaryParts []string
	}
	domainIndexes := make(map[string]int)
	domainOrder := make([]string, 0)
	milestones := make(map[string]*milestoneAccumulator)
	milestoneOrder := make([]string, 0)
	for _, item := range items {
		domainName := nonEmptyReportText(item.BusinessDomain, reportUnclassifiedBusinessDomain)
		domainIndex, ok := domainIndexes[domainName]
		if !ok {
			domainIndex = len(analysis.BusinessDomains)
			domainIndexes[domainName] = domainIndex
			domainOrder = append(domainOrder, domainName)
			analysis.BusinessDomains = append(analysis.BusinessDomains, ReportBusinessDomain{
				ID:          "domain-" + slugReportID(domainName),
				Name:        domainName,
				Milestones:  make([]ReportMilestone, 0),
				WorkItemIDs: make([]string, 0),
				EvidenceIDs: make([]string, 0),
				Confidence:  "medium",
				Source:      "deterministic",
			})
		}
		domain := &analysis.BusinessDomains[domainIndex]
		domain.WorkItemIDs = appendUniqueReportText(domain.WorkItemIDs, item.ID, 50)
		for _, evidenceID := range item.EvidenceIDs {
			domain.EvidenceIDs = appendUniqueReportText(domain.EvidenceIDs, evidenceID, 20)
		}
		if domain.BusinessImpact == "" || domain.BusinessImpact == "业务影响待确认。" {
			domain.BusinessImpact = nonEmptyReportText(item.BusinessImpact, item.Impact)
		}

		categories := item.Categories
		if len(categories) == 0 {
			categories = []string{item.Category}
		}
		for _, category := range categories {
			category = normalizeReportCategory(category)
			key := domainName + ":" + category
			acc, ok := milestones[key]
			if !ok {
				acc = &milestoneAccumulator{
					milestone: ReportMilestone{
						ID:             "milestone-" + slugReportID(key),
						BusinessDomain: domainName,
						Title:          reportMilestoneLabel(category),
						WorkItemIDs:    make([]string, 0),
						EvidenceIDs:    make([]string, 0),
						Status:         item.Status,
						Confidence:     item.Confidence,
						Source:         "deterministic",
					},
					category:     category,
					summaryParts: make([]string, 0, 4),
				}
				milestones[key] = acc
				milestoneOrder = append(milestoneOrder, key)
			}
			acc.milestone.WorkItemIDs = appendUniqueReportText(acc.milestone.WorkItemIDs, item.ID, 50)
			acc.milestone.Status = aggregateReportStatus(acc.milestone.Status, item.Status)
			for _, evidenceID := range item.EvidenceIDs {
				acc.milestone.EvidenceIDs = appendUniqueReportText(acc.milestone.EvidenceIDs, evidenceID, 20)
			}
			itemSummary := item.Title
			if item.Description != "" {
				itemSummary += "：" + item.Description
			}
			acc.summaryParts = appendUniqueReportText(acc.summaryParts, itemSummary, 4)
		}
	}

	for _, domainName := range domainOrder {
		domainIndex := domainIndexes[domainName]
		domain := &analysis.BusinessDomains[domainIndex]
		for _, key := range milestoneOrder {
			acc := milestones[key]
			if acc == nil || acc.milestone.BusinessDomain != domain.Name {
				continue
			}
			acc.milestone.Summary = strings.Join(acc.summaryParts, "；")
			analysis.Milestones = append(analysis.Milestones, acc.milestone)
			domain.Milestones = append(domain.Milestones, acc.milestone)
			analysis.Changes = append(analysis.Changes, ReportProjectChange{
				ID:          acc.milestone.ID,
				Category:    acc.category,
				Title:       domain.Name + " · " + acc.milestone.Title,
				Description: acc.milestone.Summary,
				Impact:      nonEmptyReportText(domain.BusinessImpact, "业务影响待确认。"),
				Status:      reportStatusOrUnknown(acc.milestone.Status),
				EvidenceIDs: acc.milestone.EvidenceIDs,
				Confidence:  acc.milestone.Confidence,
				Source:      "deterministic",
			})
		}
		domain.Summary = reportDomainSummary(*domain)
	}

	seenRisk := make(map[string]struct{})
	seenNext := make(map[string]struct{})
	for _, item := range items {
		for _, risk := range item.Risks {
			key := item.ID + ":" + risk
			if _, exists := seenRisk[key]; !exists && len(analysis.Risks) < 20 {
				seenRisk[key] = struct{}{}
				analysis.Risks = append(analysis.Risks, ReportAnalysisNote{
					Title:       item.Identifier + " 风险",
					Description: risk,
					EvidenceIDs: item.EvidenceIDs,
					Confidence:  item.Confidence,
					Source:      "deterministic",
				})
			}
		}
		if item.Status == "blocked" || len(item.Dependencies) > 0 {
			key := item.IssueID + ":blocked"
			if _, exists := seenRisk[key]; !exists && len(analysis.Risks) < 20 {
				seenRisk[key] = struct{}{}
				description := "该 issue 当前处于阻塞状态，解除依赖后才能继续推进。"
				if item.Status != "blocked" {
					description = "该工作项存在显式依赖，完成前置条件后才能继续推进。"
				}
				analysis.Risks = append(analysis.Risks, ReportAnalysisNote{
					Title:       item.Identifier + " 存在阻塞",
					Description: description,
					EvidenceIDs: item.EvidenceIDs,
					Confidence:  item.Confidence,
					Source:      "deterministic",
				})
			}
		}
		if item.Status != "done" && item.Status != "cancelled" {
			key := item.IssueID + ":next"
			if _, exists := seenNext[key]; !exists && len(analysis.NextSteps) < 20 {
				seenNext[key] = struct{}{}
				description := "继续推进并在完成后更新 issue 状态。"
				if item.Status == "blocked" {
					description = "先确认依赖和解除阻塞条件，再继续推进。"
				}
				analysis.NextSteps = append(analysis.NextSteps, ReportAnalysisNote{
					Title:       item.Identifier + " 后续推进",
					Description: description,
					EvidenceIDs: item.EvidenceIDs,
					Confidence:  item.Confidence,
					Source:      "deterministic",
				})
			}
		}
	}
	if len(items) == 0 {
		analysis.Summary = "本周期没有可用于项目变化分析的工作项。"
	} else {
		domainNames := make([]string, 0, len(analysis.BusinessDomains))
		milestoneNames := make([]string, 0, minReportInt(len(analysis.Milestones), 5))
		for _, domain := range analysis.BusinessDomains {
			domainNames = append(domainNames, domain.Name)
		}
		for _, milestone := range analysis.Milestones {
			milestoneNames = appendUniqueReportText(milestoneNames, milestone.Title, 5)
		}
		analysis.Summary = fmt.Sprintf("本周期围绕%s推进，形成了%s等里程碑。已确认的变化来自工作事实和验证记录；业务收益待确认。", strings.Join(domainNames, "、"), strings.Join(milestoneNames, "、"))
	}
	return analysis
}

func slugReportID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return "unclassified"
	}
	return truncateReportText(value, 80)
}

func aggregateReportStatus(current, next string) string {
	if current == "blocked" || next == "blocked" {
		return "blocked"
	}
	if current == "in_progress" || next == "in_progress" {
		return "in_progress"
	}
	if current == "in_review" || next == "in_review" {
		return "in_review"
	}
	if current == "todo" || current == "backlog" || next == "todo" || next == "backlog" {
		return "in_progress"
	}
	if current == "cancelled" && next == "cancelled" {
		return "cancelled"
	}
	return "done"
}

func reportDomainSummary(domain ReportBusinessDomain) string {
	if len(domain.Milestones) == 0 {
		return "本周期记录了该业务域的工作事实，具体影响待确认。"
	}
	titles := make([]string, 0, len(domain.Milestones))
	for _, milestone := range domain.Milestones {
		titles = appendUniqueReportText(titles, milestone.Title, 6)
	}
	return fmt.Sprintf("本周期推进%s；业务影响：%s", strings.Join(titles, "、"), nonEmptyReportText(domain.BusinessImpact, "待确认"))
}

type reportPromptSnapshot struct {
	PeriodType       string        `json:"period_type"`
	RangeStart       time.Time     `json:"range_start"`
	RangeEnd         time.Time     `json:"range_end"`
	Timezone         string        `json:"timezone"`
	ActiveIssueCount int           `json:"active_issue_count"`
	Issues           []ReportIssue `json:"issues"`
}

func prepareReportPrompt(snapshot ReportSnapshot) reportPromptSnapshot {
	issues := make([]ReportIssue, 0, len(snapshot.Issues))
	for _, issue := range snapshot.Issues {
		prepared := issue
		prepared.Timeline = cloneAndLimitTimeline(issue.Timeline, &prepared.TimelineTruncated)
		prepared.Title = sanitizeSensitiveText(prepared.Title)
		prepared.Description = sanitizeSensitiveText(prepared.Description)
		prepared.Summary = sanitizeReportIssueSummary(prepared.Summary)
		for index := range prepared.Timeline {
			event := &prepared.Timeline[index]
			event.Content = sanitizeSensitiveText(event.Content)
			event.Action = sanitizeSensitiveText(event.Action)
			event.Details = sanitizeJSONForPrompt(event.Details)
		}
		issues = append(issues, prepared)
	}
	activeCount := snapshot.ActiveIssueCount
	if activeCount == 0 {
		activeCount = len(snapshot.Issues)
	}
	return reportPromptSnapshot{
		PeriodType:       snapshot.PeriodType,
		RangeStart:       snapshot.RangeStart,
		RangeEnd:         snapshot.RangeEnd,
		Timezone:         snapshot.Timezone,
		ActiveIssueCount: activeCount,
		Issues:           issues,
	}
}

func sanitizeReportIssueSummary(summary ReportIssueSummary) ReportIssueSummary {
	summary.Problem = sanitizeSensitiveText(summary.Problem)
	summary.Outcome = sanitizeSensitiveText(summary.Outcome)
	summary.Decision = sanitizeSensitiveText(summary.Decision)
	summary.CurrentState = sanitizeSensitiveText(summary.CurrentState)
	summary.Impact = sanitizeSensitiveText(summary.Impact)
	for index := range summary.Actions {
		summary.Actions[index] = sanitizeSensitiveText(summary.Actions[index])
	}
	for index := range summary.OpenItems {
		summary.OpenItems[index] = sanitizeSensitiveText(summary.OpenItems[index])
	}
	for index := range summary.WorkTypes {
		summary.WorkTypes[index] = sanitizeSensitiveText(summary.WorkTypes[index])
	}
	for index := range summary.WorkDone {
		summary.WorkDone[index] = sanitizeSensitiveText(summary.WorkDone[index])
	}
	for index := range summary.Deliverables {
		summary.Deliverables[index] = sanitizeSensitiveText(summary.Deliverables[index])
	}
	for index := range summary.Verification {
		summary.Verification[index] = sanitizeSensitiveText(summary.Verification[index])
	}
	for index := range summary.Dependencies {
		summary.Dependencies[index] = sanitizeSensitiveText(summary.Dependencies[index])
	}
	for index := range summary.Risks {
		summary.Risks[index] = sanitizeSensitiveText(summary.Risks[index])
	}
	for index := range summary.Artifacts {
		summary.Artifacts[index] = sanitizeSensitiveText(summary.Artifacts[index])
	}
	return summary
}

func sanitizeReportList(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(sanitizeSensitiveText(value))
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func cloneAndLimitTimeline(events []ReportTimelineEvent, truncated *bool) []ReportTimelineEvent {
	if len(events) == 0 {
		return nil
	}
	cloned := reportPromptTimelineEvents(events)
	if len(cloned) == 0 {
		if truncated != nil {
			*truncated = false
		}
		return nil
	}
	for index := range cloned {
		if cloned[index].Details != nil {
			cloned[index].Details = append(json.RawMessage(nil), cloned[index].Details...)
		}
	}
	totalChars := 0
	for _, event := range cloned {
		totalChars += len(event.Content) + len(event.Action) + len(event.Details)
	}
	if len(cloned) <= reportPromptMaxEvents && totalChars <= reportPromptMaxChars {
		return cloned
	}

	selected := make([]ReportTimelineEvent, 0, reportPromptMaxEvents)
	selectedIDs := make(map[string]struct{}, reportPromptMaxEvents)
	selectedChars := 0
	for _, event := range cloned {
		if !event.InRange && len(selected) < 4 {
			selected = append(selected, event)
			selectedIDs[event.ID] = struct{}{}
			selectedChars += eventChars(event)
		}
	}
	for index := len(cloned) - 1; index >= 0 && len(selected) < reportPromptMaxEvents; index-- {
		event := cloned[index]
		if _, exists := selectedIDs[event.ID]; exists {
			continue
		}
		if selectedChars+eventChars(event) > reportPromptMaxChars && len(selected) > 0 {
			continue
		}
		selected = append(selected, event)
		selectedIDs[event.ID] = struct{}{}
		selectedChars += eventChars(event)
	}
	sort.SliceStable(selected, func(left, right int) bool {
		if selected[left].OccurredAt.Equal(selected[right].OccurredAt) {
			return selected[left].ID < selected[right].ID
		}
		return selected[left].OccurredAt.Before(selected[right].OccurredAt)
	})
	selected = fitTimelineToCharacterBudget(selected, reportPromptMaxChars)
	if truncated != nil {
		*truncated = timelineNeedsTruncation(cloned)
	}
	return selected
}

func timelineNeedsTruncation(events []ReportTimelineEvent) bool {
	events = reportPromptTimelineEvents(events)
	if len(events) > reportPromptMaxEvents {
		return true
	}
	totalChars := 0
	for _, event := range events {
		totalChars += eventChars(event)
	}
	return totalChars > reportPromptMaxChars
}

func reportPromptTimelineEvents(events []ReportTimelineEvent) []ReportTimelineEvent {
	filtered := make([]ReportTimelineEvent, 0, len(events))
	for _, event := range events {
		if reportConversationEventContent(event) != "" {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func fitTimelineToCharacterBudget(events []ReportTimelineEvent, budget int) []ReportTimelineEvent {
	if budget <= 0 {
		return nil
	}
	fit := make([]ReportTimelineEvent, 0, len(events))
	remaining := budget
	for _, event := range events {
		if remaining <= 0 {
			break
		}
		if eventChars(event) > remaining {
			event = trimTimelineEvent(event, remaining)
		}
		if eventChars(event) == 0 {
			continue
		}
		fit = append(fit, event)
		remaining -= eventChars(event)
	}
	return fit
}

func trimTimelineEvent(event ReportTimelineEvent, budget int) ReportTimelineEvent {
	// Keep the identifying metadata and spend the prompt budget on human text;
	// a task result is less useful than the comment/status context when a single
	// record is exceptionally large.
	event.Details = nil
	if eventChars(event) <= budget {
		return event
	}
	if len(event.Action) > 0 {
		event.Action = truncateReportText(event.Action, budget)
	}
	if eventChars(event) <= budget {
		return event
	}
	remaining := budget - len(event.Action)
	if remaining < 0 {
		remaining = 0
	}
	event.Content = truncateReportText(event.Content, remaining)
	return event
}

func truncateReportText(value string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(value) <= max {
		return value
	}
	const suffix = "…"
	if max <= len(suffix) {
		return suffix[:max]
	}
	truncated := make([]rune, 0, len(value))
	for _, character := range value {
		candidate := string(append(truncated, character))
		if len(candidate)+len(suffix) > max {
			break
		}
		truncated = append(truncated, character)
	}
	return string(truncated) + suffix
}

func eventChars(event ReportTimelineEvent) int {
	return len(event.Content) + len(event.Action) + len(event.Details)
}

var (
	reportBearerPattern           = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	reportTokenPattern            = regexp.MustCompile(`(?i)\b(?:ghp_[A-Za-z0-9_]+|github_pat_[A-Za-z0-9_]+|sk-[A-Za-z0-9_-]+|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)\b`)
	reportSecretAssignmentPattern = regexp.MustCompile(`(?i)(["']?(?:api[_-]?key|access[_-]?token|refresh[_-]?token|authorization|password|passwd|secret|token)["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^,\s}]+)`)
	reportPrivateKeyPattern       = regexp.MustCompile(`(?s)-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----`)
	reportBusinessPrefixPattern   = regexp.MustCompile(`^\s*(?:【([^】]+)】|\[([^\]]+)\])`)
	reportBusinessLabelPattern    = regexp.MustCompile(`(?i)(?:业务域|业务|business)\s*[:：]\s*([^\n,，;；]+)`)
)

const reportUnclassifiedBusinessDomain = "项目级能力建设"

func inferReportBusinessDomain(title, description string) string {
	for _, value := range []string{title, description} {
		value = strings.TrimSpace(sanitizeSensitiveText(value))
		if value == "" {
			continue
		}
		if matches := reportBusinessPrefixPattern.FindStringSubmatch(value); len(matches) > 0 {
			for index := 1; index < len(matches); index++ {
				if domain := strings.TrimSpace(matches[index]); domain != "" {
					return truncateReportText(domain, 120)
				}
			}
		}
		if matches := reportBusinessLabelPattern.FindStringSubmatch(value); len(matches) == 2 {
			if domain := strings.TrimSpace(matches[1]); domain != "" {
				return truncateReportText(domain, 120)
			}
		}
	}
	return reportUnclassifiedBusinessDomain
}

func sanitizeSensitiveText(value string) string {
	if value == "" {
		return value
	}
	value = reportPrivateKeyPattern.ReplaceAllString(value, "[REDACTED_PRIVATE_KEY]")
	value = reportBearerPattern.ReplaceAllString(value, "Bearer [REDACTED_TOKEN]")
	value = reportTokenPattern.ReplaceAllString(value, "[REDACTED_TOKEN]")
	return reportSecretAssignmentPattern.ReplaceAllString(value, `${1}"[REDACTED_SECRET]"`)
}

func sanitizeJSONForPrompt(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	redacted := sanitizeSensitiveText(string(value))
	if !json.Valid([]byte(redacted)) {
		return json.RawMessage(`{"redacted":true}`)
	}
	return json.RawMessage(redacted)
}

func parseIssueSummaries(raw string, issues []ReportIssue) (map[string]ReportIssueSummary, error) {
	raw = llm.StripJSONFence(raw)
	var envelope struct {
		Summaries json.RawMessage `json:"summaries"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Summaries) == 0 || string(envelope.Summaries) == "null" {
		return nil, fmt.Errorf("summaries array is missing")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(envelope.Summaries, &items); err != nil {
		return nil, err
	}
	expected := make(map[string]struct{}, len(issues))
	issuesByID := make(map[string]ReportIssue, len(issues))
	for _, issue := range issues {
		expected[issue.IssueID] = struct{}{}
		issuesByID[issue.IssueID] = issue
	}
	parsed := make(map[string]ReportIssueSummary, len(items))
	for _, item := range items {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(item, &fields); err != nil {
			continue
		}
		for _, required := range []string{"issue_id", "problem", "actions", "outcome", "open_items"} {
			value, ok := fields[required]
			if !ok || strings.TrimSpace(string(value)) == "null" {
				fields = nil
				break
			}
		}
		if fields == nil {
			continue
		}
		var summary ReportIssueSummary
		if err := json.Unmarshal(item, &summary); err != nil || summary.IssueID == "" {
			continue
		}
		summary.IssueID = strings.TrimSpace(summary.IssueID)
		if _, ok := expected[summary.IssueID]; !ok || summary.IssueID == "" {
			continue
		}
		if _, duplicate := parsed[summary.IssueID]; duplicate {
			continue
		}
		summary.Actions = normalizeReportStringList(summary.Actions, 8, 1000)
		summary.OpenItems = normalizeReportStringList(summary.OpenItems, 8, 1000)
		summary.WorkTypes = normalizeReportStringList(summary.WorkTypes, 8, 100)
		summary.WorkDone = normalizeReportStringList(summary.WorkDone, 8, 1000)
		summary.Deliverables = normalizeReportStringList(summary.Deliverables, 8, 1000)
		summary.Verification = normalizeReportStringList(summary.Verification, 8, 1000)
		summary.Dependencies = normalizeReportStringList(summary.Dependencies, 8, 1000)
		summary.Risks = normalizeReportStringList(summary.Risks, 8, 1000)
		summary.Artifacts = normalizeReportStringList(summary.Artifacts, 8, 1000)
		summary.Problem = strings.TrimSpace(sanitizeSensitiveText(summary.Problem))
		summary.Outcome = strings.TrimSpace(sanitizeSensitiveText(summary.Outcome))
		summary.Decision = strings.TrimSpace(sanitizeSensitiveText(summary.Decision))
		summary.CurrentState = strings.TrimSpace(sanitizeSensitiveText(summary.CurrentState))
		summary.Impact = strings.TrimSpace(sanitizeSensitiveText(summary.Impact))
		if len(summary.Problem) > 2000 || len(summary.Outcome) > 2000 || len(summary.Decision) > 1500 || len(summary.CurrentState) > 500 || len(summary.Impact) > 1500 {
			continue
		}
		issue := issuesByID[summary.IssueID]
		validEvidence := reportEvidenceSetForIssue(issue)
		providedEvidence := len(summary.EvidenceIDs) > 0
		summary.EvidenceIDs = validateReportEvidenceIDs(summary.EvidenceIDs, validEvidence)
		if providedEvidence && len(summary.EvidenceIDs) == 0 && len(validEvidence) > 0 {
			continue
		}
		summary.Confidence = normalizeReportConfidence(summary.Confidence)
		parsed[summary.IssueID] = summary
	}
	if len(parsed) == 0 && len(issues) > 0 {
		return nil, fmt.Errorf("no valid issue summaries")
	}
	return parsed, nil
}

func normalizeReportStringList(values []string, maxItems, maxLength int) []string {
	if len(values) > maxItems {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(sanitizeSensitiveText(value))
		if value == "" {
			continue
		}
		if len(value) > maxLength {
			return nil
		}
		result = append(result, value)
	}
	return result
}

func deterministicIssueSummary(issue ReportIssue) ReportIssueSummary {
	statusLabel := reportStatusLabel(issue.Status)
	if statusLabel == "" {
		statusLabel = issue.Status
	}

	workDone := reportIssueWorkFacts(issue)
	if len(workDone) == 0 {
		workDone = []string{"当前没有可进一步拆分的具体工作内容，需人工确认。"}
	}
	actions := append([]string(nil), workDone...)
	if len(actions) > 8 {
		actions = actions[:8]
	}
	problem := reportIssueProblem(issue, statusLabel, workDone)
	lastFact := workDone[len(workDone)-1]
	outcome := fmt.Sprintf("当前状态：%s。", statusLabel)
	if lastFact != "" {
		outcome += " 最近记录：" + lastFact
	}
	openItems := reportIssueOpenItems(issue)
	decision := reportFirstMatchingFact(issue, []string{"方案", "决定", "确认", "拍板", "采用", "设计"})
	deliverables := reportMatchingFacts(issue, []string{"实现", "新增", "完成", "支持", "产出", "生成", "上线"})
	verification := reportMatchingFacts(issue, []string{"验证", "测试", "实测", "通过", "回归", "成功", "200"})
	dependencies := reportMatchingFacts(issue, []string{"依赖", "等待", "前置", "阻塞", "需要"})
	risks := reportMatchingFacts(issue, []string{"失败", "错误", "超时", "风险", "鉴权", "配置", "不稳定"})
	currentState := statusLabel
	if currentState == "" {
		currentState = issue.Status
	}
	return ReportIssueSummary{
		IssueID:       issue.IssueID,
		Problem:       problem,
		Actions:       actions,
		Outcome:       outcome,
		OpenItems:     openItems,
		WorkTypes:     inferReportCategories(issue),
		WorkDone:      workDone,
		Decision:      decision,
		Deliverables:  deliverables,
		Verification:  verification,
		CurrentState:  currentState,
		Dependencies:  dependencies,
		Risks:         risks,
		Impact:        "业务影响待确认。",
		EvidenceIDs:   reportIssueEvidence(issue),
		Confidence:    reportSummaryConfidence(issue, workDone),
		SummarySource: "deterministic",
	}
}

func reportIssueWorkFacts(issue ReportIssue) []string {
	facts := make([]string, 0, 8)
	for _, event := range issue.Timeline {
		if !event.InRange {
			continue
		}
		if value := reportConversationEventContent(event); value != "" {
			facts = appendUniqueReportText(facts, value, 8)
		}
	}
	return facts
}

func reportIssueProblem(issue ReportIssue, statusLabel string, workDone []string) string {
	if description := strings.TrimSpace(sanitizeSensitiveText(issue.Description)); description != "" {
		return truncateReportText(strings.Join(strings.Fields(description), " "), 800)
	}
	if len(workDone) > 0 && workDone[0] != "" {
		return "本周期围绕「" + strings.TrimSpace(issue.Title) + "」推进：" + workDone[0]
	}
	return fmt.Sprintf("围绕「%s」推进，当前状态为%s。", issue.Title, statusLabel)
}

func reportIssueOpenItems(issue ReportIssue) []string {
	items := make([]string, 0, 3)
	switch issue.Status {
	case "blocked":
		items = append(items, "解除阻塞并确认后续执行条件。")
	case "done", "cancelled":
		// Terminal states have no default follow-up.
	default:
		items = append(items, "继续推进该 issue，并在完成后更新状态。")
	}
	for _, fact := range reportMatchingFacts(issue, []string{"待确认", "待审核", "TODO", "下一步", "需要"}) {
		items = appendUniqueReportText(items, "待确认："+fact, 3)
	}
	return items
}

func reportMatchingFacts(issue ReportIssue, keywords []string) []string {
	result := make([]string, 0, 4)
	for _, fact := range reportIssueWorkFacts(issue) {
		lower := strings.ToLower(fact)
		for _, keyword := range keywords {
			if strings.Contains(lower, strings.ToLower(keyword)) {
				result = appendUniqueReportText(result, fact, 4)
				break
			}
		}
	}
	return result
}

func reportFirstMatchingFact(issue ReportIssue, keywords []string) string {
	values := reportMatchingFacts(issue, keywords)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func appendUniqueReportText(values []string, value string, limit int) []string {
	value = strings.TrimSpace(value)
	if value == "" || len(values) >= limit {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func reportSummaryConfidence(issue ReportIssue, workDone []string) string {
	if len(workDone) == 0 {
		return "low"
	}
	if len(issue.Timeline) > 0 {
		return "medium"
	}
	return "low"
}

func deterministicReportFallback(snapshot ReportSnapshot, reason string, err error) (string, error) {
	if err != nil {
		slog.Warn("project report: using deterministic fallback", "reason", reason, "error", err)
	} else {
		slog.Warn("project report: using deterministic fallback", "reason", reason)
	}
	return buildDeterministicReportContent(snapshot), nil
}

func reportIssuesFromCompletedRows(rows []db.ListIssuesCompletedForReportRow, identifierPrefix string) []ReportIssue {
	issues := make([]ReportIssue, 0, len(rows))
	for _, row := range rows {
		issues = append(issues, reportIssue(identifierPrefix, row.Number, row.Title))
	}
	return issues
}

func reportIssuesFromCancelledRows(rows []db.ListIssuesCancelledForReportRow, identifierPrefix string) []ReportIssue {
	issues := make([]ReportIssue, 0, len(rows))
	for _, row := range rows {
		issues = append(issues, reportIssue(identifierPrefix, row.Number, row.Title))
	}
	return issues
}

func reportIssuesFromOverdueRows(rows []db.ListIssuesOverdueForReportRow, identifierPrefix string) []ReportIssue {
	issues := make([]ReportIssue, 0, len(rows))
	for _, row := range rows {
		issues = append(issues, reportIssue(identifierPrefix, row.Number, row.Title))
	}
	return issues
}

func reportIssuesFromStatusRows(rows []db.ListIssuesByStatusForReportRow, identifierPrefix string) []ReportIssue {
	issues := make([]ReportIssue, 0, len(rows))
	for _, row := range rows {
		issues = append(issues, reportIssue(identifierPrefix, row.Number, row.Title))
	}
	return issues
}

func reportIssue(identifierPrefix string, number int32, title string) ReportIssue {
	return ReportIssue{
		Identifier: fmt.Sprintf("%s-%d", identifierPrefix, number),
		Title:      title,
	}
}

func buildDeterministicReportContent(snapshot ReportSnapshot) string {
	if len(snapshot.Issues) > 0 {
		return buildIssueReportContent(snapshot)
	}
	var content strings.Builder
	content.WriteString("## 报告\n\n")
	content.WriteString("AI 总结暂时不可用，以下内容由系统根据项目数据生成。\n\n")
	appendReportIssueSection(&content, "完成事项", snapshot.Completed)
	appendReportIssueSection(&content, "进行中事项", snapshot.InProgress)
	appendReportIssueSection(&content, "阻塞/风险", snapshot.Blocked)
	appendReportIssueSection(&content, "逾期事项", snapshot.Overdue)
	content.WriteString("## 下一步重点\n\n")
	if len(snapshot.Blocked) > 0 {
		content.WriteString("- 优先处理阻塞/风险事项。\n")
	}
	if len(snapshot.Overdue) > 0 {
		content.WriteString("- 优先处理逾期事项。\n")
	}
	if len(snapshot.InProgress) > 0 {
		content.WriteString("- 持续推进进行中的事项。\n")
	}
	if len(snapshot.Blocked) == 0 && len(snapshot.Overdue) == 0 && len(snapshot.InProgress) == 0 {
		content.WriteString("- 暂无需要跟进的事项。\n")
	}
	return buildReportContent(content.String(), snapshot)
}

func buildIssueReportContent(snapshot ReportSnapshot) string {
	for index := range snapshot.Issues {
		if snapshot.Issues[index].Summary.Problem == "" {
			snapshot.Issues[index].Summary = deterministicIssueSummary(snapshot.Issues[index])
		}
	}
	if len(snapshot.WorkItems) == 0 {
		snapshot.WorkItems = buildReportWorkItems(snapshot)
	}
	if snapshot.ProjectAnalysis.Summary == "" {
		snapshot.ProjectAnalysis = deterministicProjectAnalysis(snapshot)
	}

	var content strings.Builder
	content.WriteString("## 报告\n\n")
	if snapshot.ProjectTitle != "" {
		fmt.Fprintf(&content, "**项目：** %s\n\n", snapshot.ProjectTitle)
	}
	content.WriteString("**报告结论：** ")
	content.WriteString(snapshot.ProjectAnalysis.Summary)
	content.WriteString("\n\n")
	content.WriteString("以下正文按事实、里程碑和领导视图组织；活动数量仅保留在文末指标附录。\n\n")
	content.WriteString("## Issue 工作摘要\n\n")
	for _, issue := range snapshot.Issues {
		fmt.Fprintf(&content, "### %s：%s\n\n", issue.Identifier, issue.Title)
		if issue.BusinessDomain != "" {
			fmt.Fprintf(&content, "- 业务域：%s\n", issue.BusinessDomain)
		}
		fmt.Fprintf(&content, "- 问题：%s\n", issue.Summary.Problem)
		content.WriteString("- 操作：\n")
		for _, action := range issue.Summary.Actions {
			fmt.Fprintf(&content, "  - %s\n", action)
		}
		fmt.Fprintf(&content, "- 结果：%s\n", issue.Summary.Outcome)
		content.WriteString("- 待跟进：\n")
		if len(issue.Summary.OpenItems) == 0 {
			content.WriteString("  - 暂无\n\n")
		} else {
			for _, item := range issue.Summary.OpenItems {
				fmt.Fprintf(&content, "  - %s\n", item)
			}
			content.WriteString("\n")
		}
	}

	content.WriteString("## 按业务域和里程碑\n\n")
	appendReportBusinessDomains(&content, snapshot.ProjectAnalysis.BusinessDomains)

	content.WriteString("## 工作执行版\n\n")
	if len(snapshot.WorkItems) == 0 {
		content.WriteString("- 本周期没有可列出的工作项。\n\n")
	} else {
		for _, item := range snapshot.WorkItems {
			appendReportWorkItem(&content, item, true)
		}
		content.WriteString("\n")
	}

	content.WriteString("## 项目业务推进版\n\n")
	content.WriteString(snapshot.ProjectAnalysis.Summary + "\n\n")
	appendReportBusinessDomains(&content, snapshot.ProjectAnalysis.BusinessDomains)
	if len(snapshot.ProjectAnalysis.Changes) == 0 {
		content.WriteString("- 暂无可确认的项目能力变化。\n\n")
	} else {
		for _, change := range snapshot.ProjectAnalysis.Changes {
			fmt.Fprintf(&content, "- **%s** %s：%s\n", reportCategoryLabel(change.Category), change.Title, change.Description)
			fmt.Fprintf(&content, "  - 状态：%s\n", reportStatusOrUnknown(change.Status))
			fmt.Fprintf(&content, "  - 影响：%s\n", nonEmptyReportText(change.Impact, "业务影响待确认。"))
			if len(change.EvidenceIDs) > 0 {
				fmt.Fprintf(&content, "  - 证据：%s\n", strings.Join(change.EvidenceIDs, ", "))
			} else {
				content.WriteString("  - 证据：待人工确认\n")
			}
		}
		content.WriteString("\n")
	}

	content.WriteString("## 风险与待确认\n\n")
	if len(snapshot.ProjectAnalysis.Risks) == 0 && len(snapshot.ProjectAnalysis.NextSteps) == 0 && len(snapshot.AnalysisWarnings) == 0 {
		content.WriteString("- 暂无额外风险；业务影响仍需结合实际结果确认。\n\n")
	} else {
		for _, note := range snapshot.ProjectAnalysis.Risks {
			fmt.Fprintf(&content, "- 风险：%s——%s\n", note.Title, note.Description)
			appendReportEvidenceLine(&content, note.EvidenceIDs)
		}
		for _, note := range snapshot.ProjectAnalysis.NextSteps {
			fmt.Fprintf(&content, "- 下一步：%s——%s\n", note.Title, note.Description)
			appendReportEvidenceLine(&content, note.EvidenceIDs)
		}
		for _, warning := range snapshot.AnalysisWarnings {
			fmt.Fprintf(&content, "- 待确认：%s\n", warning)
		}
		content.WriteString("\n")
	}

	content.WriteString("## 证据明细\n\n")
	appendReportEvidenceDetails(&content, snapshot)
	if len(snapshot.Issues) == 0 {
		content.WriteString("暂无活跃 issue。\n")
	}
	return buildReportContent(content.String(), snapshot)
}

func appendReportBusinessDomains(content *strings.Builder, domains []ReportBusinessDomain) {
	if len(domains) == 0 {
		content.WriteString("- 暂无已识别的业务域；请人工补充业务归属。\n\n")
		return
	}
	for _, domain := range domains {
		fmt.Fprintf(content, "### %s\n\n", domain.Name)
		if domain.Summary != "" {
			fmt.Fprintf(content, "%s\n\n", domain.Summary)
		}
		if domain.BusinessImpact != "" {
			fmt.Fprintf(content, "- 业务影响：%s\n", domain.BusinessImpact)
		}
		if len(domain.EvidenceIDs) > 0 {
			fmt.Fprintf(content, "- 证据：%s\n", strings.Join(domain.EvidenceIDs, ", "))
		}
		for _, milestone := range domain.Milestones {
			fmt.Fprintf(content, "- **里程碑：%s**（%s）\n", milestone.Title, reportStatusOrUnknown(milestone.Status))
			if milestone.Summary != "" {
				fmt.Fprintf(content, "  - 变化：%s\n", milestone.Summary)
			}
			if len(milestone.WorkItemIDs) > 0 {
				fmt.Fprintf(content, "  - 工作项：%s\n", strings.Join(milestone.WorkItemIDs, ", "))
			}
			if len(milestone.EvidenceIDs) > 0 {
				fmt.Fprintf(content, "  - 证据：%s\n", strings.Join(milestone.EvidenceIDs, ", "))
			}
		}
		content.WriteString("\n")
	}
}

func appendReportWorkItem(content *strings.Builder, item ReportWorkItem, detailed bool) {
	fmt.Fprintf(content, "- **%s** %s：%s\n", reportCategoryLabel(item.Category), item.Identifier, item.Title)
	if item.BusinessDomain != "" {
		fmt.Fprintf(content, "  - 业务域：%s\n", item.BusinessDomain)
	}
	if len(item.Milestones) > 0 {
		fmt.Fprintf(content, "  - 里程碑：%s\n", strings.Join(item.Milestones, "、"))
	}
	if item.Description != "" {
		fmt.Fprintf(content, "  - 实际工作：%s\n", item.Description)
	}
	if detailed && item.Decision != "" {
		fmt.Fprintf(content, "  - 决策：%s\n", item.Decision)
	}
	appendReportList(content, "产出", item.Deliverables)
	appendReportList(content, "验证", item.Verification)
	fmt.Fprintf(content, "  - 结果：%s\n", item.Outcome)
	fmt.Fprintf(content, "  - 当前状态：%s\n", nonEmptyReportText(item.CurrentState, reportStatusOrUnknown(item.Status)))
	appendReportList(content, "依赖", item.Dependencies)
	appendReportList(content, "风险", item.Risks)
	fmt.Fprintf(content, "  - 业务影响：%s\n", nonEmptyReportText(item.BusinessImpact, nonEmptyReportText(item.Impact, "业务影响待确认。")))
	appendReportEvidenceLine(content, item.EvidenceIDs)
}

func appendReportList(content *strings.Builder, label string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(content, "  - %s：%s\n", label, strings.Join(values, "；"))
}

func reportCategoryLabel(category string) string {
	return map[string]string{
		"bug_fix":      "Bug 修复",
		"feature":      "功能/能力",
		"architecture": "架构",
		"design":       "方案设计",
		"research":     "调研分析",
		"operations":   "运维发布",
		"discussion":   "讨论决策",
		"risk":         "风险处理",
		"misc":         "其他",
	}[normalizeReportCategory(category)]
}

func reportStatusOrUnknown(status string) string {
	if label := reportStatusLabel(status); label != "" {
		return label
	}
	if strings.TrimSpace(status) == "" {
		return "待确认"
	}
	return status
}

func nonEmptyReportText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func appendReportEvidenceLine(content *strings.Builder, evidenceIDs []string) {
	if len(evidenceIDs) == 0 {
		content.WriteString("  - 证据：待人工确认\n")
		return
	}
	fmt.Fprintf(content, "  - 证据：%s\n", strings.Join(evidenceIDs, ", "))
}

func appendReportEvidenceDetails(content *strings.Builder, snapshot ReportSnapshot) {
	hasEvidence := false
	evidenceEvents := 0
	truncated := false
	for _, issue := range snapshot.Issues {
		for _, event := range issue.Timeline {
			if !event.InRange || strings.TrimSpace(event.ID) == "" {
				continue
			}
			// The evidence appendix supports the three-layer report too: keep
			// discussion/task facts for human review, but do not reintroduce
			// status churn or generic activity rows that Stage 0 removed from
			// the narrative inputs.
			description := reportConversationEventContent(event)
			if description == "" {
				continue
			}
			if evidenceEvents >= reportContentMaxEvidenceEvents {
				truncated = true
				break
			}
			hasEvidence = true
			evidenceEvents++
			fmt.Fprintf(content, "- `%s` · %s · %s · %s：%s\n", event.ID, issue.Identifier, event.OccurredAt.Format(time.RFC3339), event.Type, description)
		}
		if truncated {
			break
		}
	}
	if !hasEvidence {
		content.WriteString("- 暂无可展示的周期内证据。\n")
	} else if truncated {
		fmt.Fprintf(content, "- 证据条目已限制为最近可展示的 %d 条。\n", reportContentMaxEvidenceEvents)
	}
	content.WriteString("\n")
}

func appendReportIssueSection(content *strings.Builder, title string, issues []ReportIssue) {
	fmt.Fprintf(content, "## %s\n\n", title)
	if len(issues) == 0 {
		content.WriteString("- 暂无\n\n")
		return
	}
	for _, issue := range issues {
		fmt.Fprintf(content, "- %s：%s\n", issue.Identifier, issue.Title)
	}
	content.WriteString("\n")
}

func buildReportContent(modelContent string, snapshot ReportSnapshot) string {
	content := strings.TrimSpace(modelContent)
	if index := strings.LastIndex(content, "## 数据指标"); index >= 0 {
		content = strings.TrimSpace(content[:index])
	}
	if content == "" {
		content = "## 报告\n\n暂无可总结的内容。"
	}
	if len(snapshot.Issues) > 0 {
		counts := map[string]int{}
		for _, issue := range snapshot.Issues {
			counts[issue.Status]++
		}
		// Keep the established metric labels in the markdown export. The
		// issue-centered counters above describe this report window; these
		// labels remain for clients that already parse the saved markdown.
		return capReportContent(fmt.Sprintf(`%s

## 数据指标

- 活跃 issue：%d
- 已完成：%d
- 进行中：%d
- 阻塞：%d
- 取消：%d
- 完成事项：%d
- 进行中事项：%d
- 阻塞/风险：%d
- 逾期事项：%d
- 取消事项：%d`, content, len(snapshot.Issues), counts["done"], counts["in_progress"]+counts["in_review"]+counts["todo"]+counts["backlog"], counts["blocked"], counts["cancelled"], snapshot.CompletedCount, snapshot.InProgressCount, snapshot.BlockedCount, snapshot.OverdueCount, snapshot.CancelledCount))
	}
	return capReportContent(fmt.Sprintf(`%s

## 数据指标

- 完成事项：%d
- 进行中事项：%d
- 阻塞/风险：%d
- 逾期事项：%d
- 取消事项：%d`, content, snapshot.CompletedCount, snapshot.InProgressCount, snapshot.BlockedCount, snapshot.OverdueCount, snapshot.CancelledCount))
}

func capReportContent(content string) string {
	if len(content) <= reportContentMaxBytes {
		return content
	}
	const marker = "\n\n> 证据附录已达到存储上限，完整讨论仍可在 issue 时间线中查看。"
	return truncateReportText(content, reportContentMaxBytes-len(marker)) + marker
}

const reportSystemPrompt = `You are an issue-centered project reporting assistant. Return exactly one JSON object shaped {"summaries":[{"issue_id":"...","problem":"...","actions":["..."],"outcome":"...","open_items":["..."],"work_types":["bug_fix|feature|architecture|design|research|operations|discussion|risk|misc"],"work_done":["..."],"decision":"...","deliverables":["..."],"verification":["..."],"current_state":"...","dependencies":["..."],"risks":["..."],"artifacts":["..."],"impact":"...","evidence_ids":["..."],"confidence":"high|medium|low"}]}.
Do not wrap the JSON in Markdown code fences. Do not add prose before or after the JSON object.
Return at most one summary for each supplied issue_id. Use the exact supplied issue_id; never invent, merge, or omit an issue silently. Every field is required. actions and open_items must be arrays of concise strings.
Summarize only the supplied issue timeline. A timeline entry with in_range=false is historical context and must not be counted as work in the requested period. The optional work fields must describe actual work evidenced by in-range events: decision means a confirmed choice, deliverables means concrete outputs, verification means tests or observed validation, dependencies and risks mean explicit constraints or failures. evidence_ids must contain only IDs from that issue's in-range timeline. When optional work fields cannot be established, return empty arrays or a low confidence instead of guessing.
Do not reveal or repeat tokens, passwords, API keys, authorization headers, private keys, or other secrets even if they appear in the input.
Do not introduce other projects, periods, numbers, status changes, or assumptions. The AI's role is wording only. Write concise, professional Chinese.`

const projectReportSystemPrompt = `You are a project reporting analyst. Return exactly one JSON object shaped {"summary":"...","business_domains":[],"milestones":[],"changes":[],"risks":[],"next_steps":[],"evidence_ids":[],"confidence":"high|medium|low"}.
Each business_domains item must have name, summary, work_item_ids, milestones, business_impact, and evidence_ids. Each milestone must have business_domain, title, summary, work_item_ids, status, and evidence_ids. Each changes item must have category, title, description, impact, status, and evidence_ids. Each risks and next_steps item must have title, description, and evidence_ids. Use only the supplied project, work_items, and their evidence IDs. Every domain, milestone, change, risk, and next step must cite at least one supplied evidence ID and only supplied work_item_ids; if the business impact is not explicit in the evidence, say "业务影响待确认" instead of guessing.
Do not introduce other projects, periods, numbers, statuses, milestones, users, or facts. Do not treat comment or task counts as business value. Do not reveal secrets. Do not wrap the JSON in Markdown fences or add prose. Write concise, professional Chinese.`

const legacyReportSystemPrompt = `You are a project reporting assistant. Return exactly one JSON object shaped {"content":"markdown"}.
Do not wrap the JSON in Markdown code fences. Do not add prose before or after the JSON object.
The markdown content must use these sections in order: 完成事项, 进行中事项, 阻塞/风险, 逾期事项, 下一步重点. Do not write 数据指标; the caller adds that section.
Only cite issues and facts present in the supplied JSON. Never introduce other projects, periods, calculations, numbers, or assumptions.
Preserve issue identifiers exactly. Do not change any counts; use only the supplied *_count fields.
The AI's role is wording only. Write concise, professional Chinese Markdown.`
