export type ProjectStatus = "planned" | "in_progress" | "paused" | "completed" | "cancelled";

export type ProjectPriority = "urgent" | "high" | "medium" | "low" | "none";

export interface Project {
  id: string;
  workspace_id: string;
  title: string;
  description: string | null;
  icon: string | null;
  status: ProjectStatus;
  priority: ProjectPriority;
  lead_type: "member" | "agent" | null;
  lead_id: string | null;
  // Calendar days ("YYYY-MM-DD"), no time-of-day or timezone — same contract as
  // issue.start_date / issue.due_date.
  start_date: string | null;
  due_date: string | null;
  created_at: string;
  updated_at: string;
  issue_count: number;
  done_count: number;
  resource_count: number;
}

export interface CreateProjectRequest {
  title: string;
  workspace_id?: string;
  description?: string;
  icon?: string;
  status?: ProjectStatus;
  priority?: ProjectPriority;
  lead_type?: "member" | "agent";
  lead_id?: string;
  start_date?: string;
  due_date?: string;
  // Resources to attach in the same transaction as the project. Server returns
  // 4xx (and rolls back) if any one is invalid or duplicate.
  resources?: CreateProjectResourceRequest[];
}

export interface UpdateProjectRequest {
  title?: string;
  description?: string | null;
  icon?: string | null;
  status?: ProjectStatus;
  priority?: ProjectPriority;
  lead_type?: "member" | "agent" | null;
  lead_id?: string | null;
  // Omit the key to leave the date untouched; send null (or "") to clear it.
  start_date?: string | null;
  due_date?: string | null;
}

export interface ListProjectsResponse {
  projects: Project[];
  total: number;
}

// ProjectResource is a typed pointer from a project to an external resource.
// The resource_ref shape depends on resource_type. New types add a case in
// validateAndNormalizeResourceRef on the server and a renderer in the UI.
//
// Known types (UI must default-case unknown server-side additions):
//   - github_repo: cloud-side git checkout, ref = { url, ref?, default_branch_hint? }
//   - local_directory: agent execution on a specific daemon,
//     ref = { local_path, daemon_id, label?, execution_mode? }
export type ProjectResourceType = "github_repo" | "local_directory";

export interface GithubRepoResourceRef {
  url: string;
  ref?: string;
  default_branch_hint?: string;
}

/**
 * How tasks sharing one local directory are executed.
 *
 * - `in_place`: the agent works directly in the user's directory and tasks run
 *   one at a time — a second task waits in `waiting_local_directory`. Edits
 *   land in the user's working copy.
 * - `worktree`: each task gets its own git worktree of that repo inside the
 *   runtime's workspace, so tasks run concurrently and deliver their work as a
 *   branch instead of touching the working copy. Every task of one conversation
 *   shares that branch — `agent/<agent>/<issue>` — so a follow-up continues the
 *   previous turn's work; a task with no conversation behind it gets
 *   `agent/<agent>/<task>`. Continuation is decided by an ownership record in
 *   the repo, not by the branch name, so a same-named branch the user made is
 *   never adopted.
 *
 * Absent means `in_place`: resources created before the mode existed keep their
 * original behavior, so this is optional rather than defaulted on the server.
 */
export type LocalDirectoryExecutionMode = "in_place" | "worktree";

export interface LocalDirectoryResourceRef {
  local_path: string;
  daemon_id: string;
  label?: string;
  execution_mode?: LocalDirectoryExecutionMode;
}

export type ProjectResourceRef =
  | GithubRepoResourceRef
  | LocalDirectoryResourceRef
  | Record<string, unknown>;

export interface ProjectResource {
  id: string;
  project_id: string;
  workspace_id: string;
  resource_type: ProjectResourceType;
  resource_ref: ProjectResourceRef;
  label: string | null;
  position: number;
  created_at: string;
  created_by: string | null;
}

export interface CreateProjectResourceRequest {
  resource_type: ProjectResourceType;
  resource_ref: ProjectResourceRef;
  label?: string;
  position?: number;
}

// resource_type is immutable server-side; partial-update payload mirrors that.
// Sending only the field(s) you want to change is fine — the server merges
// the request body with the existing row, including resource_ref shortcuts.
export interface UpdateProjectResourceRequest {
  resource_ref?: ProjectResourceRef;
  label?: string | null;
  position?: number;
}

export interface ListProjectResourcesResponse {
  resources: ProjectResource[];
  total: number;
}

export type ProjectReportTimelineEventType =
  | "comment"
  | "activity_log"
  | "issue_status_history"
  | "agent_task_queue";

export interface ProjectReportTimelineEvent {
  id: string;
  type: ProjectReportTimelineEventType;
  occurred_at: string;
  in_range: boolean;
  author_type?: "member" | "agent" | "system" | string;
  author_id?: string;
  content?: string;
  comment_type?: string;
  parent_id?: string;
  action?: string;
  details?: Record<string, unknown>;
}

export interface ProjectReportIssueSummary {
  issue_id: string;
  problem: string;
  actions: string[];
  outcome: string;
  open_items: string[];
  work_types?: string[];
  work_done?: string[];
  decision?: string;
  deliverables?: string[];
  verification?: string[];
  current_state?: string;
  dependencies?: string[];
  risks?: string[];
  artifacts?: string[];
  impact?: string;
  evidence_ids?: string[];
  confidence?: "high" | "medium" | "low" | string;
  summary_source?: "ai" | "deterministic" | string;
}

