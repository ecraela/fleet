import { Check, SquareTwoStack } from "../../icons";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { Button as AriaButton } from "react-aria-components";
import { cx } from "../../utils/cx";
import { IconButton } from "../button/button";
import { Tooltip } from "../tooltip/tooltip";

function useCopy(value: string) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<number | undefined>(undefined);

  useEffect(() => () => window.clearTimeout(timer.current), []);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
    } catch {
      return;
    }
    setCopied(true);
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setCopied(false), 2000);
  };

  return { copied, copy };
}

export interface CopyButtonProps {
  value: string;
  /** Accessible name, e.g. "Copy change ID". */
  label?: string;
  className?: string;
}

export function CopyButton({
  value,
  label = "Copy to clipboard",
  className,
}: CopyButtonProps) {
  const { copied, copy } = useCopy(value);

  return (
    <>
      <Tooltip content={copied ? "Copied" : "Copy to clipboard"}>
        <IconButton
          aria-label={label}
          variant="ghost"
          size="sm"
          className={cx("cv-copy-button", className)}
          onPress={copy}
        >
          {copied ? <Check /> : <SquareTwoStack />}
        </IconButton>
      </Tooltip>
      <span className="cv-sr-only" role="status">
        {copied ? "Copied" : ""}
      </span>
    </>
  );
}

export interface CopyableTextProps {
  value: string;
  /** Displayed text. Defaults to `value`. */
  children?: ReactNode;
  /**
   * `field` is the monospace identifier chip used in detail views.
   * `inline` is the pressable chip used inside sentences.
   */
  variant?: "field" | "inline";
  label?: string;
  className?: string;
  /** Detail views can scroll a complete identifier on one line. */
  overflow?: "truncate" | "scroll";
}

export function CopyableText({
  value,
  children,
  variant = "field",
  label,
  className,
  overflow = "truncate",
}: CopyableTextProps) {
  const { copied, copy } = useCopy(value);
  const accessibleLabel = label ?? `Copy ${value}`;

  if (variant === "inline") {
    return (
      <>
        <Tooltip content={copied ? "Copied" : "Copy to clipboard"}>
          <AriaButton
            aria-label={accessibleLabel}
            className={cx("cv-copyable", "cv-copyable--inline", className)}
            onPress={copy}
          >
            <span className="cv-copyable__text">{children ?? value}</span>
            <span className="cv-copyable__icon" aria-hidden="true">
              {copied ? <Check /> : <SquareTwoStack />}
            </span>
          </AriaButton>
        </Tooltip>
        <span className="cv-sr-only" role="status">
          {copied ? "Copied" : ""}
        </span>
      </>
    );
  }

  return (
    <span
      className={cx(
        "cv-copyable",
        "cv-copyable--field",
        overflow === "scroll" && "cv-copyable--scroll",
        className,
      )}
    >
      <span
        className="cv-copyable__text"
        {...(overflow === "scroll"
          ? {
              tabIndex: 0,
              role: "region",
              "aria-label": label?.replace(/^Copy /i, "") ?? value,
            }
          : {})}
      >
        {children ?? value}
      </span>
      <CopyButton value={value} label={accessibleLabel} />
    </span>
  );
}
