CREATE UNIQUE INDEX CONCURRENTLY discussion_proposal_source_comment_idx
    ON discussion_proposal (workspace_id, source_issue_id, question_comment_id)
    WHERE question_comment_id IS NOT NULL;
