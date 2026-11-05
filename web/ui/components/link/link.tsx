import { ArrowUpRightMini } from "../../icons";
import type { ReactNode } from "react";
import {
  Link as AriaLink,
  type LinkProps as AriaLinkProps,
} from "react-aria-components";
import { cx } from "../../utils/cx";

export interface LinkProps extends Omit<
  AriaLinkProps,
  "children" | "className" | "style"
> {
  children: ReactNode;
  className?: string;
  /** `dashed` is the default; `hover` reveals a blue underline on hover. */
  variant?: "dashed" | "plain" | "hover";
  /** Opens in a new tab and appends an arrow. */
  isExternal?: boolean;
}

export function Link({
  children,
  className,
  variant = "dashed",
  isExternal = false,
  ...props
}: LinkProps) {
  return (
    <AriaLink
      {...props}
      {...(isExternal ? { target: "_blank", rel: "noopener noreferrer" } : {})}
      className={cx("cv-link", `cv-link--${variant}`, className)}
    >
      {children}
      {isExternal ? (
        <>
          <ArrowUpRightMini className="cv-link__external" aria-hidden="true" />
          <span className="cv-sr-only"> (opens in a new tab)</span>
        </>
      ) : null}
    </AriaLink>
  );
}
