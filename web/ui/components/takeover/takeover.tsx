import { XMark } from "../../icons";
import type { ReactNode } from "react";
import { cx } from "../../utils/cx";
import { IconButton } from "../button/button";

export interface TakeoverProps {
  /** Product mark in the top bar. */
  brand: ReactNode;
  /** Bordered sign-in card or the default full-page task. */
  variant?: "default" | "card";
  icon?: ReactNode;
  headerActions?: ReactNode;
  /** Optional badge beside the brand, e.g. the plan a feature needs. */
  badge?: ReactNode;
  onClose?: () => void;
  closeLabel?: string;
  title: ReactNode;
  description?: ReactNode;
  children: ReactNode;
  /** Right-aligned actions in the bottom bar. */
  footer?: ReactNode;
  className?: string;
}

/**
 * Full-screen, single-task page such as "Create organization". The content
 * column is centered; the primary action lives in the bottom bar.
 */
export function Takeover({
  brand,
  variant = "default",
  icon,
  headerActions,
  badge,
  onClose,
  closeLabel = "Close",
  title,
  description,
  children,
  footer,
  className,
}: TakeoverProps) {
  return (
    <div
      className={cx(
        "cv-takeover",
        variant === "card" && "cv-takeover--card",
        className,
      )}
    >
      <header className="cv-takeover__bar">
        <div className="cv-takeover__brand">
          {brand}
          {badge}
        </div>
        {headerActions}
        {onClose ? (
          <IconButton aria-label={closeLabel} onPress={onClose}>
            <XMark />
          </IconButton>
        ) : null}
      </header>
      <main className="cv-takeover__main">
        <div className="cv-takeover__content">
          <div className="cv-takeover__heading">
            {icon != null ? (
              <div className="cv-takeover__icon">{icon}</div>
            ) : null}
            <div>
              <h1 className="cv-takeover__title">{title}</h1>
              {description != null ? (
                <p className="cv-takeover__description">{description}</p>
              ) : null}
            </div>
          </div>
          <div className="cv-takeover__body">{children}</div>
        </div>
      </main>
      {footer != null ? (
        <footer className="cv-takeover__footer">{footer}</footer>
      ) : null}
    </div>
  );
}
