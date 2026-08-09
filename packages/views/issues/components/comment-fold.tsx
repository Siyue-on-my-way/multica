"use client";

import { useState, useRef, useEffect, type ReactNode } from "react";
import { ChevronDown, ChevronUp } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

interface CommentFoldProps {
  children: ReactNode;
  /** The maximum height in pixels before the content is folded. */
  maxHeight?: number;
  /** The buffer height in pixels. Content will only fold if its actual height exceeds maxHeight + buffer. */
  buffer?: number;
  className?: string;
}

export function CommentFold({
  children,
  maxHeight = 500,
  buffer = 100,
  className,
}: CommentFoldProps) {
  const { t } = useT("issues");
  const contentRef = useRef<HTMLDivElement>(null);
  const [isOverflowing, setIsOverflowing] = useState(false);
  const [isExpanded, setIsExpanded] = useState(false);

  useEffect(() => {
    const el = contentRef.current;
    if (!el) return;

    // Measure the content's natural height directly, never the clamped
    // wrapper: scrollHeight on the clamped element collapses to maxHeight
    // once folded, which would make "is it overflowing" depend on whether
    // it's currently folded — an oscillating ResizeObserver feedback loop
    // that never settles (fold -> shrinks -> looks unfolded -> unfold ->
    // grows -> looks overflowing -> fold -> ...).
    const checkOverflow = () => {
      setIsOverflowing(el.scrollHeight > maxHeight + buffer);
    };

    checkOverflow();

    // Re-check when images load or content changes size
    const observer = new ResizeObserver(() => {
      checkOverflow();
    });
    observer.observe(el);

    return () => observer.disconnect();
  }, [maxHeight, buffer]);

  return (
    <div className={cn("relative", className)}>
      <div
        ref={contentRef}
        className={cn(
          "transition-all duration-300",
          isOverflowing && !isExpanded && "overflow-hidden relative",
        )}
        style={isOverflowing && !isExpanded ? { maxHeight: `${maxHeight}px` } : undefined}
      >
        {children}

        {isOverflowing && !isExpanded && (
          <div className="absolute bottom-0 left-0 right-0 h-32 bg-gradient-to-t from-background to-transparent pointer-events-none flex items-end justify-center pb-2">
            <Button
              variant="secondary"
              size="sm"
              className="pointer-events-auto shadow-sm rounded-full px-4"
              onClick={() => setIsExpanded(true)}
            >
              <ChevronDown className="mr-1.5 h-4 w-4" />
              {t(($) => $.comment.show_more)}
            </Button>
          </div>
        )}
      </div>

      {isOverflowing && isExpanded && (
        <div className="mt-2 flex justify-center">
          <Button
            variant="ghost"
            size="sm"
            className="text-muted-foreground rounded-full px-4"
            onClick={() => setIsExpanded(false)}
          >
            <ChevronUp className="mr-1.5 h-4 w-4" />
            {t(($) => $.comment.show_less)}
          </Button>
        </div>
      )}
    </div>
  );
}
