import { Check, ChevronDown } from "../../icons";
import type { ReactNode } from "react";
import {
  Button,
  FieldError,
  Header,
  Label,
  ListBox,
  ListBoxItem,
  ListBoxSection,
  Popover,
  Select as AriaSelect,
  SelectValue,
  Text,
  type Key,
  type SelectProps as AriaSelectProps,
} from "react-aria-components";
import { cx } from "../../utils/cx";
import type { FieldErrorMessage } from "../text-field/text-field";

/** Every dropdown list shares this height (about eight rows) and scrolls inside. */
export const LIST_MAX_HEIGHT = 320;

export interface SelectOption {
  id: Key;
  label: string;
  /** Leading visual such as a `StatusDot`, flag, or provider mark. */
  icon?: ReactNode;
  /** Secondary text rendered after the label, e.g. a region code. */
  detail?: string;
  /** Supporting instructions that wrap beneath an option's label. */
  description?: string | undefined;
  isDisabled?: boolean;
}

export interface SelectOptionGroup {
  id: Key;
  title?: string;
  options: readonly SelectOption[];
}

export interface SelectProps extends Omit<
  AriaSelectProps<SelectOption>,
  "children" | "className" | "style" | "items"
> {
  /** Options, optionally grouped. Groups are separated by a divider. */
  options: readonly (SelectOption | SelectOptionGroup)[];
  label?: ReactNode;
  hideLabel?: boolean;
  description?: ReactNode;
  errorMessage?: FieldErrorMessage;
  /** `field` fills its container; `inline` is the text trigger for footers. */
  variant?: "field" | "inline";
  className?: string;
}

export function Select({
  options,
  label,
  hideLabel = false,
  description,
  errorMessage,
  variant = "field",
  className,
  ...props
}: SelectProps) {
  const flatOptions = options.flatMap((item) =>
    "options" in item ? item.options : [item],
  );

  return (
    <AriaSelect
      {...props}
      className={cx(
        "cv-field",
        "cv-select",
        `cv-select--${variant}`,
        className,
      )}
    >
      {label != null ? (
        <Label className={hideLabel ? "cv-sr-only" : "cv-field__label"}>
          {label}
        </Label>
      ) : null}
      <Button className="cv-select__trigger">
        <SelectValue className="cv-select__value">
          {({ isPlaceholder, selectedText }) => {
            const option = isPlaceholder
              ? undefined
              : flatOptions.find(
                  (candidate) => candidate.label === selectedText,
                );
            return (
              <>
                {option?.icon != null ? (
                  <span className="cv-select__icon" aria-hidden="true">
                    {option.icon}
                  </span>
                ) : null}
                <span
                  className="cv-select__text"
                  title={isPlaceholder ? undefined : selectedText}
                >
                  {isPlaceholder
                    ? (props.placeholder ?? "Select an option")
                    : selectedText}
                </span>
              </>
            );
          }}
        </SelectValue>
        <ChevronDown className="cv-select__chevron" aria-hidden="true" />
      </Button>
      {description != null ? (
        <Text slot="description" className="cv-field__description">
          {description}
        </Text>
      ) : null}
      <FieldError className="cv-field__error">{errorMessage}</FieldError>
      <Popover
        offset={variant === "inline" ? 6 : 8}
        maxHeight={LIST_MAX_HEIGHT}
        className={cx(
          "cv-popover",
          "cv-select__popover",
          `cv-select__popover--${variant}`,
        )}
      >
        <ListBox className="cv-listbox">
          {options.map((item) =>
            "options" in item ? (
              <ListBoxSection
                key={item.id}
                id={item.id}
                className="cv-listbox__section"
              >
                {item.title != null ? (
                  <Header className="cv-listbox__header">{item.title}</Header>
                ) : null}
                {item.options.map(renderOption)}
              </ListBoxSection>
            ) : (
              renderOption(item)
            ),
          )}
        </ListBox>
      </Popover>
    </AriaSelect>
  );
}

function renderOption(option: SelectOption) {
  return (
    <ListBoxItem
      key={option.id}
      id={option.id}
      textValue={option.label}
      isDisabled={option.isDisabled ?? false}
      className="cv-listbox__item"
    >
      {({ isSelected }) => (
        <>
          <span className="cv-listbox__main">
            {option.icon != null ? (
              <span className="cv-listbox__icon" aria-hidden="true">
                {option.icon}
              </span>
            ) : null}
            <span className="cv-listbox__copy">
              <span className="cv-listbox__heading">
                <Text slot="label" className="cv-listbox__label">
                  {option.label}
                </Text>
                {option.detail != null ? (
                  <Text slot="description" className="cv-listbox__detail">
                    {option.detail}
                  </Text>
                ) : null}
              </span>
              {option.description != null ? (
                <Text slot="description" className="cv-listbox__description">
                  {option.description}
                </Text>
              ) : null}
            </span>
          </span>
          <span className="cv-listbox__check" aria-hidden="true">
            {isSelected ? <Check /> : null}
          </span>
        </>
      )}
    </ListBoxItem>
  );
}
