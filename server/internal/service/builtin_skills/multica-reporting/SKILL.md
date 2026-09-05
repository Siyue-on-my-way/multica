---
name: multica-reporting
description: "Use when generating or reading project reports (daily, weekly, monthly). Covers the multica report CLI commands and report templates."
user-invocable: true
allowed-tools: Bash(multica *)
---

# Multica Reporting

This skill provides instructions for generating and reading project reports using the `multica report` CLI commands.

## Generating a Report

To generate a report, use the `multica report generate` command. This triggers an asynchronous background job that collects evidence and uses an LLM to synthesize the report.

```bash
multica report generate --project <project-id> --since <RFC3339> --until <RFC3339> [--type weekly|daily|monthly] [--template <template-id>]
```

- `--project`: The UUID of the project.
- `--since` and `--until`: The time window for the report (e.g., `2026-08-23T00:00:00Z`).
- `--type`: The type of report (default is `weekly`).
- `--template`: (Optional) The UUID of a custom report template. If omitted, the system default template is used.

The command returns a JSON response containing the `report_id` and `status` (usually `pending`).

## Checking Job Status

To check the status of a report generation job, use the `multica report job` command.

```bash
multica report job <job-id> --project <project-id>
```

The command returns a JSON response containing the `status` (`pending`, `running`, `succeeded`, `failed`). If `succeeded`, it also includes the `report` object with the generated `content`.

## Reading a Saved Report

To read a generated and saved report, use the `multica report get` command.

```bash
multica report get <report-id> --project <project-id>
```

The command returns a JSON response containing the report `content` (Markdown). If the report is still generating, it may return an error or empty content.

## Workflow

1. When a user asks you to generate a report, first determine the `project-id` and the time window (`since` and `until`).
2. Run `multica report generate` with the appropriate parameters.
3. The generation is asynchronous. You can poll the status using `multica report job`, or simply inform the user that the report generation has been started and provide the `report_id`.
4. If the user asks you to summarize a past report, use `multica report get` to read its content and answer their questions based on it.
