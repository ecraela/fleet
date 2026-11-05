import type { ReactNode } from "react";
import {
  Menu as AriaMenu,
  MenuItem as AriaMenuItem,
  MenuTrigger,
  Popover,
  Separator,
  Text,
  type MenuItemProps as AriaMenuItemProps,
  type MenuProps as AriaMenuProps,
  type PopoverProps,
} from "react-aria-components";
import { cx } from "../../utils/cx";

export interface MenuProps<T extends object> extends Omit<
  AriaMenuProps<T>,
  "className" | "style"
> {
  /** A React Aria pressable, typically an `IconButton` or `Button`. */
  trigger: ReactNode;
  placement?: NonNullable<PopoverProps["placement"]>;
}

export function Menu<T extends object>({
  trigger,
  placement = "bottom end",
  ...props
}: MenuProps<T>) {
  return (
    <MenuTrigger>
      {trigger}
      <Popover
        placement={placement}
        offset={6}
        className="cv-popover cv-menu__popover"
      >
        <AriaMenu {...props} className="cv-menu" />
      </Popover>
    </MenuTrigger>
  );
}

export interface MenuItemProps<T extends object> extends Omit<
  AriaMenuItemProps<T>,
  "children" | "className" | "style"
> {
  children: ReactNode;
  icon?: ReactNode;
  /** Right-aligned decoration, e.g. an external-link arrow. */
  trailing?: ReactNode;
  /** Supporting copy, including the reason an action is unavailable. */
  description?: ReactNode;
  /** Disables the action and keeps its reason visible in the menu. */
  disabledReason?: string | undefined;
  /** Destructive items render in the error color. */
  tone?: "default" | "danger";
}

export function MenuItem<T extends object>({
  children,
  icon,
  trailing,
  description,
  disabledReason,
  tone = "default",
  textValue,
  ...props
}: MenuItemProps<T>) {
  const resolvedTextValue =
    textValue ?? (typeof children === "string" ? children : undefined);
  const opensNewTab = props.target === "_blank";
  const supportingCopy = disabledReason ?? description;

  return (
    <AriaMenuItem
      {...props}
      {...(disabledReason ? { isDisabled: true } : {})}
      {...(resolvedTextValue !== undefined
        ? { textValue: resolvedTextValue }
        : {})}
      {...(opensNewTab && props.rel === undefined
        ? { rel: "noopener noreferrer" }
        : {})}
      className={cx(
        "cv-menu__item",
        supportingCopy != null && "cv-menu__item--described",
        tone === "danger" && "cv-menu__item--danger",
      )}
    >
      {icon != null ? (
        <span className="cv-menu__icon" aria-hidden="true">
          {icon}
        </span>
      ) : null}
      <span className="cv-menu__label">
        <Text slot="label">
          {children}
          {opensNewTab ? (
            <span className="cv-sr-only"> (opens in a new tab)</span>
          ) : null}
        </Text>
        {supportingCopy != null ? (
          <Text slot="description" className="cv-menu__description">
            {supportingCopy}
          </Text>
        ) : null}
      </span>
      {trailing != null ? (
        <span className="cv-menu__trailing" aria-hidden="true">
          {trailing}
        </span>
      ) : null}
    </AriaMenuItem>
  );
}

export function MenuSeparator() {
  return <Separator className="cv-menu__separator" />;
}
