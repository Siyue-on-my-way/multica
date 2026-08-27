CREATE TABLE provider_configs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    provider_type VARCHAR(50) NOT NULL,
    base_url VARCHAR(1024),
    api_key VARCHAR(1024),
    model VARCHAR(255),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_provider_configs_workspace_id ON provider_configs(workspace_id);

-- Add a column to agent_runtime to link to the active provider config
ALTER TABLE agent_runtime ADD COLUMN active_provider_config_id UUID REFERENCES provider_configs(id) ON DELETE SET NULL;
