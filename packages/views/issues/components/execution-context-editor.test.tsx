// @vitest-environment jsdom
import { describe, it, expect, vi } from "vitest";
import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  ExecutionContextEditor,
  emptyExecutionContextEditorValue,
  toExecutionContext,
} from "./execution-context-editor";
import { I18nProvider } from "@multica/core/i18n/react";
import enModals from "../../locales/en/modals.json";

function renderEditor(value = emptyExecutionContextEditorValue(), onChange = vi.fn()) {
  render(
    <I18nProvider locale="en" resources={{ en: { modals: enModals } }}>
      <ExecutionContextEditor value={value} onChange={onChange} />
    </I18nProvider>,
  );
  return { onChange, value };
}

describe("ExecutionContextEditor", () => {
  it("shows the inheritance and sensitive-data warnings up front", () => {
    renderEditor();
    expect(screen.getByText("Injected into every descendant issue of this one; edits affect future tasks")).toBeInTheDocument();
    expect(screen.getByText("Never enter passwords, tokens or other secrets here")).toBeInTheDocument();
  });

  it("adds an entry, fills it, and reports the change", async () => {
    const user = userEvent.setup();
    let latest: { entries: { kind: string; value: string }[] } | null = null;
    function Harness() {
      const [value, setValue] = useState(emptyExecutionContextEditorValue());
      return (
        <ExecutionContextEditor
          value={value}
          onChange={(next) => {
            latest = next;
            setValue(next);
          }}
        />
      );
    }
    render(
      <I18nProvider locale="en" resources={{ en: { modals: enModals } }}>
        <Harness />
      </I18nProvider>,
    );

    await user.click(screen.getByRole("button", { name: "Add entry" }));
    await user.type(screen.getByPlaceholderText("Repo URL, path, command or constraint"), "https://github.com/acme/widget");

    expect(latest!.entries).toHaveLength(1);
    expect(latest!.entries[0]!.kind).toBe("repo");
    expect(latest!.entries[0]!.value).toBe("https://github.com/acme/widget");
  });

  it("round-trips a document and drops empty entries", () => {
    const document = toExecutionContext({
      inherit: false,
      entries: [
        { kind: "repo", label: "widget", value: "https://github.com/acme/widget", os: "" },
        { kind: "command", value: "   " },
      ],
    });
    expect(document).toEqual({
      version: 1,
      inherit: false,
      entries: [{ kind: "repo", label: "widget", value: "https://github.com/acme/widget", os: undefined }],
    });
    expect(toExecutionContext(emptyExecutionContextEditorValue())).toBeUndefined();
  });
});
