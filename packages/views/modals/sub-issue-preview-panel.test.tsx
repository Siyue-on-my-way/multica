import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SubIssuePreviewModal } from "./sub-issue-preview-panel";
import { I18nProvider } from "@multica/core/i18n/react";
import enModals from "../locales/en/modals.json";

const mockSuggestPlans = vi.hoisted(() => vi.fn());
const mockExpandPlan = vi.hoisted(() => vi.fn());
const mockCreateIssue = vi.hoisted(() => vi.fn());
const mockToastSuccess = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({
  api: {
    suggestSubissuePlans: mockSuggestPlans,
    expandSubissuePlan: mockExpandPlan,
  },
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/issues/queries", () => ({
  issueDetailOptions: () => ({ queryKey: ["issue"] }),
}));

vi.mock("@multica/core/issues/mutations", () => ({
  useCreateIssue: () => ({ mutateAsync: mockCreateIssue }),
}));

vi.mock("@tanstack/react-query", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-query")>();
  return {
    ...actual,
    useQuery: () => ({ data: { project_id: "project-1" } }),
  };
});

vi.mock("sonner", () => ({
  toast: { success: mockToastSuccess, error: vi.fn() },
}));

function renderModal() {
  return render(
    <I18nProvider locale="en" resources={{ en: { modals: enModals } }}>
      <SubIssuePreviewModal data={{ issueId: "issue-1", commentId: "comment-1" }} onClose={vi.fn()} />
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
    });
    mockExpandPlan.mockResolvedValue({
      subissues: [
        { id: "plan-2-item-1", title: "First task", goal: "Do the first task", description: "First details", stage: 1, depends_on_ids: [], suggested_parent_identifier: null, suggested_parent_issue_id: null, confidence: 0.9 },
        { id: "plan-2-item-2", title: "Second task", goal: "Do the second task", description: "Second details", stage: 2, depends_on_ids: [], suggested_parent_identifier: null, suggested_parent_issue_id: null, confidence: 0.9 },
        { id: "plan-2-item-3", title: "Balanced split summary test", goal: "Test the complete flow", description: "Integration test details", stage: 3, depends_on_ids: ["plan-2-item-1", "plan-2-item-2"], suggested_parent_identifier: null, suggested_parent_issue_id: null, confidence: 0.9 },
      ],
    });
    mockCreateIssue.mockResolvedValue({ id: "created" });
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
    }));
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
      status: "todo",
      stage: 1,
    }));
    expect(mockCreateIssue).toHaveBeenNthCalledWith(2, expect.objectContaining({
      title: "Second task",
      description: "Second details",
      project_id: "project-1",
      status: "todo",
      stage: 2,
    }));
    expect(mockCreateIssue).toHaveBeenNthCalledWith(3, expect.objectContaining({
      title: "Balanced split summary test",
      description: "Integration test details",
      stage: 3,
    }));
    expect(mockToastSuccess).toHaveBeenCalledWith("Created 3 sub-issues");
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
});
