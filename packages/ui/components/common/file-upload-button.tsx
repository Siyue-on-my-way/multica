"use client";

import { useRef } from "react";
import { FolderOpen, Paperclip } from "@multica/ui/icons";
import { useTranslation } from "react-i18next";
import { IconButton } from "@multica/ui/components/common/icon-button";
import { cn } from "@multica/ui/lib/utils";

interface FileUploadButtonProps {
  /** Called with the selected File — caller handles upload. */
  onSelect: (file: File) => void;
  /** When set, renders a second affordance that opens the workspace file
   *  library picker — attach existing library files without re-uploading. */
  onSelectFromLibrary?: () => void;
  disabled?: boolean;
  className?: string;
  size?: "sm" | "default";
  multiple?: boolean;
}

function FileUploadButton({
  onSelect,
  onSelectFromLibrary,
  disabled,
  className,
  size = "default",
  multiple = false,
}: FileUploadButtonProps) {
  const { t } = useTranslation("ui");
  const inputRef = useRef<HTMLInputElement>(null);
  const attachLabel = t(($) => $.attach_file);
  const libraryLabel = t(($) => $.choose_from_library);

  const handleChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files ?? []);
    if (files.length === 0) return;
    e.target.value = "";
    for (const file of files) onSelect(file);
  };

  // `size-*`, not `h-* w-*`: the icon button's own `[&_svg:not([class*='size-'])]`
  // rule would otherwise override it (icon-xs forces 12px).
  const iconSize = size === "sm" ? "size-3.5" : "size-4.5";
  const buttonSize = size === "sm" ? "icon-xs" : "icon-sm";

  return (
    <>
      <IconButton
        type="button"
        variant="ghost"
        size={buttonSize}
        onClick={() => inputRef.current?.click()}
        disabled={disabled}
        label={attachLabel}
        className={cn("text-muted-foreground", className)}
      >
        <Paperclip className={iconSize} />
      </IconButton>
      {onSelectFromLibrary && (
        <IconButton
          type="button"
          variant="ghost"
          size={buttonSize}
          onClick={onSelectFromLibrary}
          disabled={disabled}
          label={libraryLabel}
          className={cn("text-muted-foreground", className)}
        >
          <FolderOpen className={iconSize} />
        </IconButton>
      )}
      <input
        ref={inputRef}
        type="file"
        multiple={multiple}
        className="hidden"
        onChange={handleChange}
      />
    </>
  );
}

export { FileUploadButton, type FileUploadButtonProps };
