"use client";

import { useState, type ReactElement } from "react";
import type { Issue } from "@multica/core/types";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
} from "@multica/ui/components/ui/dropdown-menu";
import { useIssueActions } from "./use-issue-actions";
import {
  IssueActionsMenuItems,
  dropdownPrimitives,
} from "./issue-actions-menu-items";
// SIY: "Move to another workspace" opens a dialog from the menu, mounted next
// to the dropdown rather than inside it so it survives the menu closing.
import { MoveIssueWorkspaceDialog } from "../components/move-issue-workspace-dialog";

interface IssueActionsDropdownProps {
  issue: Issue;
  /** A single React element cloned by Base UI as the trigger (via `render` prop). */
  trigger: ReactElement;
  align?: "start" | "end" | "center";
  /** If set, leave the page after the issue is deleted — back to wherever the
   *  user came from, or to this path when there is no in-app history. */
  onDeletedFallbackPath?: string;
  /** The reader's archived inbox notification for this issue; adds "Restore to inbox". */
  archivedInboxItemId?: string;
  /** Adds "Show in sidebar". */
  onShowInSidebar?: () => void;
}

export function IssueActionsDropdown({
  issue,
  trigger,
  align = "end",
  onDeletedFallbackPath,
  archivedInboxItemId,
  onShowInSidebar,
}: IssueActionsDropdownProps) {
  const actions = useIssueActions(issue);
  const [open, setOpen] = useState(false);
  const [moveOpen, setMoveOpen] = useState(false);

  return (
    <>
      <DropdownMenu open={open} onOpenChange={setOpen}>
        <DropdownMenuTrigger render={trigger} />
        <DropdownMenuContent align={align} className="w-auto">
          <IssueActionsMenuItems
            issue={issue}
            actions={actions}
            primitives={dropdownPrimitives}
            onClose={() => setOpen(false)}
            onOpenMove={() => setMoveOpen(true)}
            onDeletedFallbackPath={onDeletedFallbackPath}
            archivedInboxItemId={archivedInboxItemId}
            onShowInSidebar={onShowInSidebar}
          />
        </DropdownMenuContent>
      </DropdownMenu>
      {moveOpen && (
        <MoveIssueWorkspaceDialog issue={issue} open={moveOpen} onOpenChange={setMoveOpen} />
      )}
    </>
  );
}
