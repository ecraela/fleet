import { ExclamationCircle, MagnifyingGlass } from "../../icons";
import { useContext, useRef, useState, type ReactNode } from "react";
import {
  FieldError,
  FieldErrorContext,
  FormContext,
  Input,
  Label,
  SearchField as AriaSearchField,
  Text,
  TextArea as AriaTextArea,
  TextField as AriaTextField,
  Tooltip as AriaTooltip,
  TooltipTrigger,
  useSlottedContext,
  type SearchFieldProps as AriaSearchFieldProps,
  type TextFieldProps as AriaTextFieldProps,
  type ValidationResult,
} from "react-aria-components";
import { cx } from "../../utils/cx";

export type FieldErrorMessage =
  ReactNode | ((validation: ValidationResult) => ReactNode);

export interface TextFieldProps extends Omit<
  AriaTextFieldProps,
  "children" | "className" | "style" | "spellCheck"
> {
  label?: ReactNode;
  /** Keep an accessible label in compact toolbars and table cells. */
  hideLabel?: boolean;
  description?: ReactNode;
  errorMessage?: FieldErrorMessage;
  /**
   * In `tooltip` mode, a warning icon appears inside the input and the
   * message shows while the field is focused or the icon is hovered. The
   * message is always announced through the input's description.
   */
  errorDisplay?: "tooltip" | "inline";
  placeholder?: string;
  spellCheck?: boolean;
  leadingIcon?: ReactNode;
  /** Shown while the field is valid, e.g. a hint icon. */
  trailingIcon?: ReactNode;
  /**
   * Static text inside the control after the value, e.g. a unit ("seconds")
   * or the fixed part of a name (".example.com").
   */
  suffix?: ReactNode;
  className?: string;
}

export function TextField({
  label,
  hideLabel = false,
  description,
  errorMessage: providedErrorMessage,
  errorDisplay = "tooltip",
  placeholder,
  spellCheck,
  leadingIcon,
  trailingIcon,
  suffix,
  className,
  onFocusChange,
  ...props
}: TextFieldProps) {
  const [isFocused, setFocused] = useState(false);
  const [hasSubmittedError, setSubmittedError] = useState(false);
  const form = useSlottedContext(FormContext);
  const showValidation =
    hasSubmittedError ||
    props.isInvalid === true ||
    (props.validationBehavior ?? form?.validationBehavior) === "aria";
  const errorMessage =
    providedErrorMessage === "" ? undefined : providedErrorMessage;

  return (
    <AriaTextField
      {...props}
      onFocusChange={(focused) => {
        setFocused(focused);
        onFocusChange?.(focused);
      }}
      className={cx("cv-field", className)}
    >
      {label != null ? (
        <Label className={hideLabel ? "cv-sr-only" : "cv-field__label"}>
          {label}
        </Label>
      ) : null}
      <FieldControl
        leadingIcon={
          leadingIcon ??
          (props.type === "search" ? (
            <MagnifyingGlass aria-hidden="true" />
          ) : undefined)
        }
        trailingIcon={trailingIcon}
        suffix={suffix}
        errorMessage={errorMessage}
        errorDisplay={errorDisplay}
        isFocused={isFocused}
        showValidation={showValidation}
      >
        <Input
          className="cv-input"
          // React Aria also validates on blur. Keep feedback quiet until an
          // invalid submission, while preserving native submission blocking.
          {...(!showValidation ? { "aria-invalid": false } : {})}
          onInvalid={() => setSubmittedError(true)}
          {...(spellCheck !== undefined ? { spellCheck } : {})}
          {...(placeholder !== undefined ? { placeholder } : {})}
        />
      </FieldControl>
      {description != null ? (
        <Text slot="description" className="cv-field__description">
          {description}
        </Text>
      ) : null}
      {showValidation && (
        <FieldError
          className={
            errorDisplay === "inline" ? "cv-field__error" : "cv-sr-only"
          }
        >
          {errorMessage}
        </FieldError>
      )}
    </AriaTextField>
  );
}

