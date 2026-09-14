-- name: CreateDiscussionProposal :one
INSERT INTO discussion_proposal (
    id, workspace_id, source_issue_id, question_comment_id,
    answer_comment_id, snapshot
) VALUES (
    sqlc.arg(id), sqlc.arg(workspace_id), sqlc.arg(source_issue_id),
    sqlc.narg(question_comment_id), sqlc.narg(answer_comment_id),
    sqlc.arg(snapshot)
)
RETURNING *;

-- name: GetDiscussionProposal :one
SELECT * FROM discussion_proposal
WHERE workspace_id = sqlc.arg(workspace_id)
  AND id = sqlc.arg(id);

-- name: GetDiscussionProposalByQuestion :one
SELECT * FROM discussion_proposal
WHERE workspace_id = sqlc.arg(workspace_id)
  AND source_issue_id = sqlc.arg(source_issue_id)
  AND question_comment_id = sqlc.arg(question_comment_id)
LIMIT 1;

-- name: ListDiscussionProposalsForComments :many
SELECT * FROM discussion_proposal
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (question_comment_id = ANY(sqlc.arg(comment_ids)::uuid[])
       OR answer_comment_id = ANY(sqlc.arg(comment_ids)::uuid[]));

-- name: ListDiscussionProposalsForIssue :many
SELECT * FROM discussion_proposal
WHERE workspace_id = sqlc.arg(workspace_id)
  AND source_issue_id = sqlc.arg(source_issue_id)
ORDER BY created_at DESC, id DESC;

-- name: CreateProposalGeneration :one
INSERT INTO proposal_generation (
    id, workspace_id, proposal_id, strategy, create_mode, idempotency_key, created_by, snapshot
) VALUES (
    sqlc.arg(id), sqlc.arg(workspace_id), sqlc.arg(proposal_id),
    sqlc.arg(strategy), sqlc.arg(create_mode), sqlc.narg(idempotency_key),
    sqlc.arg(created_by), sqlc.arg(snapshot)
)
RETURNING *;

-- name: GetProposalGeneration :one
SELECT * FROM proposal_generation
WHERE workspace_id = sqlc.arg(workspace_id)
  AND id = sqlc.arg(id);

-- name: GetProposalGenerationByIdempotency :one
SELECT * FROM proposal_generation
WHERE workspace_id = sqlc.arg(workspace_id)
  AND proposal_id = sqlc.arg(proposal_id)
  AND idempotency_key = sqlc.arg(idempotency_key);

-- name: ListProposalGenerations :many
SELECT * FROM proposal_generation
WHERE workspace_id = sqlc.arg(workspace_id)
  AND proposal_id = sqlc.arg(proposal_id)
ORDER BY created_at DESC, id DESC;

-- name: CreateProposalGenerationIssue :one
INSERT INTO proposal_generation_issue (
    id, workspace_id, generation_id, proposal_id, issue_id, item_id, stage
) VALUES (
    sqlc.arg(id), sqlc.arg(workspace_id), sqlc.arg(generation_id),
    sqlc.arg(proposal_id), sqlc.arg(issue_id), sqlc.arg(item_id),
    sqlc.narg(stage)
)
RETURNING *;

-- name: ListProposalGenerationIssues :many
SELECT * FROM proposal_generation_issue
WHERE workspace_id = sqlc.arg(workspace_id)
  AND generation_id = sqlc.arg(generation_id)
ORDER BY created_at, id;

-- name: ListProposalGenerationIssuesForProposal :many
SELECT * FROM proposal_generation_issue
WHERE workspace_id = sqlc.arg(workspace_id)
  AND proposal_id = sqlc.arg(proposal_id)
ORDER BY created_at, id;