export interface ProjectReportIssue {
  issue_id: string;
  identifier: string;
  title: string;
  description?: string;
  business_domain?: string;
  status: string;
  due_date?: string;
  summary: ProjectReportIssueSummary;
  timeline: ProjectReportTimelineEvent[];
  timeline_truncated?: boolean;
}

export interface ProjectReportWorkItem {
  id: string;
  issue_id: string;
  identifier: string;
  issue_title: string;
  business_domain?: string;
  milestone?: string;
  milestones?: string[];
  category: string;
  categories?: string[];
  title: string;
  description: string;
  work_done?: string[];
  decision?: string;
  deliverables?: string[];
  verification?: string[];
  current_state?: string;
  dependencies?: string[];
  risks?: string[];
  outcome: string;
  impact?: string;
  business_impact?: string;
  status: string;
  evidence_ids?: string[];
  confidence?: "high" | "medium" | "low" | string;
  source?: "ai" | "deterministic" | string;
}

export interface ProjectReportAnalysisNote {
  title: string;
  description: string;
  evidence_ids?: string[];
  confidence?: "high" | "medium" | "low" | string;
  source?: "ai" | "deterministic" | string;
}

export interface ProjectReportChange {
  id: string;
  category: string;
  title: string;
  description: string;
  impact?: string;
  status: string;
  evidence_ids?: string[];
  confidence?: "high" | "medium" | "low" | string;
  source?: "ai" | "deterministic" | string;
}

export interface ProjectReportProjectAnalysis {
  summary: string;
  business_domains?: ProjectReportBusinessDomain[];
  milestones?: ProjectReportMilestone[];
  changes?: ProjectReportChange[];
  risks?: ProjectReportAnalysisNote[];
  next_steps?: ProjectReportAnalysisNote[];
  evidence_ids?: string[];
  confidence?: "high" | "medium" | "low" | string;
  source?: "ai" | "deterministic" | string;
}

export interface ProjectReportNarrative {
  issue_id: string;
  identifier: string;
  title: string;
  business_domain?: string;
  status_from?: string;
  status_to?: string;
  done: string;
  outcome?: string;
  evidence?: string[];
  risks?: string[];
  noteworthy: boolean;
  source: "ai" | "deterministic" | string;
}

export interface ProjectReportMilestone {
  id: string;
  business_domain: string;
  title: string;
  summary: string;
  work_item_ids?: string[];
  status: string;
  evidence_ids?: string[];
  confidence?: "high" | "medium" | "low" | string;
  source?: "ai" | "deterministic" | string;
}

export interface ProjectReportBusinessDomain {
  id: string;
  name: string;
  summary: string;
  work_item_ids?: string[];
  milestones?: ProjectReportMilestone[];
  business_impact?: string;
  evidence_ids?: string[];
  confidence?: "high" | "medium" | "low" | string;
  source?: "ai" | "deterministic" | string;
}

export interface ProjectReportSnapshot {
  period_type: ProjectReportPeriod;
  range_start: string;
  range_end: string;
  timezone: string;
  project_title?: string;
  project_description?: string;
  generated_at: string;
  summary_version: number;
  analysis_version?: number;
  issues: ProjectReportIssue[];
  active_issue_count: number;
  work_items?: ProjectReportWorkItem[];
  project_analysis?: ProjectReportProjectAnalysis;
  analysis_warnings?: string[];
  narrative_version?: number;
  narratives?: ProjectReportNarrative[];
  executive_summary?: string;
  completed?: ProjectReportIssue[];
  in_progress?: ProjectReportIssue[];
  blocked?: ProjectReportIssue[];
  overdue?: ProjectReportIssue[];
  cancelled?: ProjectReportIssue[];
  completed_count?: number;
  in_progress_count?: number;
  blocked_count?: number;
  overdue_count?: number;
  cancelled_count?: number;
}

export interface ProjectReport {
  id: string;
  workspace_id: string;
  project_id: string;
  period_type: ProjectReportPeriod;
  range_start: string;
  range_end: string;
  timezone: string;
  generated_by_type: "member" | "agent";
  generated_by_id: string;
  data_snapshot?: ProjectReportSnapshot | Record<string, unknown>;
  content: string;
  created_at: string;
  saved_at?: string | null;
}

export type ProjectReportPeriod = "daily" | "weekly" | "monthly";

export interface ProjectReportTemplate {
  id: string;
  workspace_id?: string | null;
  name: string;
  period_type: ProjectReportPeriod;
  system_prompt: string;
  created_at: string;
  updated_at: string;
}

export interface ProjectReportHistoryItem {
  id: string;
  workspace_id: string;
  project_id: string;
  period_type: ProjectReportPeriod;
  range_start: string;
  range_end: string;
  timezone: string;
  generated_by_type: "member" | "agent";
  generated_by_id: string;
  created_at: string;
  saved_at: string;
}

export interface ListProjectReportsResponse {
  reports: ProjectReportHistoryItem[];
  total: number;
}

export interface CreateProjectReportRequest {
  template_id?: string;
  period_type: ProjectReportPeriod;
  range_start: string;
  range_end: string;
  timezone: string;
}

export interface ProjectReportJob {
  job_id: string;
  report_id: string;
  status: "pending" | "running" | "succeeded" | "failed";
  attempt: number;
  max_attempts: number;
  error_message?: string | null;
  created_at: string;
  report?: ProjectReport;
}
