ALTER TABLE report_history ADD COLUMN template_id UUID REFERENCES report_templates(id);
