import type { CSSProperties, ReactNode } from "react";
import { cx } from "../../utils/cx";
import { Check } from "../../icons";

export interface SkeletonProps {
  width?: CSSProperties["width"];
  height?: CSSProperties["height"];
  className?: string;
}

export function Skeleton({
  width = "100%",
  height = 8,
  className,
}: SkeletonProps) {
  return (
    <span
      aria-hidden="true"
      className={cx("cv-skeleton", className)}
      style={{ width, height }}
    />
  );
}

export interface EmptyStateProps {
  title: ReactNode;
  description?: ReactNode;
  icon?: ReactNode;
  actions?: ReactNode;
  /** Compact, left-aligned guidance inside an existing section. */
  size?: "md" | "sm";
  className?: string;
}

export function EmptyState({
  title,
  description,
  icon,
  actions,
  size = "md",
  className,
}: EmptyStateProps) {
  return (
    <div className={cx("cv-empty-state", `cv-empty-state--${size}`, className)}>
      {icon != null ? <div className="cv-empty-state__icon">{icon}</div> : null}
      <h3 className="cv-empty-state__title">{title}</h3>
      {description != null ? (
        <p className="cv-empty-state__description">{description}</p>
      ) : null}
      {actions != null ? (
        <div className="cv-empty-state__actions">{actions}</div>
      ) : null}
    </div>
  );
}

export type IconTileTone = "neutral" | "success" | "warning" | "error";

/** Compact confirmation for a comparison with no differences. */
export function SyncState({
  title,
  description,
}: Pick<EmptyStateProps, "title" | "description">) {
  return (
    <div className="cv-sync-state">
      <Check size={20} />
      <div className="cv-sync-state__content">
        <h3 className="cv-sync-state__title">{title}</h3>
        {description != null ? (
          <p className="cv-sync-state__description">{description}</p>
        ) : null}
      </div>
    </div>
  );
}

export interface IconTileProps {
  children: ReactNode;
  tone?: IconTileTone;
  size?: "sm" | "lg";
  className?: string;
}

/**
 * Tinted resource icon used beside table names and on detail headers.
 */
export function IconTile({
  children,
  tone = "neutral",
  size = "sm",
  className,
}: IconTileProps) {
  return (
    <span
      aria-hidden="true"
      className={cx(
        "cv-icon-tile",
        `cv-icon-tile--${tone}`,
        `cv-icon-tile--${size}`,
        className,
      )}
    >
      <span className="cv-icon-tile__glyph">{children}</span>
    </span>
  );
}
