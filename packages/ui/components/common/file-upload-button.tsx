"use client";

import { useRef } from "react";
import { FolderOpen, Paperclip } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@multica/ui/components/ui/button";
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

  const iconSize = size === "sm" ? "h-3.5 w-3.5" : "h-4 w-4";
  const buttonSize = size === "sm" ? "icon-xs" : "icon-sm";

  return (
    <>
      <Button
        type="button"
        variant="ghost"
        size={buttonSize}
        onClick={() => inputRef.current?.click()}
        disabled={disabled}
        aria-label={attachLabel}
        title={attachLabel}
        className={cn("text-muted-foreground", className)}
      >
        <Paperclip className={iconSize} />
      </Button>
      {onSelectFromLibrary && (
        <Button
          type="button"
          variant="ghost"
          size={buttonSize}
          onClick={onSelectFromLibrary}
          disabled={disabled}
          aria-label={libraryLabel}
          title={libraryLabel}
          className={cn("text-muted-foreground", className)}
        >
          <FolderOpen className={iconSize} />
        </Button>
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
