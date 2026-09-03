#!/usr/bin/env bash
set -euo pipefail

# Diagnose the issue tree for a project. The query follows parent_issue_id from
# the requested root, then reports only descendants belonging to the project.
# Usage: scripts/check-issue-ancestry.sh [project_id] [root_issue_id]

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROJECT_ID="${1:-e4a83175-1edb-47c6-9189-28b78826b824}"
ROOT_ISSUE_ID="${2:-79f4d8ef-62a7-4cdc-837b-ecbd6c08fa73}"

cd "$REPO_ROOT/docker"

if ! docker-compose ps --status running --services | grep -qx 'multica-postgres'; then
  echo "PostgreSQL container multica-postgres is not running." >&2
  echo "Start the service first with: $REPO_ROOT/restart.sh --restart-only" >&2
  exit 1
fi

echo "Project: $PROJECT_ID"
echo "Root issue: $ROOT_ISSUE_ID"
echo
echo "Issue ancestry (depth 0 = root; rows marked MATCH are in the project):"
echo

docker-compose exec -T multica-postgres psql -X -v ON_ERROR_STOP=1 \
  -U "${POSTGRES_USER:-multica}" -d "${POSTGRES_DB:-multica}" \
  -v project_id="$PROJECT_ID" -v root_issue_id="$ROOT_ISSUE_ID" \
  --pset=footer=off --pset=border=2 --pset=linestyle=unicode <<'SQL'
CREATE TEMP TABLE issue_tree AS
WITH RECURSIVE tree AS (
  SELECT
    i.id,
    i.parent_issue_id,
    i.workspace_id,
    i.project_id,
    i.number,
    i.title,
    i.status,
    i.priority,
    i.assignee_type,
    i.assignee_id,
    i.created_at,
    0 AS depth,
    ARRAY[i.id] AS visited,
    ARRAY[i.number::text] AS number_path
  FROM issue i
  WHERE i.id = :'root_issue_id'::uuid

  UNION ALL

  SELECT
    child.id,
    child.parent_issue_id,
    child.workspace_id,
    child.project_id,
    child.number,
    child.title,
    child.status,
    child.priority,
    child.assignee_type,
    child.assignee_id,
    child.created_at,
    tree.depth + 1,
    tree.visited || child.id,
    tree.number_path || child.number::text
  FROM tree
  JOIN issue child ON child.parent_issue_id = tree.id
  WHERE NOT child.id = ANY(tree.visited)
)
SELECT * FROM tree;

SELECT
  CASE WHEN tree.project_id = :'project_id'::uuid THEN 'MATCH' ELSE 'OTHER PROJECT' END AS project_check,
  tree.depth,
  repeat('  ', tree.depth) || COALESCE(ws.issue_prefix, '?') || '-' || tree.number AS identifier,
  tree.id,
  tree.parent_issue_id,
  tree.project_id,
  tree.workspace_id,
  tree.status,
  tree.priority,
  COALESCE(tree.assignee_type || ':' || tree.assignee_id::text, '-') AS assignee,
  to_char(tree.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS UTC') AS created_at,
  tree.title,
  array_to_string(tree.number_path, ' > ') AS ancestry_numbers
FROM issue_tree tree
LEFT JOIN workspace ws ON ws.id = tree.workspace_id
ORDER BY tree.number_path;

SELECT
  'SUMMARY' AS section,
  COUNT(*) FILTER (WHERE tree.project_id = :'project_id'::uuid) AS matching_project_issues,
  COUNT(*) FILTER (WHERE tree.depth > 0 AND tree.project_id = :'project_id'::uuid) AS matching_descendants,
  COUNT(*) FILTER (WHERE tree.depth > 0) AS all_descendants,
  COUNT(*) FILTER (WHERE tree.depth > 0 AND tree.workspace_id <> root.workspace_id) AS cross_workspace_descendants,
  COUNT(*) FILTER (WHERE tree.depth > 0 AND tree.project_id IS DISTINCT FROM :'project_id'::uuid) AS descendants_in_other_projects
FROM issue_tree tree
CROSS JOIN issue root
WHERE root.id = :'root_issue_id'::uuid;

SELECT
  'PROJECT_ISSUES_WITHOUT_ROOT_ANCESTRY' AS section,
  COUNT(*) AS count
FROM issue project_issue
WHERE project_issue.project_id = :'project_id'::uuid
  AND NOT EXISTS (SELECT 1 FROM issue_tree tree WHERE tree.id = project_issue.id);
SQL
SQL_STATUS=$?

if [ "$SQL_STATUS" -ne 0 ]; then
  echo "The ancestry query failed." >&2
  exit "$SQL_STATUS"
fi
