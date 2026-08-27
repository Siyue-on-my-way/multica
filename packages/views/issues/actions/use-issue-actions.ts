"use client";

import { useCallback, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Issue, UpdateIssueRequest } from "@multica/core/types";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { useModalStore } from "@multica/core/modals";
import { useUpdateIssue } from "@multica/core/issues/mutations";
import { issueKeys } from "@multica/core/issues/queries";
import { pinListOptions, useCreatePin, useDeletePin } from "@multica/core/pins";
import { api, dispatchReasonCode } from "@multica/core/api";
import { copyText } from "@multica/ui/lib/clipboard";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { useIssueSurfaceActionsOptional } from "../surface/actions-context";

export interface UseIssueActionsResult {
  isPinned: boolean;
  updateField: (updates: Partial<UpdateIssueRequest>) => void;
  openInNewTab: () => void;
  togglePin: () => void;
  copyLink: () => Promise<void>;
  openCreateSubIssue: () => void;
  openSetParent: () => void;
  removeParent: () => void;
  openAddChild: () => void;
  openDeleteConfirm: (opts?: { onDeletedFallbackPath?: string }) => void;
  /** True only for an agent- or squad-assigned issue — the only case where a
   *  fresh-session-with-compacted-context run is meaningful. */
  canCompactContext: boolean;
  compactingContext: boolean;
  compactContext: () => void;
}

/**
 * Accepts a nullable issue so callers can invoke the hook before they've
 * early-returned on a missing issue. Returned handlers are safe no-ops when
 * `issue` is null.
 */
