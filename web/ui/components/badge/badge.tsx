import type { ReactNode } from "react";
import { cx } from "../../utils/cx";

export type BadgeTone = "neutral" | "success" | "error" | "warning" | "info";

export interface BadgeProps {
  children: ReactNode;
  tone?: BadgeTone;
  icon?: ReactNode;
  className?: string;
}

/** Status label. Always carries text so state never relies on color alone. */
export function Badge({
  children,
  tone = "neutral",
  icon,
  className,
}: BadgeProps) {
  return (
    <span className={cx("cv-badge", `cv-badge--${tone}`, className)}>
      {icon != null ? (
        <span className="cv-badge__icon" aria-hidden="true">
          {icon}
        </span>
      ) : null}
      {children}
    </span>
  );
}

export type StatusDotTone = BadgeTone | "all";

export function StatusDot({
  tone,
  className,
}: {
  tone: StatusDotTone;
  className?: string;
}) {
  return (
    <span
      aria-hidden="true"
      className={cx("cv-status-dot", `cv-status-dot--${tone}`, className)}
    />
  );
}
