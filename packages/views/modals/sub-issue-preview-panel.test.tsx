import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SubIssuePreviewModal } from "./sub-issue-preview-panel";
import { I18nProvider } from "@multica/core/i18n/react";
import enModals from "../locales/en/modals.json";

const mockSuggestPlans = vi.hoisted(() => vi.fn());
const mockGetPlanJob = vi.hoisted(() => vi.fn());
const mockCancelPlanJob = vi.hoisted(() => vi.fn());
const mockRetryPlanJob = vi.hoisted(() => vi.fn());
const mockExpandPlan = vi.hoisted(() => vi.fn());
const mockCreateIssue = vi.hoisted(() => vi.fn());
const mockCreateProposalGeneration = vi.hoisted(() => vi.fn());
const mockInvalidateQueries = vi.hoisted(() => vi.fn());
const mockToastSuccess = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({
  api: {
    suggestSubissuePlans: mockSuggestPlans,
    getSubissuePlanJob: mockGetPlanJob,
    cancelSubissuePlanJob: mockCancelPlanJob,
    retrySubissuePlanJob: mockRetryPlanJob,
    expandSubissuePlan: mockExpandPlan,
    createProposalGeneration: mockCreateProposalGeneration,
  },
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/issues/queries", () => ({
  issueDetailOptions: () => ({ queryKey: ["issue"] }),
  issueKeys: {
    timeline: (issueId: string) => ["issues", "timeline", issueId],
  },
}));

vi.mock("@multica/core/issues/mutations", () => ({
  useCreateIssue: () => ({ mutateAsync: mockCreateIssue }),
}));

vi.mock("@tanstack/react-query", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-query")>();
  return {
    ...actual,
    useQuery: () => ({ data: { project_id: "project-1" } }),
    useQueryClient: () => ({ invalidateQueries: mockInvalidateQueries }),
  };
});

vi.mock("sonner", () => ({
  toast: { success: mockToastSuccess, error: vi.fn() },
}));

function renderModal(onClose = vi.fn()) {
  return render(
    <I18nProvider locale="en" resources={{ en: { modals: enModals } }}>
      <SubIssuePreviewModal data={{ issueId: "issue-1", commentId: "comment-1" }} onClose={onClose} />
    </I18nProvider>,
  );
}

function renderProposalModal() {
  return render(
    <I18nProvider locale="en" resources={{ en: { modals: enModals } }}>
      <SubIssuePreviewModal
        data={{ issueId: "issue-1", commentId: "comment-1", proposalId: "proposal-1" }}
        onClose={vi.fn()}
      />
    </I18nProvider>,
  );
}

const twoItemPlan = {
  id: "plan-2",
  name: "Balanced split",
  items: [
    { id: "plan-2-item-1", title: "【Billing】First task", goal: "Do the first task", business: "Billing" },
    { id: "plan-2-item-2", title: "【Billing】Second task", goal: "Do the second task", business: "Billing" },
    { id: "plan-2-item-3", title: "【Billing】Billing summary test", goal: "Test the complete flow", kind: "summary_test" as const, business: "Billing" },
  ],
};

