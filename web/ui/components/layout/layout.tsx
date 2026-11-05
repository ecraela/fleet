import type { ReactNode } from "react";
import { cx } from "../../utils/cx";

export interface PageHeaderProps {
  title: ReactNode;
  /** Small label above a detail title, e.g. "Change". */
  eyebrow?: ReactNode;
  description?: ReactNode;
  /** Usually a large `IconTile`. */
  icon?: ReactNode;
  actions?: ReactNode;
  className?: string;
}

/**
 * Collection pages use the display title. Supplying an `eyebrow` switches to
 * the compact detail title used on single-resource pages.
 */
export function PageHeader({
  title,
  eyebrow,
  description,
  icon,
  actions,
  className,
}: PageHeaderProps) {
  return (
    <header
      className={cx(
        "cv-page-header",
        eyebrow != null && "cv-page-header--detail",
        className,
      )}
    >
      <div className="cv-page-header__main">
        {icon != null ? (
          <div className="cv-page-header__icon">{icon}</div>
        ) : null}
        <div className="cv-page-header__text">
          {eyebrow != null ? (
            <p className="cv-page-header__eyebrow">{eyebrow}</p>
          ) : null}
          <h1 className="cv-page-header__title">{title}</h1>
          {description != null ? (
            <p className="cv-page-header__description">{description}</p>
          ) : null}
        </div>
      </div>
      {actions != null ? (
        <div className="cv-page-header__actions">{actions}</div>
      ) : null}
    </header>
  );
}

export interface ToolbarProps {
  children: ReactNode;
  "aria-label"?: string;
  className?: string;
}

/** Filter row above a table: search grows 2×, selects 1×, icon buttons fixed. */
export function Toolbar({ children, className, ...props }: ToolbarProps) {
  return (
    <div role="group" {...props} className={cx("cv-toolbar", className)}>
      {children}
    </div>
  );
}

export interface DescriptionListProps {
  children: ReactNode;
  /**
   * `stacked` puts uppercase labels above values in a grid (detail pages).
   * `inline` puts labels beside values in one column (expanded rows, panels).
   */
  layout?: "stacked" | "inline";
  /** Grid columns for the stacked layout. */
  columns?: 1 | 2 | 3 | 4;
  className?: string;
}

export function DescriptionList({
  children,
  layout = "stacked",
  columns = 3,
  className,
}: DescriptionListProps) {
  return (
    <dl
      className={cx(
        "cv-description-list",
        `cv-description-list--${layout}`,
        layout === "stacked" && `cv-description-list--${columns}`,
        className,
      )}
    >
      {children}
    </dl>
  );
}

export interface DescriptionItemProps {
  label: ReactNode;
  children: ReactNode;
  /** Right-aligned secondary text such as a timestamp. */
  aside?: ReactNode;
  className?: string;
}

export function DescriptionItem({
  label,
  children,
  aside,
  className,
}: DescriptionItemProps) {
  return (
    <div className={cx("cv-description-item", className)}>
      <dt className="cv-description-item__label">{label}</dt>
      <dd className="cv-description-item__value">
        {children}
        {aside != null ? (
          <span className="cv-description-item__aside">{aside}</span>
        ) : null}
      </dd>
    </div>
  );
}

export interface InlineMetaProps {
  items: ReadonlyArray<readonly [label: ReactNode, value: ReactNode]>;
  className?: string;
}

/** Short label/value pairs on one line, e.g. `TTL 300 s  Proxy DNS only`. */
export function InlineMeta({ items, className }: InlineMetaProps) {
  return (
    <span className={cx("cv-inline-meta", className)}>
      {items.map(([label, value], index) => (
        <span key={index} className="cv-inline-meta__item">
          <span className="cv-inline-meta__label">{label}</span>
          <span className="cv-inline-meta__value">{value}</span>
        </span>
      ))}
    </span>
  );
}

export interface SectionProps {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  /** `sm` is a divided subsection for panels and expanded rows. */
  size?: "md" | "sm";
  className?: string;
  /** Space notices, fields and tables vertically without adding page margins. */
  layout?: "flow" | "stack";
}

export function Section({
  title,
  description,
  actions,
  children,
  size = "md",
  className,
  layout = "flow",
}: SectionProps) {
  const Heading = size === "sm" ? "h3" : "h2";
  return (
    <section
      className={cx(
        "cv-section",
        `cv-section--${size}`,
        layout === "stack" && "cv-section--stack",
        className,
      )}
    >
      <div className="cv-section__header">
        <Heading className="cv-section__title">{title}</Heading>
        {actions != null ? (
          <div className="cv-section__actions">{actions}</div>
        ) : null}
      </div>
      {description != null && (
        <p className="cv-section__description">{description}</p>
      )}
      {layout === "stack" ? (
        <div className="cv-section__content">{children}</div>
      ) : (
        children
      )}
    </section>
  );
}
