"use client";

import { AlertTriangle } from "lucide-react";
import type { ExecutionContext } from "@multica/core/types";
import { Switch } from "@multica/ui/components/ui/switch";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../i18n";

// ExecutionContextEditor is the segmented "执行上下文" block (SIY-177): the
// task setup every descendant issue inherits. Kept deliberately OUT of the
// task description — the description stays the current issue's own words,
// while this block is what the runtime merges along the ancestor chain,
// de-duplicates and injects into the agent prompt.
//
// Per SIY-177 feedback the block is ONE free-form longtext: the user writes
// whatever they want to inherit (repo URLs, per-OS directories, commands,
// constraints), guided by a default template as the placeholder. The block
// always shows the sensitive-data warning: the text is injected into every
// descendant's agent prompt, so a pasted token would leak into every future
// task in the subtree.

export interface ExecutionContextEditorValue {
  content: string;
  inherit: boolean;
}

export function emptyExecutionContextEditorValue(): ExecutionContextEditorValue {
  return { content: "", inherit: true };
}

/** Build the persisted document; an empty text keeps the issue context-free. */
export function toExecutionContext(value: ExecutionContextEditorValue): ExecutionContext | undefined {
  const content = value.content.trim();
  if (!content) return undefined;
  return { version: 1, inherit: value.inherit, content };
}

/** Clamp free text to its first lines for the collapsed issue display. */
export function firstLines(text: string, max = 2): string {
  const lines = text.replace(/\n+$/, "").split("\n");
  return lines.slice(0, max).join("\n");
}

export function ExecutionContextEditor({
  value,
  onChange,
  disabled,
}: {
  value: ExecutionContextEditorValue;
  onChange: (next: ExecutionContextEditorValue) => void;
  disabled?: boolean;
}) {
  const { t } = useT("modals");

  return (
    <div className="flex flex-col gap-2 rounded-md border border-border/60 p-2.5">
      <div className="flex items-start gap-1.5 rounded-md bg-amber-500/10 px-2.5 py-2 text-caption text-amber-700 dark:text-amber-400">
        <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
        <div className="min-w-0">
          <div className="font-medium">{t(($) => $.execution_context.section_hint)}</div>
          <div>{t(($) => $.execution_context.sensitive_warning)}</div>
        </div>
      </div>

      <Textarea
        value={value.content}
        onChange={(event) => onChange({ ...value, content: event.target.value })}
        placeholder={t(($) => $.execution_context.content_placeholder)}
        className="min-h-40 text-caption"
        disabled={disabled}
      />

      <label className="flex items-center justify-end gap-1.5 text-caption text-muted-foreground">
        {t(($) => $.execution_context.inherit_label)}
        <Switch
          checked={value.inherit}
          onCheckedChange={(checked) => onChange({ ...value, inherit: checked === true })}
          disabled={disabled}
        />
      </label>
    </div>
  );
}
