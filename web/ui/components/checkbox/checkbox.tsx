import { Check, Minus } from "../../icons";
import { useId, type ReactNode } from "react";
import {
  Checkbox as AriaCheckbox,
  type CheckboxProps as AriaCheckboxProps,
} from "react-aria-components";
import { cx } from "../../utils/cx";

export interface CheckboxProps extends Omit<
  AriaCheckboxProps,
  "children" | "className" | "style"
> {
  children?: ReactNode;
  /** Helper text below the label. May contain links. */
  description?: ReactNode;
  className?: string;
}

export function Checkbox({
  children,
  description,
  className,
  ...props
}: CheckboxProps) {
  const descriptionId = useId();

  const checkbox = (
    <AriaCheckbox
      {...props}
      {...(description != null ? { "aria-describedby": descriptionId } : {})}
      className={cx("cv-checkbox", className)}
    >
      {({ isSelected, isIndeterminate }) => (
        <>
          <span className="cv-checkbox__box" aria-hidden="true">
            {isIndeterminate ? <Minus /> : isSelected ? <Check /> : null}
          </span>
          {children != null ? (
            <span className="cv-checkbox__label">{children}</span>
          ) : null}
        </>
      )}
    </AriaCheckbox>
  );

  if (description == null) return checkbox;

  return (
    <div className="cv-checkbox-field">
      {checkbox}
      <p id={descriptionId} className="cv-checkbox__description">
        {description}
      </p>
    </div>
  );
}
