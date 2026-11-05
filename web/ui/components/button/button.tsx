import { useId, useRef, type ReactNode, type Ref } from "react";
import {
  Button as AriaButton,
  Focusable,
  type ButtonProps as AriaButtonProps,
} from "react-aria-components";
import { cx } from "../../utils/cx";
import { Kbd } from "../kbd/kbd";
import { Tooltip } from "../tooltip/tooltip";
import { isApplePlatform, useShortcut } from "./use-shortcut";

export type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";
export type ButtonSize = "sm" | "md" | "lg";

export interface ButtonProps extends Omit<
  AriaButtonProps,
  "children" | "className" | "style"
> {
  children?: ReactNode;
  className?: string;
  ref?: Ref<HTMLButtonElement>;
  /** Legacy console prop; React Aria filters this native attribute. */
  title?: string;
  /** Visual emphasis. Keep a single `primary` action per surface. */
  variant?: ButtonVariant;
  size?: ButtonSize;
  leadingIcon?: ReactNode;
  trailingIcon?: ReactNode;
  /**
   * Keyboard hint rendered as chips after the label, e.g. `["⌘", "↩︎"]`.
   * Hints for ⌘/Ctrl + Enter and ⌫ are live: pressing them clicks the button
   * while it is enabled and not behind an overlay. `Esc` is left to the
   * dialog, which already closes on Escape. `⌘` renders as `Ctrl` outside
   * Apple platforms.
   */
  shortcut?: readonly string[];
  /** Replaces the content with loading dots without changing the width. */
  isLoading?: boolean;
  /** Disables the action and explains why on hover, keyboard focus and to assistive technology. */
  disabledReason?: string | undefined;
}

export function Button({
  children,
  className,
  ref,
  variant = "secondary",
  size = "md",
  leadingIcon,
  trailingIcon,
  shortcut,
  isLoading = false,
  disabledReason,
  ...props
}: ButtonProps) {
  const hasShortcut = shortcut !== undefined && shortcut.length > 0;
  const buttonRef = useRef<HTMLButtonElement>(null);
  const disabledId = useId();
  const buttonId = props.id ?? `${disabledId}-button`;
  useShortcut(
    buttonRef,
    shortcut,
    props.isDisabled === true || isLoading || Boolean(disabledReason),
  );

  const button = (
    <AriaButton
      {...props}
      {...(disabledReason ? { id: buttonId, isDisabled: true } : {})}
      ref={mergeRefs(buttonRef, ref)}
      isPending={isLoading}
      className={cx(
        "cv-button",
        `cv-button--${variant}`,
        `cv-button--${size}`,
        leadingIcon != null && "cv-button--leading-icon",
        hasShortcut && "cv-button--shortcut",
        className,
      )}
    >
      <span className="cv-button__content">
        {leadingIcon != null ? (
          <span className="cv-button__icon" aria-hidden="true">
            {leadingIcon}
          </span>
        ) : null}
        {children}
        {trailingIcon != null ? (
          <span className="cv-button__icon" aria-hidden="true">
            {trailingIcon}
          </span>
        ) : null}
        {hasShortcut ? (
          <span className="cv-button__shortcut" aria-hidden="true">
            {shortcut.map((key) => (
              <Kbd key={key}>{displayKey(key)}</Kbd>
            ))}
          </span>
        ) : null}
      </span>
      {isLoading ? (
        <span className="cv-button__loader" aria-hidden="true">
          <span />
          <span />
          <span />
        </span>
      ) : null}
    </AriaButton>
  );

  if (!disabledReason) return button;
  // The action remains natively disabled; its focusable wrapper exposes the explanation.
  return (
    <Tooltip content={disabledReason}>
      <Focusable>
        <span
          className="cv-button-disabled-trigger"
          tabIndex={0}
          role="button"
          aria-disabled="true"
          aria-labelledby={buttonId}
          aria-describedby={`${disabledId}-reason`}
        >
          <span aria-hidden="true">{button}</span>
          <span className="cv-sr-only" id={`${disabledId}-reason`}>
            {disabledReason}
          </span>
        </span>
      </Focusable>
    </Tooltip>
  );
}

function displayKey(key: string) {
  return key === "⌘" && !isApplePlatform ? "Ctrl" : key;
}

function mergeRefs<T>(...refs: (Ref<T> | undefined)[]): Ref<T> {
  return (value) => {
    for (const ref of refs) {
      if (typeof ref === "function") ref(value);
      else if (ref != null) ref.current = value;
    }
  };
}

export interface IconButtonProps extends Omit<
  ButtonProps,
  "leadingIcon" | "trailingIcon" | "shortcut" | "children"
> {
  /** Icon buttons have no visible text, so a label is mandatory. */
  "aria-label": string;
  children: ReactNode;
}

export function IconButton({
  children,
  className,
  variant = "secondary",
  ...props
}: IconButtonProps) {
  return (
    <Button
      {...props}
      variant={variant}
      className={cx("cv-button--icon-only", className)}
    >
      <span className="cv-button__icon" aria-hidden="true">
        {children}
      </span>
    </Button>
  );
}
