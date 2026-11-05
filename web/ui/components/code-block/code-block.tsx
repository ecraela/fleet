import type { ReactNode } from "react";
import {
  FieldError,
  Label,
  Text,
  TextArea as AriaTextArea,
  TextField as AriaTextField,
  type TextFieldProps as AriaTextFieldProps,
} from "react-aria-components";
import { cx } from "../../utils/cx";
import { CopyButton } from "../copy/copy";

export interface CodeBlockProps {
  code: string;
  language?: "json" | "text";
  /** Names the scrollable region for assistive technology. */
  label?: string;
  /** Header bar text, e.g. "JSON". The copy button moves into the bar. */
  title?: string;
  showLineNumbers?: boolean;
  isCopyable?: boolean;
  /** Wrap long lines instead of scrolling sideways, for values rather than code. */
  wrap?: boolean;
  className?: string;
}

export function CodeBlock({
  code,
  language = "json",
  label = "Code",
  title,
  showLineNumbers = false,
  isCopyable = true,
  wrap = false,
  className,
}: CodeBlockProps) {
  const lines = code.split("\n");
  const copy = isCopyable ? (
    <CopyButton value={code} label={`Copy ${label.toLowerCase()}`} />
  ) : null;

  return (
    <div
      className={cx(
        "cv-code-block",
        showLineNumbers && "cv-code-block--numbered",
        wrap && "cv-code-block--wrap",
        className,
      )}
    >
      {title != null ? (
        <div className="cv-code-block__header">
          <span>{title}</span>
          {copy}
        </div>
      ) : null}
      <pre className="cv-code-block__pre" tabIndex={0} aria-label={label}>
        <code>
          {lines.map((line, index) => (
            <span key={index} className="cv-code__line">
              {language === "json" ? highlightJson(line) : line}
              {index < lines.length - 1 ? "\n" : null}
            </span>
          ))}
        </code>
      </pre>
      {title == null && copy != null ? (
        <div className="cv-code-block__copy">{copy}</div>
      ) : null}
    </div>
  );
}

export interface CodeEditorProps extends Omit<
  AriaTextFieldProps,
  "children" | "className" | "style"
> {
  label?: ReactNode;
  description?: ReactNode;
  errorMessage?: string | undefined;
  language?: "json" | "text";
  placeholder?: string;
  /** Lines shown before the editor grows. @default 4 */
  rows?: number;
  className?: string;
}

/**
 * An editable code block: `CodeBlock`'s surface and highlighting over a
 * native text area, so selection, undo and assistive technology behave as in
 * any other field. It grows with its content and scrolls sideways.
 */
export function CodeEditor({
  label,
  description,
  errorMessage,
  language = "json",
  placeholder,
  rows = 4,
  className,
  value = "",
  ...props
}: CodeEditorProps) {
  const lines = value.split("\n");
  return (
    <AriaTextField
      {...props}
      value={value}
      className={cx("cv-field", className)}
    >
      {label != null ? (
        <Label className="cv-field__label">{label}</Label>
      ) : null}
      <div className="cv-code-editor">
        <pre className="cv-code-editor__highlight" aria-hidden="true">
          <code>
            {lines.map((line, index) => (
              <span key={index} className="cv-code__line">
                {language === "json" ? highlightJson(line) : line}
                {/* A zero-width space gives a trailing empty line its height. */}
                {index < lines.length - 1 ? "\n" : "\u200b"}
              </span>
            ))}
          </code>
        </pre>
        <AriaTextArea
          className="cv-code-editor__input"
          rows={rows}
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          wrap="off"
          {...(placeholder !== undefined ? { placeholder } : {})}
        />
      </div>
      {description != null ? (
        <Text slot="description" className="cv-field__description">
          {description}
        </Text>
      ) : null}
      <FieldError className="cv-field__error">
        {errorMessage === "" ? undefined : errorMessage}
      </FieldError>
    </AriaTextField>
  );
}

/** Inline monospace text for DNS names, record values, and identifiers. */
export function MonoText({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return <span className={cx("cv-mono", className)}>{children}</span>;
}

const JSON_TOKEN =
  /("(?:\\.|[^"\\])*")(\s*:)?|\b(true|false|null)\b|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)|([{}[\],])/g;

function highlightJson(code: string) {
  const nodes: ReactNode[] = [];
  const pattern = new RegExp(JSON_TOKEN.source, "g");
  let cursor = 0;
  let match: RegExpExecArray | null;

  while ((match = pattern.exec(code)) !== null) {
    const [token, string, colon, literal, number, punctuation] = match;
    if (match.index > cursor) nodes.push(code.slice(cursor, match.index));
    const key = match.index;

    if (string !== undefined) {
      nodes.push(
        <span key={key} className={colon ? "cv-code__key" : "cv-code__string"}>
          {string}
        </span>,
      );
      if (colon) {
        nodes.push(
          <span key={`${key}:`} className="cv-code__punctuation">
            {colon}
          </span>,
        );
      }
    } else if (literal !== undefined || number !== undefined) {
      nodes.push(
        <span key={key} className="cv-code__literal">
          {token}
        </span>,
      );
    } else if (punctuation !== undefined) {
      nodes.push(
        <span key={key} className="cv-code__punctuation">
          {token}
        </span>,
      );
    }

    cursor = match.index + token.length;
  }

  if (cursor < code.length) nodes.push(code.slice(cursor));
  return nodes;
}
