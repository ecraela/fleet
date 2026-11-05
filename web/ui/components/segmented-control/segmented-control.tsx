import type { ReactNode } from "react";
import {
  ToggleButton,
  ToggleButtonGroup,
  type Key,
} from "react-aria-components";
import { cx } from "../../utils/cx";

export interface SegmentedControlOption {
  id: Key;
  label: string;
  /** Leading visual. Decorative: the label carries the meaning. */
  icon?: ReactNode;
  /** Trailing content such as a count badge. */
  trailing?: ReactNode;
}

export interface SegmentedControlProps {
  "aria-label": string;
  options: readonly SegmentedControlOption[];
  selectedKey: Key;
  onSelectionChange: (key: Key) => void;
  size?: "sm" | "md";
  isDisabled?: boolean;
  className?: string;
}

/**
 * A single-choice, text-labelled toggle group, e.g. Split / Unified, or the
 * views of one resource.
 */
export function SegmentedControl({
  "aria-label": ariaLabel,
  options,
  selectedKey,
  onSelectionChange,
  size = "md",
  isDisabled = false,
  className,
}: SegmentedControlProps) {
  return (
    <ToggleButtonGroup
      aria-label={ariaLabel}
      selectionMode="single"
      disallowEmptySelection
      isDisabled={isDisabled}
      selectedKeys={[selectedKey]}
      onSelectionChange={(keys) => {
        const [key] = Array.from(keys);
        if (key != null) onSelectionChange(key);
      }}
      className={cx("cv-segmented", `cv-segmented--${size}`, className)}
    >
      {options.map((option) => (
        <ToggleButton
          key={option.id}
          id={option.id}
          className="cv-segmented__item"
        >
          {option.icon != null ? (
            <span className="cv-segmented__icon" aria-hidden="true">
              {option.icon}
            </span>
          ) : null}
          {option.label}
          {option.trailing}
        </ToggleButton>
      ))}
    </ToggleButtonGroup>
  );
}