describe("SubIssuePreviewModal", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockSuggestPlans.mockResolvedValue({
      plans: [
        { id: "plan-1", name: "Context first", coverage: "full" as const, items: [{ id: "plan-1-item-1", title: "Combined", goal: "Combined" }] },
        twoItemPlan,
      ],
      diagnostics: [{
        plan_id: "plan-1",
        strategy: "single_full_context",
        name: "Context first",
        source: "file",
        version: 1,
        model: "gemini-3.1-pro-preview",
        reasoning_effort: "high",
        temperature: 0.4,
        max_completion_tokens: 8192,
        timeout_ms: 120000,
        system_prompt: "System prompt for the selected plan",
        user_prompt: "User prompt with the original comment",
        raw_output: '{"name":"Context first","items":[]}',
        input_chars: 1234,
        output_chars: 567,
        prompt_tokens: 321,
        completion_tokens: 123,
        total_tokens: 444,
        finish_reason: "stop",
        attempt: 1,
      }],
      summary: {
        status: "success",
        total_duration_ms: 3200,
        upstream_duration_ms: 2900,
        upstream_calls: 5,
        retry_count: 1,
      },
    });
    mockExpandPlan.mockResolvedValue({
      subissues: [
        { id: "plan-2-item-1", title: "First task", goal: "Do the first task", description: "First details", stage: 1, depends_on_ids: [], suggested_parent_identifier: null, suggested_parent_issue_id: null, confidence: 0.9 },
        { id: "plan-2-item-2", title: "Second task", goal: "Do the second task", description: "Second details", stage: 2, depends_on_ids: [], suggested_parent_identifier: null, suggested_parent_issue_id: null, confidence: 0.9 },
        { id: "plan-2-item-3", title: "Balanced split summary test", goal: "Test the complete flow", description: "Integration test details", stage: 3, depends_on_ids: ["plan-2-item-1", "plan-2-item-2"], suggested_parent_identifier: null, suggested_parent_issue_id: null, confidence: 0.9 },
      ],
    });
    mockCreateIssue.mockResolvedValue({ id: "created" });
    mockCreateProposalGeneration.mockResolvedValue({
      id: "generation-1",
      proposal_id: "proposal-1",
      strategy: "parallel",
      create_mode: "backlog",
      snapshot: {},
      issues: [{ id: "child-1", identifier: "SIY-2", title: "First task", status: "backlog" }],
      created_at: "2026-09-14T00:00:00Z",
    });
    mockInvalidateQueries.mockResolvedValue(undefined);
  });

  it("preselects the full-coverage plan by default", async () => {
    renderModal();

    expect(await screen.findByText("Balanced split")).toBeInTheDocument();
    // plan-1 is the coverage-verified plan; its outline must be the one shown
    // without any click, and it carries the full-coverage badge.
    expect(screen.getAllByDisplayValue("Combined").length).toBeGreaterThan(0);
    expect(screen.getByText("Full coverage")).toBeInTheDocument();
    expect(screen.queryByText("Reference granularity only")).not.toBeInTheDocument();
  });

  it("allows closing the modal while a foreground generation request is pending", async () => {
    const onClose = vi.fn();
    mockSuggestPlans.mockImplementationOnce(() => new Promise(() => {}));
    renderModal(onClose);

    expect(await screen.findByTestId("subissue-generation-status")).toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "Close" }));

    expect(onClose).toHaveBeenCalledTimes(1);
    expect(mockSuggestPlans.mock.calls[0]?.[2]).toBeInstanceOf(AbortSignal);
    expect(mockSuggestPlans.mock.calls[0]?.[2]?.aborted).toBe(true);
  });

  it("can stop a foreground generation without closing the preview", async () => {
    const onClose = vi.fn();
    mockSuggestPlans.mockImplementationOnce(() => new Promise(() => {}));
    renderModal(onClose);

    expect(await screen.findByTestId("subissue-generation-status")).toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "Stop generation" }));

    expect(onClose).not.toHaveBeenCalled();
    expect(mockSuggestPlans.mock.calls[0]?.[2].aborted).toBe(true);
    await waitFor(() => expect(screen.queryByTestId("subissue-generation-status")).not.toBeInTheDocument());
  });

  it("shows upstream timing and retry telemetry for the split generation", async () => {
    renderModal();

    expect(await screen.findByText("Split generation succeeded")).toBeInTheDocument();
    expect(screen.getByText("3200")).toBeInTheDocument();
    expect(screen.getByText("2900")).toBeInTheDocument();
    expect(screen.getByText("5")).toBeInTheDocument();
    expect(screen.getByText("1")).toBeInTheDocument();
  });

  it("shows the immutable original agent answer as preview evidence", async () => {
    mockSuggestPlans.mockResolvedValueOnce({
      plans: [{ id: "plan-1", name: "Context first", coverage: "full" as const, items: [{ id: "item-1", title: "Task", goal: "Goal" }] }],
      source_packet: {
        version: 1,
        source_issue: { id: "source-1", identifier: "SIY-9", number: 9, title: "Source discussion" },
        agent_answer: "原始 agent 回复：保留所有约束。",
      },
      summary: { status: "success", total_duration_ms: 0, upstream_duration_ms: 0, upstream_calls: 1, retry_count: 0 },
    });

    renderModal();
    const evidence = await screen.findByRole("button", { name: /原始 agent 回复/ });
    await userEvent.setup().click(evidence);
    expect(await screen.findByText("原始 agent 回复：保留所有约束。", { exact: true })).toBeInTheDocument();
  });

  it("shows the associated-agent fallback while a 202 job is running and loads its plan", async () => {
    const fallbackJob = {
      id: "job-1",
      issue_id: "issue-1",
      source_snapshot_digest: "digest-1",
      context_revision: 4,
      status: "queued" as const,
      source: "fallback_agent",
      fallback_reason: "primary_llm_unavailable",
      agent_id: "agent-1",
      task_id: "task-1",
      plans: [],
      created_at: "2026-09-14T00:00:00Z",
      updated_at: "2026-09-14T00:00:00Z",
      expires_at: "2026-09-14T00:15:00Z",
    };
    mockSuggestPlans.mockResolvedValueOnce({
      job: fallbackJob,
      job_id: fallbackJob.id,
      status: "queued",
      source: fallbackJob.source,
      fallback_reason: fallbackJob.fallback_reason,
      diagnostics: [{ plan_id: "plan-fallback", strategy: "fallback", name: "Fallback", source: "fallback_agent", fallback_reason: "primary_llm_unavailable", agent_id: "agent-1", task_id: "task-1", model: "", temperature: 0, max_completion_tokens: 0, system_prompt: "", user_prompt: "", raw_output: "", input_chars: 0, output_chars: 0, prompt_tokens: 0, completion_tokens: 0, total_tokens: 0, finish_reason: "", attempt: 0 }],
      summary: { status: "running", total_duration_ms: 0, upstream_duration_ms: 0, upstream_calls: 0, retry_count: 0 },
    });
    mockGetPlanJob.mockResolvedValueOnce({
      ...fallbackJob,
      status: "succeeded",
      plans: [{ id: "fallback-plan", name: "Associated plan", coverage: "full", items: [{ id: "fallback-item", title: "Read-only task", goal: "Describe the task" }] }],
      proposal_id: "proposal-fallback",
    });

    renderModal();

    expect(await screen.findByTestId("subissue-plan-fallback-status")).toHaveTextContent(
      "The primary model is unavailable; using an associated agent",
    );
    await waitFor(() => expect(mockGetPlanJob).toHaveBeenCalledWith("job-1"), { timeout: 2000 });
    expect(await screen.findByDisplayValue("Read-only task")).toBeInTheDocument();
    expect(screen.getByText("Associated agent produced a split plan")).toBeInTheDocument();
  });

  it("cancels an accepted fallback job without closing the modal", async () => {
    const fallbackJob = {
      id: "job-cancel-1",
      issue_id: "issue-1",
      source_snapshot_digest: "digest-1",
      context_revision: 4,
      status: "queued" as const,
      source: "fallback_agent",
      fallback_reason: "primary_llm_unavailable",
      agent_id: "agent-1",
      task_id: "task-1",
      plans: [],
      created_at: "2026-09-14T00:00:00Z",
      updated_at: "2026-09-14T00:00:00Z",
      expires_at: "2026-09-14T00:15:00Z",
    };
    mockSuggestPlans.mockResolvedValueOnce({
      job: fallbackJob,
      job_id: fallbackJob.id,
      status: "queued",
      source: fallbackJob.source,
      fallback_reason: fallbackJob.fallback_reason,
      summary: { status: "running", total_duration_ms: 0, upstream_duration_ms: 0, upstream_calls: 0, retry_count: 0 },
    });
    mockGetPlanJob.mockResolvedValue(fallbackJob);
    mockCancelPlanJob.mockResolvedValueOnce({ ...fallbackJob, status: "cancelled" });
    const onClose = vi.fn();
    renderModal(onClose);

    expect(await screen.findByTestId("subissue-plan-fallback-status")).toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "Cancel planning" }));

    await waitFor(() => expect(mockCancelPlanJob).toHaveBeenCalledWith(fallbackJob.id));
    expect(onClose).not.toHaveBeenCalled();
    expect(await screen.findAllByText("Split generation stopped")).not.toHaveLength(0);
  });

  it("shows the selected strategy model, usage and prompts", async () => {
    renderModal();
    const user = userEvent.setup();

    expect(await screen.findByText("Context first")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Call details/ }));

    expect(screen.getAllByText("gemini-3.1-pro-preview").length).toBeGreaterThan(0);
    expect(screen.getByText("1234")).toBeInTheDocument();
    expect(screen.getByText("System prompt for the selected plan")).toBeInTheDocument();
    expect(screen.getByText("User prompt with the original comment")).toBeInTheDocument();
    expect(screen.getByText('{"name":"Context first","items":[]}')).toBeInTheDocument();
  });

  it("flags a reference plan when the user switches to it", async () => {
    renderModal();
    const user = userEvent.setup();

    expect(await screen.findByText("Balanced split")).toBeInTheDocument();
    expect(screen.getAllByDisplayValue("Combined").length).toBeGreaterThan(0);
    await user.click(screen.getByText("Balanced split"));
    expect(screen.getByText("Reference granularity only — it does not promise to cover every task in the original reply.")).toBeInTheDocument();
  });

  it("merges two approved draft items and sends the edited outline", async () => {
    renderModal();
    const user = userEvent.setup();

    expect(await screen.findByText("Balanced split")).toBeInTheDocument();
    await user.click(screen.getByText("Balanced split"));
    expect(screen.getByText("Integration test")).toBeInTheDocument();
    // Name-based lookup: the outline also carries the ancestor-context
    // checkbox above the plan list, so positional indexes select the wrong
    // controls.
    const mergeCheckboxes = screen.getAllByRole("checkbox", { name: "Select for merge" });
    await user.click(mergeCheckboxes[0]!);
    await user.click(mergeCheckboxes[1]!);
    await user.click(screen.getByRole("button", { name: "Merge selected" }));

    expect(screen.getByDisplayValue("【Billing】First task + Second task")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Generate details" }));

    await waitFor(() => expect(mockExpandPlan).toHaveBeenCalledTimes(1));
    expect(mockExpandPlan).toHaveBeenCalledWith("issue-1", expect.objectContaining({
      comment_id: "comment-1",
      plan: expect.objectContaining({
        id: "plan-2",
        items: [
          expect.objectContaining({ title: "【Billing】First task + Second task" }),
          expect.objectContaining({ kind: "summary_test" }),
        ],
      }),
    }), expect.any(AbortSignal));
    expect(await screen.findByText("Review sub-issue details")).toBeInTheDocument();
  });

  it("creates confirmed details without changing their order", async () => {
    renderModal();
    const user = userEvent.setup();

    await user.click(await screen.findByText("Balanced split"));
    await user.click(screen.getByRole("button", { name: "Generate details" }));
    await user.click(await screen.findByRole("button", { name: "Create 3 selected sub-issues" }));

    await waitFor(() => expect(mockCreateIssue).toHaveBeenCalledTimes(3));
    expect(mockCreateIssue).toHaveBeenNthCalledWith(1, expect.objectContaining({
      title: "First task",
      description: "First details",
      project_id: "project-1",
      status: "backlog",
      stage: 1,
    }));
    expect(mockCreateIssue).toHaveBeenNthCalledWith(2, expect.objectContaining({
      title: "Second task",
      description: "Second details",
      project_id: "project-1",
      status: "backlog",
      stage: 2,
    }));
    expect(mockCreateIssue).toHaveBeenNthCalledWith(3, expect.objectContaining({
      title: "Balanced split summary test",
      description: "Integration test details",
      stage: 3,
    }));
    expect(mockToastSuccess).toHaveBeenCalledWith("Created 3 sub-issues");
  });

  it("only starts children when the user explicitly chooses execution", async () => {
    renderModal();
    const user = userEvent.setup();

    await user.click(await screen.findByText("Balanced split"));
    await user.click(screen.getByRole("button", { name: "Generate details" }));
    await user.click(await screen.findByRole("radio", { name: /创建并开始执行/ }));
    await user.click(screen.getByRole("button", { name: "Create 3 selected sub-issues" }));

    await waitFor(() => expect(mockCreateIssue).toHaveBeenCalledTimes(3));
    expect(mockCreateIssue).toHaveBeenNthCalledWith(1, expect.objectContaining({ status: "todo" }));
    expect(mockCreateIssue).toHaveBeenNthCalledWith(2, expect.objectContaining({ status: "todo" }));
    expect(mockCreateIssue).toHaveBeenNthCalledWith(3, expect.objectContaining({ status: "todo" }));
  });

  it("scopes proposal batch idempotency to the selected create mode", async () => {
    renderProposalModal();
    const user = userEvent.setup();

    await user.click(await screen.findByText("Balanced split"));
    await user.click(screen.getByRole("button", { name: "Generate details" }));
    await user.click(await screen.findByRole("radio", { name: /创建并开始执行/ }));
    await user.click(screen.getByRole("button", { name: "Create 3 selected sub-issues" }));

    await waitFor(() => expect(mockCreateProposalGeneration).toHaveBeenCalledTimes(1));
    expect(mockCreateProposalGeneration).toHaveBeenCalledWith(
      "proposal-1",
      expect.objectContaining({
        create_mode: "todo",
        idempotency_key: expect.stringMatching(/:todo$/),
      }),
    );
    await waitFor(() => expect(mockInvalidateQueries).toHaveBeenCalledWith({
      queryKey: ["issues", "timeline", "issue-1"],
    }));
    expect(mockCreateIssue).not.toHaveBeenCalled();
  });

  it("updates the title prefix and business together", async () => {
    renderModal();
    const user = userEvent.setup();

    await user.click(await screen.findByText("Balanced split"));
    const businessInputs = screen.getAllByLabelText("Business");
    await user.clear(businessInputs[0]!);
    await user.type(businessInputs[0]!, "Payments");

    expect(screen.getByDisplayValue("【Payments】First task")).toBeInTheDocument();
    expect(screen.getByDisplayValue("Payments")).toBeInTheDocument();
  });

  it("renders the inherited execution context card from the plan response", async () => {
    mockSuggestPlans.mockResolvedValue({
      plans: [twoItemPlan],
      ancestor_brief_refs: [{ id: "root-1", updated_at: "2026-09-12T00:00:00Z" }],
      inherited_execution_context: {
        entries: [
          { kind: "repo", value: "https://github.com/acme/widget", source_issue_id: "root-1" },
          { kind: "code_dir", label: "checkout", value: "/mnt/a/widget", os: "linux", source_issue_id: "root-1" },
        ],
        sources: [{ issue_id: "root-1", identifier: "SIY-30", title: "Root", version: 3 }],
      },
    });
    renderModal();
    const user = userEvent.setup();

    // Collapsed by default: only the header shows.
    expect(await screen.findByText("Inherited execution context")).toBeInTheDocument();
    expect(screen.queryByText("/mnt/a/widget")).not.toBeInTheDocument();

    await user.click(screen.getByText("Inherited execution context"));
    expect(screen.getByText(/\/mnt\/a\/widget/)).toBeInTheDocument();
    expect(screen.getByText("https://github.com/acme/widget")).toBeInTheDocument();
    expect(screen.getAllByText("from SIY-30").length).toBe(2);
    expect(screen.getByText("SIY-30 · v3")).toBeInTheDocument();
  });

  it("hides the inherited context card when nothing resolves", async () => {
    renderModal();
    expect(await screen.findByText("Balanced split")).toBeInTheDocument();
    expect(screen.queryByText("Inherited execution context")).not.toBeInTheDocument();
  });

  it("stamps the issue-specific execution context onto every created child", async () => {
    renderModal();
    const user = userEvent.setup();

    await user.click(await screen.findByText("Balanced split"));
    await user.click(screen.getByRole("button", { name: "Generate details" }));
    await screen.findByRole("button", { name: "Create 3 selected sub-issues" });

    // Open the issue-specific context block and type the longtext (SIY-177
    // feedback: one free-form content block, no structured entry rows).
    await user.click(screen.getByText("Issue-specific execution context"));
    // The editor's textarea carries the default template as its placeholder.
    const contextBox = screen.getByPlaceholderText(/Repo URL: https:\/\/github.com\/your-org\/your-repo/);
    await user.type(contextBox, "Repo URL: https://github.com/acme/widget");

    await user.click(screen.getByRole("button", { name: "Create 3 selected sub-issues" }));
    await waitFor(() => expect(mockCreateIssue).toHaveBeenCalledTimes(3));
    for (const call of mockCreateIssue.mock.calls) {
      expect(call[0]).toEqual(expect.objectContaining({
        execution_context: expect.objectContaining({
          inherit: true,
          content: expect.stringContaining("Repo URL: https://github.com/acme/widget"),
        }),
      }));
    }
  });
});
