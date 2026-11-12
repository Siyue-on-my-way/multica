"use client";

import { useCallback, useMemo } from "react";
import { toast } from "sonner";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useCurrentWorkspace } from "@multica/core/paths";
import { buildQuickActionBody, quickActionListOptions } from "@multica/core/quick-actions";
import { useT } from "../../i18n";

/**
 * Supplies the comment composer's `/` menu with this workspace's quick actions
 * (MUL-5465) plus the workspace's saved phrases (SIY customization).
 *
 * Picking one INSERTS the cached body rather than running it — the composer
 * path exists precisely for the "this time is different" case, so the user
 * gets to edit and then send with the normal shortcut. Running without review
 * is what the sidebar button is for.
 *
 * The menu offers the same catalog the sidebar shows. Inserting draft text
 * needs no invocation preflight; the comment send path checks permission just
 * as it does for a hand-typed mention.
 *
 * Saved phrases are appended after the quick actions under a `saved-phrase:`
 * id prefix; picking one records the usage via the API (best effort) and
 * inserts the stored content, which like a quick action body stays editable
 * before sending.
 *
 * Reads workspace identity through `useCurrentWorkspace` (nullable) rather
 * than `useWorkspaceId` (throws): the comment composer also mounts outside a
 * workspace route, and an enhancement like this must degrade to "no quick
 * actions in the menu", never take the composer down with it.
 */
export function useQuickActionMenu() {
  const { t } = useT("issues");
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const { data } = useQuery({
    ...quickActionListOptions(wsId),
    enabled: wsId !== "",
  });
  const { data: savedPhrases = [] } = useQuery({
    queryKey: ["saved-phrases", wsId],
    queryFn: () => api.listSavedPhrases(),
    enabled: wsId !== "",
  });

  const actions = useMemo(
    () => (data ?? []).filter((a) => a.status === "active"),
    [data],
  );

  const menuItems = useMemo(
    () => [
      ...actions.map((a) => ({ id: a.id, name: a.name, description: a.description || undefined })),
      ...savedPhrases.map((phrase) => ({
        id: `saved-phrase:${phrase.id}`,
        name: `常用语：${phrase.name}`,
        description: phrase.category,
      })),
    ],
    [actions, savedPhrases],
  );

  const getQuickActions = useCallback(() => menuItems, [menuItems]);

  const getQuickActionContent = useCallback(
    (quickActionId: string) => {
      if (quickActionId.startsWith("saved-phrase:")) {
        const phraseId = quickActionId.slice("saved-phrase:".length);
        const phrase = savedPhrases.find((item) => item.id === phraseId);
        if (!phrase) return "";
        // Best-effort usage tracking; inserting the text must not depend on it.
        void api.useSavedPhrase(phrase.id).catch(() => undefined);
        return phrase.content;
      }
      const action = actions.find((a) => a.id === quickActionId);
      if (!action) return "";
      const content = buildQuickActionBody(action);
      if (content === null) {
        toast.error(t(($) => $.comment.trigger_blocked_target_unavailable));
        return "";
      }
      return content;
    },
    [actions, savedPhrases, t],
  );

  return useMemo(
    () => ({ getQuickActions, getQuickActionContent }),
    [getQuickActions, getQuickActionContent],
  );
}
