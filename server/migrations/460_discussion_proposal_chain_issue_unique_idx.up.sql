CREATE UNIQUE INDEX CONCURRENTLY proposal_generation_issue_unique_idx
    ON proposal_generation_issue (generation_id, issue_id);
