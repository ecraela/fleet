import { useState, type ReactNode } from "react";
import {
  Button as AriaButton,
  Tooltip as AriaTooltip,
  TooltipTrigger,
  type TooltipProps as AriaTooltipProps,
  type TooltipTriggerComponentProps,
} from "react-aria-components";

export interface TooltipProps extends Omit<
  TooltipTriggerComponentProps,
  "children"
> {
  content: ReactNode;
  /** A focusable React Aria element such as `Button` or `Link`. */
  children: ReactNode;
  placement?: NonNullable<AriaTooltipProps["placement"]>;
}

export type TooltipTextProps = Pick<
  TooltipProps,
  "content" | "children" | "placement"
>;

/** Text whose description can also be opened with a tap or keyboard press. */
export function TooltipText({ children, ...props }: TooltipTextProps) {
  const [isOpen, setOpen] = useState(false);
  return (
    <Tooltip {...props} isOpen={isOpen} onOpenChange={setOpen}>
      <AriaButton className="cv-tooltip-text" onPress={() => setOpen(true)}>
        {children}
      </AriaButton>
    </Tooltip>
  );
}

export function Tooltip({
  content,
  children,
  placement = "top",
  delay = 300,
  closeDelay = 0,
  ...props
}: TooltipProps) {
  return (
    <TooltipTrigger {...props} delay={delay} closeDelay={closeDelay}>
      {children}
      <AriaTooltip placement={placement} offset={8} className="cv-tooltip">
        {content}
      </AriaTooltip>
    </TooltipTrigger>
  );
}
