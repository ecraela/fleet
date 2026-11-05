import type { ReactNode } from "react";
import { cx } from "../../utils/cx";

export interface KbdProps {
  children: ReactNode;
  className?: string;
}

/** Keyboard shortcut chip. Adapts its colors to the button it sits in. */
export function Kbd({ children, className }: KbdProps) {
  return <kbd className={cx("cv-kbd", className)}>{children}</kbd>;
}
