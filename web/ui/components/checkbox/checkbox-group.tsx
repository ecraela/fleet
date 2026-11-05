import { useId, type ReactNode } from "react";
import {
  CheckboxGroup as AriaCheckboxGroup,
  Label,
  Text,
  type CheckboxGroupProps as AriaCheckboxGroupProps,
} from "react-aria-components";
import { cx } from "../../utils/cx";
import { Checkbox } from "./checkbox";

export interface CheckboxGroupOption {
  id: string;
  label: string;
}

export interface CheckboxGroupSection {
  id: string;
  title: string;
  options: readonly CheckboxGroupOption[];
  /** A full-width row below the columns, e.g. plan-restricted choices. */
  isFooter?: boolean;
  /** Short muted note after the title, e.g. "Enterprise plan only". */
  note?: string;
}

export interface CheckboxGroupProps extends Omit<
  AriaCheckboxGroupProps,
  "children" | "className" | "style" | "value" | "defaultValue" | "onChange"
> {
  label: ReactNode;
  description?: ReactNode;
  sections: readonly CheckboxGroupSection[];
  value: readonly string[];
  onChange: (value: string[]) => void;
  /**
   * Options already included by a broader choice (for example every region
   * when "All regions" is selected). They show as selected and disabled and
   * are never added to `value`.
   */
  coveredValues?: readonly string[];
  className?: string;
}

/**
 * Many related checkboxes, grouped under titles in aligned columns: one
 * column per section on wide screens, two on tablets, one on phones.
 */
export function CheckboxGroup({
  label,
  description,
  sections,
  value,
  onChange,
  coveredValues = [],
  className,
  ...props
}: CheckboxGroupProps) {
  const id = useId();
  const covered = new Set(coveredValues);
  const columns = sections.filter((section) => !section.isFooter);
  const footers = sections.filter((section) => section.isFooter);
  const renderSection = (section: CheckboxGroupSection) => (
    <div
      key={section.id}
      role="group"
      aria-labelledby={`${id}-${section.id}`}
      className={cx(
        "cv-checkbox-group__section",
        section.isFooter && "cv-checkbox-group__section--footer",
      )}
    >
      <span id={`${id}-${section.id}`} className="cv-checkbox-group__title">
        {section.title}
        {section.note && (
          <span className="cv-checkbox-group__note"> – {section.note}</span>
        )}
      </span>
      <div className="cv-checkbox-group__options">
        {section.options.map((option) => (
          <Checkbox
            key={option.id}
            value={option.id}
            isDisabled={covered.has(option.id)}
          >
            {option.label}
          </Checkbox>
        ))}
      </div>
    </div>
  );
  return (
    <AriaCheckboxGroup
      {...props}
      value={[...new Set([...value, ...covered])]}
      onChange={(next) => onChange(next.filter((v) => !covered.has(v)))}
      className={cx("cv-checkbox-group", className)}
    >
      <Label className="cv-field__label">{label}</Label>
      {description != null && (
        <Text slot="description" className="cv-checkbox-group__description">
          {description}
        </Text>
      )}
      <div
        className="cv-checkbox-group__columns"
        style={{ ["--cv-checkbox-group-columns" as string]: columns.length }}
      >
        {columns.map(renderSection)}
      </div>
      {footers.map(renderSection)}
    </AriaCheckboxGroup>
  );
}