function FieldControl({
  children,
  leadingIcon,
  trailingIcon,
  suffix,
  errorMessage,
  errorDisplay,
  isFocused,
  showValidation,
}: {
  children: ReactNode;
  leadingIcon: ReactNode;
  trailingIcon: ReactNode;
  suffix: ReactNode;
  errorMessage: FieldErrorMessage;
  errorDisplay: "tooltip" | "inline";
  isFocused: boolean;
  showValidation: boolean;
}) {
  const validation = useContext(FieldErrorContext);
  const statusRef = useRef<HTMLSpanElement>(null);
  const [isHovered, setHovered] = useState(false);
  const showStatus =
    showValidation && errorDisplay === "tooltip" && validation?.isInvalid;
  const message =
    typeof errorMessage === "function"
      ? validation && errorMessage(validation)
      : (errorMessage ?? validation?.validationErrors.join(" "));
  const hasTrailing = Boolean(showStatus) || trailingIcon != null;

  return (
    <div
      className="cv-field__control"
      data-leading={leadingIcon != null || undefined}
      data-trailing={hasTrailing || undefined}
      data-suffix={suffix != null || undefined}
    >
      {leadingIcon != null ? (
        <span className="cv-field__slot cv-field__slot--leading">
          {leadingIcon}
        </span>
      ) : null}
      {children}
      {suffix != null ? (
        <span className="cv-field__suffix">{suffix}</span>
      ) : null}
      {showStatus ? (
        // React Aria tooltips need trigger state from `TooltipTrigger`. The
        // icon is not focusable, so the trigger is controlled and the tooltip
        // is anchored explicitly; the input's description carries the message.
        <TooltipTrigger isOpen={Boolean(message) && (isFocused || isHovered)}>
          <span
            ref={statusRef}
            className="cv-field__slot cv-field__slot--trailing cv-field__status"
            data-active={isFocused || isHovered || undefined}
            onPointerEnter={() => setHovered(true)}
            onPointerLeave={() => setHovered(false)}
          >
            <ExclamationCircle aria-hidden="true" />
          </span>
          <AriaTooltip
            triggerRef={statusRef}
            placement="top start"
            offset={8}
            className="cv-tooltip"
          >
            {message}
          </AriaTooltip>
        </TooltipTrigger>
      ) : trailingIcon != null ? (
        <span className="cv-field__slot cv-field__slot--trailing">
          {trailingIcon}
        </span>
      ) : null}
    </div>
  );
}

export interface TextAreaProps extends Omit<
  AriaTextFieldProps,
  "children" | "className" | "style" | "spellCheck"
> {
  label?: ReactNode;
  hideLabel?: boolean;
  description?: ReactNode;
  errorMessage?: FieldErrorMessage;
  placeholder?: string;
  spellCheck?: boolean;
  /** Visible rows before the field scrolls. @default 3 */
  rows?: number;
  className?: string;
}

/**
 * A multi-line text field. Errors show inline rather than in `TextField`'s
 * hover tooltip — a tooltip anchored to a tall control reads oddly.
 */
export function TextArea({
  label,
  hideLabel = false,
  description,
  errorMessage: providedErrorMessage,
  placeholder,
  spellCheck,
  rows = 3,
  className,
  ...props
}: TextAreaProps) {
  const errorMessage =
    providedErrorMessage === "" ? undefined : providedErrorMessage;

  return (
    <AriaTextField {...props} className={cx("cv-field", className)}>
      {label != null ? (
        <Label className={hideLabel ? "cv-sr-only" : "cv-field__label"}>
          {label}
        </Label>
      ) : null}
      <AriaTextArea
        className="cv-input cv-textarea"
        rows={rows}
        {...(placeholder !== undefined ? { placeholder } : {})}
        {...(spellCheck !== undefined ? { spellCheck } : {})}
      />
      {description != null ? (
        <Text slot="description" className="cv-field__description">
          {description}
        </Text>
      ) : null}
      <FieldError className="cv-field__error">{errorMessage}</FieldError>
    </AriaTextField>
  );
}

export interface SearchFieldProps extends Omit<
  AriaSearchFieldProps,
  "children" | "className" | "style"
> {
  label?: ReactNode;
  placeholder?: string;
  className?: string;
}

export function SearchField({
  label,
  placeholder = "Search...",
  className,
  ...props
}: SearchFieldProps) {
  return (
    <AriaSearchField
      {...props}
      className={cx("cv-field", "cv-search-field", className)}
    >
      {label != null ? (
        <Label className="cv-field__label">{label}</Label>
      ) : null}
      <div className="cv-field__control" data-leading>
        <span className="cv-field__slot cv-field__slot--leading">
          <MagnifyingGlass aria-hidden="true" />
        </span>
        <Input className="cv-input" placeholder={placeholder} />
      </div>
    </AriaSearchField>
  );
}
