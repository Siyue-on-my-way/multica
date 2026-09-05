package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// MoveIssueWorkspaceRequest describes the destination of a whole issue tree.
type MoveIssueWorkspaceRequest struct {
	TargetWorkspaceID string               `json:"target_workspace_id"`
	TargetProjectID   *string              `json:"target_project_id,omitempty"`
	NewProject        *MoveIssueNewProject `json:"new_project,omitempty"`
}

type MoveIssueNewProject struct {
	Title       string  `json:"title"`
	Description *string `json:"description,omitempty"`
	Icon        *string `json:"icon,omitempty"`
	Status      string  `json:"status,omitempty"`
	Priority    string  `json:"priority,omitempty"`
}

// MoveIssueToWorkspace moves the selected issue's root tree atomically. The
// issue IDs and parent links are retained; workspace-scoped projections follow
// the issues so comments, reactions, attachments and activity remain visible.
func (h *Handler) MoveIssueToWorkspace(w http.ResponseWriter, r *http.Request) {
	var req MoveIssueWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.TargetWorkspaceID) == "" {
		writeError(w, http.StatusBadRequest, "target_workspace_id is required")
		return
	}
	if (req.TargetProjectID == nil) == (req.NewProject == nil) {
		writeError(w, http.StatusBadRequest, "exactly one of target_project_id or new_project is required")
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "issue id")
	if !ok {
		return
	}
	sourceID := h.resolveWorkspaceID(r)
	sourceWS, ok := parseUUIDOrBadRequest(w, sourceID, "workspace id")
	if !ok {
		return
	}
	targetWS, ok := parseUUIDOrBadRequest(w, req.TargetWorkspaceID, "target workspace id")
	if !ok {
		return
	}
	if sourceWS == targetWS {
		writeError(w, http.StatusBadRequest, "target workspace must differ")
		return
	}
	// The route middleware normally establishes source membership, but keep the
	// handler safe when invoked without that middleware (and make the read
	// authorization explicit at this boundary).
	if _, ok := h.workspaceMember(w, r, sourceID); !ok {
		return
	}
	if _, ok := h.requireWorkspaceRole(w, r, req.TargetWorkspaceID, "target workspace not found", "owner", "admin"); !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start move transaction")
		return
	}
	defer tx.Rollback(r.Context())
	ctx := r.Context()
	qtx := h.Queries.WithTx(tx)

	var sourceIssue db.Issue
	sourceIssue, err = qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: sourceWS})
	if err != nil {
		writeError(w, http.StatusNotFound, "issue not found")
		return
	}

	var targetProject pgtype.UUID
	if req.TargetProjectID != nil {
		targetProject, err = util.ParseUUID(*req.TargetProjectID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid target_project_id")
			return
		}
		if _, err = qtx.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: targetProject, WorkspaceID: targetWS}); err != nil {
			writeError(w, http.StatusConflict, "target project does not belong to target workspace")
			return
		}
	} else {
		p := req.NewProject
		if p == nil || strings.TrimSpace(p.Title) == "" {
			writeError(w, http.StatusBadRequest, "new_project.title is required")
			return
		}
		status, priority := p.Status, p.Priority
		if status == "" {
			status = "planned"
		}
		if priority == "" {
			priority = "none"
		}
		if !validateProjectEnum(w, "status", status, validProjectStatuses) || !validateProjectEnum(w, "priority", priority, validProjectPriorities) {
			return
		}
		project, createErr := qtx.CreateProject(ctx, db.CreateProjectParams{WorkspaceID: targetWS, Title: strings.TrimSpace(p.Title), Description: ptrToText(p.Description), Icon: ptrToText(p.Icon), Status: status, Priority: priority})
		if createErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to create target project")
			return
		}
		targetProject = project.ID
	}

	// Lock both workspace counters before reserving new per-workspace numbers.
	var counter int32
	if err = tx.QueryRow(ctx, `SELECT issue_counter FROM workspace WHERE id = $1 FOR UPDATE`, targetWS).Scan(&counter); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock target workspace")
		return
	}
	if err = tx.QueryRow(ctx, `SELECT GREATEST($1::bigint, COALESCE(MAX(number), 0))::bigint FROM issue WHERE workspace_id = $2`, counter, targetWS).Scan(&counter); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reserve target issue numbers")
		return
	}
	if _, err = tx.Exec(ctx, `CREATE TEMP TABLE issue_workspace_move_ids (id UUID PRIMARY KEY) ON COMMIT DROP`); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to prepare issue tree move")
		return
	}

	// Validate the full parent chain before moving anything. The path guard
	// prevents malformed cycles from making the recursive query unbounded.
	var ancestorRoots, ancestorCycles, ancestorMismatches, missingParents int32
	if err = tx.QueryRow(ctx, `
		WITH RECURSIVE ancestors AS (
			SELECT i.id, i.parent_issue_id, i.workspace_id, i.project_id,
			       ARRAY[i.id]::uuid[] AS path, false AS cycle
			FROM issue i WHERE i.id = $1
			UNION ALL
			SELECT p.id, p.parent_issue_id, p.workspace_id, p.project_id,
			       a.path || p.id, p.id = ANY(a.path)
			FROM issue p JOIN ancestors a ON p.id = a.parent_issue_id
			WHERE NOT a.cycle
		)
		SELECT count(*) FILTER (WHERE parent_issue_id IS NULL)::int,
		       count(*) FILTER (WHERE cycle)::int,
		       count(*) FILTER (WHERE workspace_id IS DISTINCT FROM $2 OR project_id IS DISTINCT FROM $3)::int,
		       count(*) FILTER (WHERE parent_issue_id IS NOT NULL AND NOT EXISTS (
			       SELECT 1 FROM issue p WHERE p.id = ancestors.parent_issue_id
		       ))::int
		FROM ancestors`, id, sourceWS, sourceIssue.ProjectID).Scan(&ancestorRoots, &ancestorCycles, &ancestorMismatches, &missingParents); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to validate issue parent chain")
		return
	}
	if ancestorRoots != 1 || ancestorCycles != 0 || ancestorMismatches != 0 || missingParents != 0 {
		writeError(w, http.StatusConflict, "issue parent chain contains inconsistent data")
		return
	}

	var rootID pgtype.UUID
	if err = tx.QueryRow(ctx, `
		WITH RECURSIVE ancestors AS (
			SELECT i.id, i.parent_issue_id, ARRAY[i.id]::uuid[] AS path, false AS cycle
			FROM issue i WHERE i.id = $1
			UNION ALL
			SELECT p.id, p.parent_issue_id, a.path || p.id, p.id = ANY(a.path)
			FROM issue p JOIN ancestors a ON p.id = a.parent_issue_id
			WHERE NOT a.cycle
		)
		SELECT id FROM ancestors WHERE parent_issue_id IS NULL`, id).Scan(&rootID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to locate issue tree root")
		return
	}

	// Traverse all descendants, including inconsistent rows, so cross-tenant
	// data is rejected instead of silently omitted from the move.
	var treeCycles, treeMismatches int32
	if err = tx.QueryRow(ctx, `
		WITH RECURSIVE tree AS (
			SELECT i.id, i.workspace_id, i.project_id, ARRAY[i.id]::uuid[] AS path, false AS cycle
			FROM issue i WHERE i.id = $1
			UNION ALL
			SELECT c.id, c.workspace_id, c.project_id, t.path || c.id, c.id = ANY(t.path)
			FROM issue c JOIN tree t ON c.parent_issue_id = t.id
			WHERE NOT t.cycle
		)
		SELECT count(*) FILTER (WHERE cycle)::int,
		       count(*) FILTER (WHERE workspace_id IS DISTINCT FROM $2 OR project_id IS DISTINCT FROM $3)::int
		FROM tree`, rootID, sourceWS, sourceIssue.ProjectID).Scan(&treeCycles, &treeMismatches); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to validate issue descendants")
		return
	}
	if treeCycles != 0 || treeMismatches != 0 {
		writeError(w, http.StatusConflict, "issue tree contains inconsistent data")
		return
	}
	if _, err = tx.Exec(ctx, `
		WITH RECURSIVE tree AS (
			SELECT i.id, ARRAY[i.id]::uuid[] AS path, false AS cycle
			FROM issue i WHERE i.id = $1
			UNION ALL
			SELECT c.id, t.path || c.id, c.id = ANY(t.path)
			FROM issue c JOIN tree t ON c.parent_issue_id = t.id
			WHERE NOT t.cycle
		)
		INSERT INTO issue_workspace_move_ids SELECT DISTINCT id FROM tree WHERE NOT cycle`, rootID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to collect issue tree")
		return
	}
	var treeCount int32
	if err = tx.QueryRow(ctx, `SELECT count(*)::int FROM issue_workspace_move_ids`).Scan(&treeCount); err != nil || treeCount == 0 {
		writeError(w, http.StatusNotFound, "issue tree not found")
		return
	}
	if treeCount > 2147483647-counter {
		writeError(w, http.StatusConflict, "target workspace issue counter would overflow")
		return
	}

	// Labels and custom properties are workspace catalogs. Copy only the
	// definitions referenced by this tree and remap their IDs so the moved issue
	// keeps the same visible metadata without mutating source-workspace data.
	if _, err = tx.Exec(ctx, `CREATE TEMP TABLE issue_workspace_move_label_map (source_id UUID PRIMARY KEY, target_id UUID NOT NULL) ON COMMIT DROP`); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to prepare issue label migration")
		return
	}
	if _, err = tx.Exec(ctx, `CREATE TEMP TABLE issue_workspace_move_property_map (source_id UUID PRIMARY KEY, target_id UUID NOT NULL) ON COMMIT DROP`); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to prepare issue property migration")
		return
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO issue_label (workspace_id, resource_type, name, description, color, created_at, updated_at)
		SELECT $1, l.resource_type, l.name, l.description, l.color, l.created_at, l.updated_at
		FROM issue_label l
		WHERE l.workspace_id = $2
		  AND EXISTS (SELECT 1 FROM issue_to_label il JOIN issue_workspace_move_ids m ON m.id = il.issue_id WHERE il.label_id = l.id)
		ON CONFLICT DO NOTHING`, targetWS, sourceWS); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to copy issue labels")
		return
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO issue_workspace_move_label_map (source_id, target_id)
		SELECT source.id, target.id
		FROM issue_label source JOIN issue_label target
		  ON target.workspace_id = $1
		 AND target.resource_type = source.resource_type
		 AND lower(target.name) = lower(source.name)
		WHERE source.workspace_id = $2
		  AND EXISTS (SELECT 1 FROM issue_to_label il JOIN issue_workspace_move_ids m ON m.id = il.issue_id WHERE il.label_id = source.id)`, targetWS, sourceWS); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to map issue labels")
		return
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO issue_property (workspace_id, name, type, description, icon, config, position, archived_at, created_at, updated_at)
		SELECT $1, p.name, p.type, p.description, p.icon, p.config, p.position, p.archived_at, p.created_at, p.updated_at
		FROM issue_property p
		WHERE p.workspace_id = $2
		  AND EXISTS (SELECT 1 FROM issue_workspace_move_ids m JOIN issue i ON i.id = m.id WHERE i.properties ? p.id::text)
		ON CONFLICT DO NOTHING`, targetWS, sourceWS); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to copy issue properties")
		return
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO issue_workspace_move_property_map (source_id, target_id)
		SELECT source.id, target.id
		FROM issue_property source JOIN issue_property target
		  ON target.workspace_id = $1 AND lower(target.name) = lower(source.name)
		WHERE source.workspace_id = $2
		  AND EXISTS (SELECT 1 FROM issue_workspace_move_ids m JOIN issue i ON i.id = m.id WHERE i.properties ? source.id::text)`, targetWS, sourceWS); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to map issue properties")
		return
	}

	if _, err = tx.Exec(ctx, `
		UPDATE issue_to_label il SET label_id = lm.target_id
		FROM issue_workspace_move_label_map lm
		WHERE il.label_id = lm.source_id
		  AND il.issue_id IN (SELECT id FROM issue_workspace_move_ids)`); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remap issue labels")
		return
	}
	if _, err = tx.Exec(ctx, `
		UPDATE issue i SET properties = mapped.properties
		FROM (
			SELECT i2.id, jsonb_object_agg(COALESCE(pm.target_id::text, entry.key), entry.value) AS properties
			FROM issue i2 JOIN issue_workspace_move_ids m ON m.id = i2.id
			CROSS JOIN LATERAL jsonb_each(i2.properties) AS entry(key, value)
			LEFT JOIN issue_workspace_move_property_map pm ON pm.source_id::text = entry.key
			GROUP BY i2.id
		) mapped
		WHERE i.id = mapped.id`); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remap issue properties")
		return
	}

	// Reassign numbers while the destination counter is locked. Existing IDs,
	// parent_issue_id and all other issue fields remain unchanged.
	if _, err = tx.Exec(ctx, `
		WITH numbered AS (
			SELECT i.id, ($1 + ROW_NUMBER() OVER (ORDER BY i.created_at, i.id))::int AS next_number
			FROM issue i JOIN issue_workspace_move_ids m ON m.id = i.id
		)
		UPDATE issue i SET workspace_id = $2, project_id = $3, number = n.next_number, updated_at = now()
		FROM numbered n WHERE i.id = n.id`, counter, targetWS, targetProject); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to move issue tree")
		return
	}
	if _, err = tx.Exec(ctx, `
		UPDATE workspace w
		SET issue_counter = GREATEST(
			w.issue_counter,
			COALESCE((SELECT MAX(i.number) FROM issue i WHERE i.workspace_id = w.id), 0)
		)
		WHERE w.id = $1`, targetWS); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update target issue counter")
		return
	}
	// Keep workspace-scoped issue projections in sync with the moved rows.
	for _, statement := range []string{
		`UPDATE comment SET workspace_id = $1 WHERE workspace_id = $2 AND issue_id IN (SELECT id FROM issue_workspace_move_ids)`,
		`UPDATE comment_reaction SET workspace_id = $1 WHERE workspace_id = $2 AND comment_id IN (SELECT c.id FROM comment c JOIN issue_workspace_move_ids m ON m.id = c.issue_id)`,
		`UPDATE issue_reaction SET workspace_id = $1 WHERE workspace_id = $2 AND issue_id IN (SELECT id FROM issue_workspace_move_ids)`,
		`UPDATE attachment SET workspace_id = $1 WHERE workspace_id = $2 AND (issue_id IN (SELECT id FROM issue_workspace_move_ids) OR comment_id IN (SELECT c.id FROM comment c JOIN issue_workspace_move_ids m ON m.id = c.issue_id))`,
		`UPDATE activity_log SET workspace_id = $1 WHERE workspace_id = $2 AND issue_id IN (SELECT id FROM issue_workspace_move_ids)`,
		`UPDATE inbox_item SET workspace_id = $1 WHERE workspace_id = $2 AND issue_id IN (SELECT id FROM issue_workspace_move_ids)`,
		`UPDATE pinned_item SET workspace_id = $1 WHERE workspace_id = $2 AND item_type = 'issue' AND item_id IN (SELECT id FROM issue_workspace_move_ids)`,
		`UPDATE issue_status_history SET workspace_id = $1 WHERE workspace_id = $2 AND issue_id IN (SELECT id FROM issue_workspace_move_ids)`,
	} {
		if _, err = tx.Exec(ctx, statement, targetWS, sourceWS); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to move issue-associated data")
			return
		}
	}
	moved, err := qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: sourceIssue.ID, WorkspaceID: targetWS})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load moved issue")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit issue move")
		return
	}
	userID := requestUserID(r)
	h.publish(protocol.EventIssueDeleted, sourceID, "member", userID, map[string]any{"issue_id": uuidToString(sourceIssue.ID)})
	h.publish(protocol.EventIssueUpdated, req.TargetWorkspaceID, "member", userID, map[string]any{"issue": issueToResponse(moved, h.getIssuePrefix(ctx, targetWS))})
	writeJSON(w, http.StatusOK, issueToResponse(moved, h.getIssuePrefix(ctx, targetWS)))
}
