// SIY-125: context observation. Mirrors the server's taskContextResponse wire
// shape (server/internal/handler/task_context.go). Field names are snake_case
// to match the rest of the task API surface (see AgentTask), and every field
// degrades to a safe default so a partially-upgraded server cannot break the
// execution log — the matching zod schema (TaskContextSchema) enforces that.

export interface TaskContextSection {
  key: string;
  source: string;
  delivery: string;
  injected: boolean;
  bytes: number;
  token_count: number;
  truncated: boolean;
  digest: string;
  preview: string;
  raw?: string;
}

export interface TaskContext {
  task_id: string;
  provider?: string;
  runtime_id?: string;
  session_reused: boolean;
  resume_expected: boolean;
  /** resumed | fresh | fallback | unknown */
  resume_actual: string;
  fallback_reason?: string;
  workdir_reused: boolean;
  prompt_bytes: number;
  input_tokens: number;
  /** exact | estimated */
  token_mode: string;
  sections: TaskContextSection[];
  session_id?: string;
  observed_at?: string;
  completed_at?: string | null;
}

/** Badge state derived from the observation record. */
export type ContextBadgeState = "resumed" | "fresh" | "fallback" | "unknown";

export function contextBadgeState(ctx: TaskContext | undefined): ContextBadgeState {
  if (!ctx) return "unknown";
  switch (ctx.resume_actual) {
    case "resumed":
      return "resumed";
    case "fallback":
      return "fallback";
    case "fresh":
      return "fresh";
    default:
      return "unknown";
  }
}
