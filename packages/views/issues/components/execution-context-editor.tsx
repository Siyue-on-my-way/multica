"use client";

import { AlertTriangle, Trash2 } from "lucide-react";
import type { ExecutionContext, ExecutionContextEntry, ExecutionContextKind } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { useT } from "../../i18n";

// ExecutionContextEditor is the segmented "执行上下文" block (SIY-177):
// the machine-usable task setup that every descendant issue inherits. Kept
// deliberately OUT of the task description — the description stays the
// current issue's own words, while this structured block is what the runtime
// merges along the ancestor chain, de-duplicates and OS-filters.
//
// The block always shows the sensitive-data warning: entries are injected
// into every descendant's agent prompt, so a pasted token would leak into
// every future task in the subtree.

const KINDS: ExecutionContextKind[] = ["repo", "code_dir", "command", "constraint"];

/** Kinds whose value can be platform-specific and thus get an OS picker. */
const OS_SCOPED_KINDS = new Set<string>(["code_dir", "command"]);

export interface ExecutionContextEditorValue {
  entries: ExecutionContextEntry[];
  inherit: boolean;
}

export function emptyExecutionContextEditorValue(): ExecutionContextEditorValue {
  return { entries: [], inherit: true };
}

/** Build the persisted document; empty entries keep the issue context-free. */
export function toExecutionContext(value: ExecutionContextEditorValue): ExecutionContext | undefined {
  const entries = value.entries
    .map((entry) => ({
      ...entry,
      value: entry.value.trim(),
      label: entry.label?.trim() || undefined,
      os: entry.os || undefined,
    }))
    .filter((entry) => entry.value.length > 0);
  if (entries.length === 0) return undefined;
  return { version: 1, inherit: value.inherit, entries };
}

function defaultEntry(kind: ExecutionContextKind): ExecutionContextEntry {
  return { kind, value: "", os: kind === "code_dir" ? "linux" : "" };
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

  const updateEntry = (index: number, patch: Partial<ExecutionContextEntry>) => {
    onChange({
      ...value,
      entries: value.entries.map((entry, i) => (i === index ? { ...entry, ...patch } : entry)),
    });
  };

  return (
    <div className="flex flex-col gap-2 rounded-md border border-border/60 p-2.5">
      <div className="flex items-start gap-1.5 rounded-md bg-amber-500/10 px-2.5 py-2 text-caption text-amber-700 dark:text-amber-400">
        <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
        <div className="min-w-0">
          <div className="font-medium">{t(($) => $.execution_context.section_hint)}</div>
          <div>{t(($) => $.execution_context.sensitive_warning)}</div>
        </div>
      </div>

      {value.entries.map((entry, index) => (
        <div key={index} className="flex flex-col gap-1.5 rounded-md border border-border/50 p-2">
          <div className="flex items-center gap-1.5">
            <Select
              items={KINDS.map((kind) => ({
                value: kind,
                label: t(($) => $.execution_context[`kind_${kind}` as const]),
              }))}
              value={entry.kind}
              onValueChange={(kind) =>
                updateEntry(index, {
                  kind: (kind ?? entry.kind) as ExecutionContextKind,
                  // Reset the OS scope when the kind cannot carry one.
                  os: OS_SCOPED_KINDS.has(kind ?? "") ? (entry.os || "linux") : "",
                })
              }
            >
              <SelectTrigger className="h-7 w-28 shrink-0 text-caption">
                <SelectValue>
                  {() => (
                    <span className="truncate">
                      {t(($) => $.execution_context[`kind_${entry.kind}` as const])}
                    </span>
                  )}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                {KINDS.map((kind) => (
                  <SelectItem key={kind} value={kind}>
                    {t(($) => $.execution_context[`kind_${kind}` as const])}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Input
              value={entry.label ?? ""}
              onChange={(event) => updateEntry(index, { label: event.target.value })}
              placeholder={t(($) => $.execution_context.label_placeholder)}
              className="h-7 flex-1 text-caption"
              disabled={disabled}
            />
            {OS_SCOPED_KINDS.has(entry.kind) && (
              <Select
                items={[
                  { value: "all", label: t(($) => $.execution_context.os_all) },
                  { value: "linux", label: "Linux" },
                  { value: "macos", label: "macOS" },
                  { value: "windows", label: "Windows" },
                ]}
                value={entry.os || "all"}
                onValueChange={(os) => updateEntry(index, { os: os === "all" ? "" : (os ?? "") })}
              >
                <SelectTrigger className="h-7 w-24 shrink-0 text-caption">
                  <SelectValue>
                    {() => (
                      <span className="truncate">
                        {entry.os ? entry.os : t(($) => $.execution_context.os_all)}
                      </span>
                    )}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">{t(($) => $.execution_context.os_all)}</SelectItem>
                  <SelectItem value="linux">Linux</SelectItem>
                  <SelectItem value="macos">macOS</SelectItem>
                  <SelectItem value="windows">Windows</SelectItem>
                </SelectContent>
              </Select>
            )}
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-7 w-7 shrink-0 p-0 text-muted-foreground hover:text-destructive"
              aria-label={t(($) => $.execution_context.remove_entry)}
              disabled={disabled}
              onClick={() =>
                onChange({ ...value, entries: value.entries.filter((_, i) => i !== index) })
              }
            >
              <Trash2 className="h-3.5 w-3.5" />
            </Button>
          </div>
          <Input
            value={entry.value}
            onChange={(event) => updateEntry(index, { value: event.target.value })}
            placeholder={t(($) => $.execution_context.value_placeholder)}
            className="h-7 text-caption"
            disabled={disabled}
          />
        </div>
      ))}

      <div className="flex items-center justify-between gap-2">
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-7"
          disabled={disabled || value.entries.length >= 32}
          onClick={() =>
            onChange({ ...value, entries: [...value.entries, defaultEntry("repo")] })
          }
        >
          {t(($) => $.execution_context.add_entry)}
        </Button>
        <label className="flex items-center gap-1.5 text-caption text-muted-foreground">
          {t(($) => $.execution_context.inherit_label)}
          <Switch
            checked={value.inherit}
            onCheckedChange={(checked) => onChange({ ...value, inherit: checked === true })}
            disabled={disabled}
          />
        </label>
      </div>
    </div>
  );
}
