import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import type { DiscussionProposal } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

vi.mock("../../navigation", () => ({
  AppLink: ({ href, children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement>) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    issueDetail: (id: string) => `/acme/issues/${id}`,
  }),
}));

import { CreatedSubissueSummary } from "./proposal-card";

function makeProposal(generations: DiscussionProposal["generations"]): DiscussionProposal {
  return {
    id: "proposal-1",
    workspace_id: "workspace-1",
    source_issue_id: "issue-1",
    snapshot: {
      title: "Duplicated proposal title",
      summary: "Duplicated proposal summary",
    },
    generations,
    created_at: "2026-09-17T00:00:00Z",
    updated_at: "2026-09-17T00:00:00Z",
  };
}

describe("CreatedSubissueSummary", () => {
  it("renders only compact batch results and child links", () => {
    renderWithI18n(
      <CreatedSubissueSummary
        proposal={makeProposal([
          {
            id: "generation-1",
            proposal_id: "proposal-1",
            strategy: "single_full_context",
            create_mode: "backlog",
            snapshot: {},
            issues: [
              {
                id: "child-1",
                identifier: "SIY-155",
                title: "Fix video generation failures",
                status: "backlog",
              },
            ],
            created_at: "2026-09-17T00:00:00Z",
          },
        ])}
      />,
    );

    expect(screen.getByText("Created batches")).toBeInTheDocument();
    expect(screen.getByText("single_full_context")).toBeInTheDocument();
    expect(screen.getByText("Not started")).toBeInTheDocument();
    expect(screen.getByText("1 sub-issue")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "SIY-155 · Fix video generation failures" }))
      .toHaveAttribute("href", "/acme/issues/child-1");
    expect(screen.queryByText("Duplicated proposal title")).not.toBeInTheDocument();
    expect(screen.queryByText("Duplicated proposal summary")).not.toBeInTheDocument();
  });

  it("renders nothing when no batch has been created", () => {
    const { container } = render(
      <CreatedSubissueSummary proposal={makeProposal([])} />,
    );

    expect(container.firstChild).toBeNull();
  });
});
