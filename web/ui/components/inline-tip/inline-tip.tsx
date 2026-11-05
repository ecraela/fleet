import type { ReactNode } from "react";
import { cx } from "../../utils/cx";

export type InlineTipVariant = "info" | "success" | "warning" | "error";

export interface InlineTipProps {
  /** @default "info" */
  variant?: InlineTipVariant;
  /**
   * Bold text before the colon. Defaults to "Tip", "Success", "Warning", or
   * "Error" to match `variant`; pass a custom label (e.g. "Note") to override.
   */
  label?: string;
  /** A standalone title replaces the inline label for longer, actionable feedback. */
  title?: ReactNode;
  /** Actions follow the message on their own row. */
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
  /** Announce dynamic feedback; omit for static guidance. */
  role?: "alert" | "status";
}

const defaultLabel: Record<InlineTipVariant, string> = {
  info: "Tip",
  success: "Success",
  warning: "Warning",
  error: "Error",
};

/**
 * Inline guidance or feedback. Place action outcomes at the top of the
 * affected section or inside the dialog that triggered them
 * (see DESIGN_SYSTEM.md's "Feedback and messaging" — there is no Toast);
 * for a table or list row's own state, use `Badge`.
 */
export function InlineTip({
  variant = "info",
  label,
  title,
  actions,
  children,
  className,
  role,
}: InlineTipProps) {
  return (
    <div
      role={role}
      className={cx(
        "cv-inline-tip",
        `cv-inline-tip--${variant}`,
        title != null && "cv-inline-tip--structured",
        className,
      )}
    >
      <span className="cv-inline-tip__bar" aria-hidden="true" />
      <div className="cv-inline-tip__text">
        {title != null ? (
          <>
            <strong className="cv-inline-tip__title">{title}</strong>
            <div className="cv-inline-tip__message">{children}</div>
          </>
        ) : (
          <>
            <span className="cv-inline-tip__label">
              {label ?? defaultLabel[variant]}:
            </span>{" "}
            {children}
          </>
        )}
        {actions && <div className="cv-inline-tip__actions">{actions}</div>}
      </div>
    </div>
  );
}