export function useIssueActions(issue: Issue | null): UseIssueActionsResult {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const user = useAuthStore((s) => s.user);
  const userId = user?.id;

  const { data: pinnedItems = [] } = useQuery({
    ...pinListOptions(wsId, userId ?? ""),
    enabled: !!userId,
  });

  const isPinned =
    !!issue &&
    pinnedItems.some(
      (p) => p.item_type === "issue" && p.item_id === issue.id,
    );

  const updateIssue = useUpdateIssue();
  const surfaceActions = useIssueSurfaceActionsOptional();
  const createPin = useCreatePin();
  const deletePin = useDeletePin();
  const openModal = useModalStore((s) => s.open);
  const queryClient = useQueryClient();

  const issueId = issue?.id ?? null;
  const issueIdentifier = issue?.identifier ?? null;
  const issueProjectId = issue?.project_id ?? null;
  const issueStatus = issue?.status ?? null;
  const issueAssigneeType = issue?.assignee_type ?? null;

  const updateField = useCallback(
    (updates: Partial<UpdateIssueRequest>) => {
      if (!issueId) return;
      // Assigning to an agent/squad may start a run. Route through the
      // pre-trigger confirm modal (preview + optional handoff note + "暂不开始"),
      // which applies the change itself — the four entry points share this one
      // backend-driven flow instead of guessing (MUL-3375). Every other field
      // change (status, priority, member assign, unassign) applies directly.
      //
      // Backlog is the parking lot: assigning a backlog issue never starts a run
      // (server/internal/service/issue_trigger.go), so the modal would only show
      // an empty "won't start" box with a single Apply button. Apply directly,
      // matching the batch backlog short-circuit in BatchActionToolbar.
      if (
        (updates.assignee_type === "agent" || updates.assignee_type === "squad") &&
        updates.assignee_id &&
        issueStatus !== "backlog"
      ) {
        openModal("issue-run-confirm", {
          issueIds: [issueId],
          mode: "assign",
          assigneeType: updates.assignee_type,
          assigneeId: updates.assignee_id,
        });
        return;
      }
      if (surfaceActions) {
        surfaceActions.updateIssue(issueId, updates, {
          errorMessage: t(($) => $.detail.update_failed),
        });
      } else {
        updateIssue.mutate(
          { id: issueId, ...updates },
          {
            onError: (err) =>
              toast.error(
                err instanceof Error && err.message
                  ? err.message
                  : t(($) => $.detail.update_failed),
              ),
          },
        );
      }
    },
    [issueId, issueStatus, surfaceActions, updateIssue, openModal, t],
  );

  // Explicit "open it somewhere else" CTA, so the new tab takes focus
  // (`activate: true`) — the user is asking to move into the new context, not
  // to stash it for later the way modifier-click does. Same contract as the
  // table row open and the attachment preview's "Open in new tab".
  //
  // Only desktop implements `openInNewTab`; on web it is undefined and we fall
  // back to a real browser tab via the shareable URL.
  const openInNewTab = useCallback(() => {
    if (!issueId) return;
    // Identifier form, same as copyLink: on web this becomes a real browser
    // tab at the shareable URL, so it is a link the user sees and may copy out
    // of the address bar. Opening on the UUID would also make the route
    // immediately rewrite the fresh tab's URL.
    const path = paths.issueDetail(issueIdentifier || issueId);
    if (navigation.openInNewTab) {
      navigation.openInNewTab(path, issueIdentifier ?? undefined, {
        activate: true,
      });
      return;
    }
    window.open(
      navigation.getShareableUrl(path),
      "_blank",
      "noopener,noreferrer",
    );
  }, [issueId, issueIdentifier, navigation, paths]);

  const togglePin = useCallback(() => {
    if (!issueId) return;
    if (isPinned) {
      deletePin.mutate({ itemType: "issue", itemId: issueId });
    } else {
      createPin.mutate({ item_type: "issue", item_id: issueId });
    }
  }, [isPinned, issueId, createPin, deletePin]);

  const copyLink = useCallback(async () => {
    if (!issueId) return;
    // Share the identifier form (`/{ws}/issues/MUL-123`): a pasted link should
    // say which issue it points at. The UUID form stays valid, so links copied
    // before this still resolve.
    const url = navigation.getShareableUrl(paths.issueDetail(issueIdentifier || issueId));
    if (await copyText(url)) {
      toast.success(t(($) => $.detail.link_copied));
    } else {
      toast.error(t(($) => $.detail.link_copy_failed));
    }
  }, [paths, issueId, issueIdentifier, navigation, t]);

  const openCreateSubIssue = useCallback(() => {
    if (!issueId) return;
    openModal("create-issue", {
      parent_issue_id: issueId,
      parent_issue_identifier: issueIdentifier,
      ...(issueProjectId ? { project_id: issueProjectId } : {}),
    });
  }, [openModal, issueId, issueIdentifier, issueProjectId]);

  const openSetParent = useCallback(() => {
    if (!issueId) return;
    openModal("issue-set-parent", { issueId });
  }, [openModal, issueId]);

  // Detach from the parent and promote to a standalone issue. Reversible
  // (Set parent re-links it), non-destructive, and mirrors the clear-date
  // actions — so it applies directly instead of a confirm modal. `stage`
  // only orders sub-issues under a parent, so clear it in the same write to
  // avoid an orphaned value on a standalone issue. The success toast fires
  // from onSuccess, not eagerly after mutate() — otherwise a request that
  // fails on permission/network/validation would flash "removed" before the
  // error toast and the optimistic rollback (false confirmation).
  const removeParent = useCallback(() => {
    if (!issueId) return;
    if (surfaceActions) {
      surfaceActions.updateIssue(
        issueId,
        { parent_issue_id: null, stage: null },
        {
          onSuccess: () =>
            toast.success(t(($) => $.actions.remove_parent_issue_success)),
          errorMessage: t(($) => $.detail.update_failed),
        },
      );
    } else {
      updateIssue.mutate(
        { id: issueId, parent_issue_id: null, stage: null },
        {
          onSuccess: () =>
            toast.success(t(($) => $.actions.remove_parent_issue_success)),
          onError: (err) =>
            toast.error(
              err instanceof Error && err.message
                ? err.message
                : t(($) => $.detail.update_failed),
            ),
        },
      );
    }
  }, [issueId, surfaceActions, updateIssue, t]);

  const openAddChild = useCallback(() => {
    if (!issueId) return;
    openModal("issue-add-child", { issueId });
  }, [openModal, issueId]);

  const openDeleteConfirm = useCallback(
    (opts?: { onDeletedFallbackPath?: string }) => {
      if (!issueId) return;
      openModal("issue-delete-confirm", {
        issueId,
        identifier: issueIdentifier,
        onDeletedFallbackPath: opts?.onDeletedFallbackPath,
      });
    },
    [openModal, issueId, issueIdentifier],
  );

  // Only meaningful when an agent (or a squad, whose leader is an agent) can
  // actually pick up a fresh run — a member-assigned or unassigned issue has
  // no session to compact.
  const canCompactContext = issueAssigneeType === "agent" || issueAssigneeType === "squad";

  const [compactingContext, setCompactingContext] = useState(false);

  // Standalone "compact context now": no task_id (the manual, no-failure
  // trigger — distinct from the execution-log per-row retry-in-new-session
  // action), always forces a fresh handoff_summary via with_context_compress.
  // Cancels any active run on the current assignee and starts a new one from
  // the compacted checkpoint, same as a cross-agent handoff.
  const compactContext = useCallback(() => {
    if (!issueId || compactingContext) return;
    setCompactingContext(true);
    api
      .rerunIssue(issueId, undefined, true)
      .then(() => {
        toast.success(t(($) => $.actions.compact_context_success));
        void queryClient.invalidateQueries({ queryKey: issueKeys.tasks(issueId) });
      })
      .catch((e: unknown) => {
        toast.error(
          dispatchReasonCode(e) === "invocation_not_allowed"
            ? t(($) => $.actions.compact_context_blocked)
            : e instanceof Error && e.message
              ? e.message
              : t(($) => $.actions.compact_context_failed),
        );
      })
      .finally(() => setCompactingContext(false));
  }, [issueId, compactingContext, queryClient, t]);

  return {
    isPinned,
    updateField,
    openInNewTab,
    togglePin,
    copyLink,
    openCreateSubIssue,
    openSetParent,
    removeParent,
    openAddChild,
    openDeleteConfirm,
    canCompactContext,
    compactingContext,
    compactContext,
  };
}
