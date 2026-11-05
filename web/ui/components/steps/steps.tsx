import { Check } from "../../icons";
import type { ReactNode } from "react";
import { cx } from "../../utils/cx";

export type StepStatus = "complete" | "current" | "upcoming";

export interface StepsProps {
  children: ReactNode;
  className?: string;
  density?: "comfortable" | "compact";
}

/** Vertical creation flow with a connecting rail, as on "Add domain". */
export function Steps({
  children,
  className,
  density = "comfortable",
}: StepsProps) {
  return (
    <ol
      className={cx(
        "cv-steps",
        density === "compact" && "cv-steps--compact",
        className,
      )}
    >
      {children}
    </ol>
  );
}

export interface StepProps {
  title: ReactNode;
  description?: ReactNode;
  status?: StepStatus;
  children?: ReactNode;
  className?: string;
}

export function Step({
  title,
  description,
  status = "upcoming",
  children,
  className,
}: StepProps) {
  return (
    <li
      className={cx("cv-step", className)}
      data-status={status}
      aria-current={status === "current" ? "step" : undefined}
    >
      <span className="cv-step__marker" aria-hidden="true">
        {status === "complete" ? <Check /> : null}
      </span>
      <div className="cv-step__body">
        <h2 className="cv-step__title">
          {title}
          {status === "complete" ? (
            <span className="cv-sr-only"> (complete)</span>
          ) : null}
        </h2>
        {description != null ? (
          <p className="cv-step__description">{description}</p>
        ) : null}
        {children != null && status !== "upcoming" ? (
          <div className="cv-step__content">{children}</div>
        ) : null}
      </div>
    </li>
  );
}
