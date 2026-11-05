import { useId, type ReactNode } from "react";
import {
  Switch as AriaSwitch,
  type SwitchProps as AriaSwitchProps,
} from "react-aria-components";

export interface SwitchProps extends Omit<
  AriaSwitchProps,
  "children" | "className" | "style"
> {
  label: string;
  description?: ReactNode;
  disabledReason?: string | undefined;
}

export function Switch({
  label,
  description,
  disabledReason,
  ...props
}: SwitchProps) {
  const descriptionId = useId();
  const copy = disabledReason ?? description;
  const describedBy = [
    props["aria-describedby"],
    copy != null ? descriptionId : undefined,
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <AriaSwitch
      {...props}
      aria-label={props["aria-label"] ?? label}
      {...(describedBy ? { "aria-describedby": describedBy } : {})}
      isDisabled={props.isDisabled || !!disabledReason}
      className="cv-switch"
    >
      <span className="cv-switch__track" aria-hidden="true">
        <span className="cv-switch__thumb" />
      </span>
      <span className="cv-switch__label">{label}</span>
      {copy != null && (
        <span id={descriptionId} className="cv-switch__description">
          {copy}
        </span>
      )}
    </AriaSwitch>
  );
}
