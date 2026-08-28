-- Actor values are supplied per update through transaction-local settings.
-- This keeps the trigger generic while preserving the caller's actor identity.
CREATE FUNCTION set_issue_status_history_actor() RETURNS trigger AS $$
BEGIN
    IF NEW.status IS DISTINCT FROM OLD.status THEN
        INSERT INTO issue_status_history (
            issue_id,
            workspace_id,
            from_status,
            to_status,
            changed_by_type,
            changed_by_id
        )
        VALUES (
            NEW.id,
            NEW.workspace_id,
            OLD.status,
            NEW.status,
            COALESCE(NULLIF(current_setting('multica.issue_status_actor_type', true), ''), 'system'),
            NULLIF(current_setting('multica.issue_status_actor_id', true), '')::uuid
        );
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER issue_status_history_write
BEFORE UPDATE ON issue
FOR EACH ROW EXECUTE FUNCTION set_issue_status_history_actor();
