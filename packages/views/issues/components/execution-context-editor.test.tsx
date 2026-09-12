// @vitest-environment jsdom
import { describe, it, expect, vi } from "vitest";
import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  ExecutionContextEditor,
  emptyExecutionContextEditorValue,
  firstLines,
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
  it("shows the inheritance and sensitive-data warnings plus the template placeholder", () => {
    renderEditor();
    expect(screen.getByText("Injected into every descendant issue of this one; edits affect future tasks")).toBeInTheDocument();
    expect(screen.getByText("Never enter passwords, tokens or other secrets here")).toBeInTheDocument();
    expect(screen.getByPlaceholderText(/Repo URL: https:\/\/github.com\/your-org\/your-repo/)).toBeInTheDocument();
  });

  it("accepts free-form longtext and reports the change", async () => {
    const user = userEvent.setup();
    let latest: { content: string; inherit: boolean } | null = null;
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

    const box = screen.getByRole("textbox");
    await user.type(box, "Repo URL: https://github.com/acme/widget\nStart: ./restart.sh");

    expect(latest!.content).toBe("Repo URL: https://github.com/acme/widget\nStart: ./restart.sh");
  });

  it("round-trips the document and keeps an empty text context-free", () => {
    const document = toExecutionContext({
      content: "  Repo URL: https://github.com/acme/widget  ",
      inherit: false,
    });
    expect(document).toEqual({
      version: 1,
      inherit: false,
      content: "Repo URL: https://github.com/acme/widget",
    });
    expect(toExecutionContext(emptyExecutionContextEditorValue())).toBeUndefined();
  });

  it("clamps display text to the first two lines", () => {
    expect(firstLines("a\nb\nc\nd")).toBe("a\nb");
    expect(firstLines("a\n\nb")).toBe("a\n");
    expect(firstLines("single line")).toBe("single line");
  });
});
