CREATE TABLE report_history (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    project_id UUID NOT NULL,
    period_type TEXT NOT NULL CHECK (period_type IN ('daily', 'weekly', 'monthly')),
    range_start TIMESTAMPTZ NOT NULL,
    range_end TIMESTAMPTZ NOT NULL,
    timezone TEXT NOT NULL,
    generated_by_type TEXT NOT NULL CHECK (generated_by_type IN ('member', 'agent')),
    generated_by_id UUID NOT NULL,
    data_snapshot JSONB NOT NULL,
    content TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT report_history_valid_range CHECK (range_start < range_end)
);
