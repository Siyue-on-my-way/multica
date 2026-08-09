import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { TimelineEntry } from "@multica/core/types";
import { useCommentCollapseStore } from "@multica/core/issues/stores";
import { renderWithI18n } from "../../test/i18n";

vi.mock("../../navigation", () => ({
  useNavigation: () => ({
    push: vi.fn(),
    pathname: "/acme/issues",
    getShareableUrl: (p: string) => `https://app.example${p}`,
  }),
}));

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: () => "Ada" }),
}));

vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: () => null,
}));

vi.mock("../hooks/use-comment-trigger-preview", () => ({
  useCommentTriggerPreview: () => ({ agents: [], blocked: [] }),
}));

vi.mock("../../editor", async () => ({
  ...(await vi.importActual<typeof import("../../editor/use-upload-gate")>(
    "../../editor/use-upload-gate",
  )),
  ...(await vi.importActual<typeof import("../../editor/use-lazy-editor")>(
    "../../editor/use-lazy-editor",
  )),
  ...(await vi.importActual<typeof import("../../editor/use-composer-submit")>(
    "../../editor/use-composer-submit",
  )),
  useEditorUpload: () => ({ uploadWithToast: vi.fn(), upload: vi.fn(), uploading: false }),
  useFileDropZone: () => ({ isDragOver: false, dropZoneProps: {} }),
  FileDropOverlay: () => null,
  ReadonlyContent: ({ content }: { content: string }) => <div>{content}</div>,
  Attachment: () => null,
  AttachmentDownloadProvider: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  ContentEditor: () => <textarea data-testid="editor" />,
}));

import { CommentCard } from "./comment-card";

const ISSUE_ID = "issue-1";

function makeEntry(overrides: Partial<TimelineEntry> = {}): TimelineEntry {
  return {
    id: "root-1",
    issue_id: ISSUE_ID,
    parent_id: null,
    actor_type: "member",
    actor_id: "user-1",
    content: "Question body",
    type: "comment",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    attachments: [],
    reactions: [],
    ...overrides,
  } as unknown as TimelineEntry;
}

function renderCard(entry: TimelineEntry, replies: TimelineEntry[] = []) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <CommentCard
        issueId={ISSUE_ID}
        entry={entry}
        replies={replies}
        currentUserId="user-1"
        onReply={vi.fn().mockResolvedValue(true)}
        onEdit={vi.fn().mockResolvedValue(undefined)}
        onDelete={vi.fn()}
        onToggleReaction={vi.fn()}
        onResolveToggle={vi.fn()}
      />
    </QueryClientProvider>,
  );
}

/** The header's own fold chevron — `aria-controls="comment-body-<id>"`
 *  uniquely identifies it, one per row (root or reply). */
function getChevron(id: string) {
  const btn = document.querySelector(`button[aria-controls="comment-body-${id}"]`);
  if (!btn) throw new Error(`Expected a chevron for ${id}`);
  return btn;
}

/** The body's actual fold target. Presence/absence here — not text content —
 *  is what "folded" means: the header preview can echo the same short text
 *  when the body itself is gone, so asserting on text alone is ambiguous. */
function getBodyElement(id: string) {
  return document.getElementById(`comment-body-${id}`);
}

/** Root's "···" menu trigger. The root always renders before any reply in
 *  DOM order (header+body first, replies section second), so it is always
 *  the first `aria-haspopup="menu"` trigger — replies get their own trigger
 *  once the thread is open, which must NOT be confused with the root's. */
function getRootMenuTrigger() {
  const trigger = document.querySelector('button[aria-haspopup="menu"]');
  if (!trigger) throw new Error("Expected the root comment actions menu trigger");
  return trigger;
}

beforeEach(() => {
  useCommentCollapseStore.setState({ collapsedByIssue: {} });
});

describe("CommentCard — layered fold state (MUL-5711)", () => {
  it("folding just the question body via the header chevron leaves replies visible", () => {
    const reply = makeEntry({ id: "reply-1", parent_id: "root-1", content: "Reply body" });
    renderCard(makeEntry(), [reply]);

    expect(getBodyElement("root-1")).not.toBeNull();
    expect(getBodyElement("reply-1")).not.toBeNull();

    fireEvent.click(getChevron("root-1"));

    expect(getBodyElement("root-1")).toBeNull();
    // Replies must survive a body-only fold.
    expect(getBodyElement("reply-1")).not.toBeNull();
  });

  it('collapsing the whole thread from the "···" menu hides the body AND every reply', async () => {
    const reply = makeEntry({ id: "reply-1", parent_id: "root-1", content: "Reply body" });
    renderCard(makeEntry(), [reply]);

    fireEvent.click(getRootMenuTrigger());
    fireEvent.click(await screen.findByText("Collapse thread"));

    expect(getBodyElement("root-1")).toBeNull();
    expect(getBodyElement("reply-1")).toBeNull();
    // Folds to the one-line summary: preview + reply count.
    expect(screen.getByText("1 reply")).toBeInTheDocument();
  });

  it("folding the body, then collapsing the whole thread, then re-expanding the thread, restores the body-folded state", async () => {
    const reply = makeEntry({ id: "reply-1", parent_id: "root-1", content: "Reply body" });
    renderCard(makeEntry(), [reply]);

    // Fold just the body first.
    fireEvent.click(getChevron("root-1"));
    expect(getBodyElement("root-1")).toBeNull();
    expect(getBodyElement("reply-1")).not.toBeNull();

    // Now collapse the whole thread via the menu.
    fireEvent.click(getRootMenuTrigger());
    fireEvent.click(await screen.findByText("Collapse thread"));
    expect(getBodyElement("reply-1")).toBeNull();

    // Re-expand the whole thread via the header chevron (thread-collapsed →
    // chevron's only job is re-expanding the thread).
    fireEvent.click(getChevron("root-1"));

    // Replies are back...
    expect(getBodyElement("reply-1")).not.toBeNull();
    // ...but the body stays folded — the two layers are independent, so
    // re-expanding the thread must NOT silently re-expand the body too.
    expect(getBodyElement("root-1")).toBeNull();
  });

  it("each reply folds independently via its own chevron", () => {
    const replyA = makeEntry({ id: "reply-a", parent_id: "root-1", content: "Reply A body" });
    const replyB = makeEntry({ id: "reply-b", parent_id: "root-1", content: "Reply B body" });
    renderCard(makeEntry(), [replyA, replyB]);

    fireEvent.click(getChevron("reply-a"));

    expect(getBodyElement("reply-a")).toBeNull();
    // Reply B and the root question are unaffected.
    expect(getBodyElement("reply-b")).not.toBeNull();
    expect(getBodyElement("root-1")).not.toBeNull();
  });

  it('starting an edit from the "···" menu re-expands a body-folded root', async () => {
    renderCard(makeEntry());

    fireEvent.click(getChevron("root-1"));
    expect(getBodyElement("root-1")).toBeNull();

    fireEvent.click(getRootMenuTrigger());
    fireEvent.click(await screen.findByText("Edit"));

    // The editor must be visible — a body-folded root starting an edit via
    // the menu (bypassing the chevron) must not mount into a hidden section.
    expect(getBodyElement("root-1")).not.toBeNull();
    expect(screen.getByTestId("editor")).toBeInTheDocument();
  });
});
