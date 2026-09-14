CREATE UNIQUE INDEX CONCURRENTLY proposal_generation_idempotency_idx
    ON proposal_generation (workspace_id, proposal_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
