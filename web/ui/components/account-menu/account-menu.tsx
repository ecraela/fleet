import { EllipsisHorizontal, Moon, Sun } from "../../icons";
import type { ComponentProps, ReactNode } from "react";
import {
  Button,
  Header,
  Menu,
  MenuItem,
  MenuSection,
  MenuTrigger,
  Popover,
  ToggleButton,
  ToggleButtonGroup,
} from "react-aria-components";
import { cx } from "../../utils/cx";
import { Avatar } from "../avatar/avatar";

export interface AccountMenuUser {
  email?: string;
  name?: string;
  avatarSrc?: string;
}

export interface AccountMenuProps {
  user: AccountMenuUser;
  /** `MenuItem`, `MenuSeparator`, and `ThemeMenuSection` elements. */
  children: ReactNode;
  defaultOpen?: boolean;
  className?: string;
  placement?: ComponentProps<typeof Popover>["placement"];
  description?: ReactNode;
  isPending?: boolean;
}

/** Signed-in user control with configurable menu placement. */
export function AccountMenu({
  user,
  children,
  defaultOpen = false,
  className,
  placement = "top start",
  description,
  isPending = false,
}: AccountMenuProps) {
  const label = user.name ?? user.email ?? "Account";

  return (
    <MenuTrigger defaultOpen={defaultOpen}>
      <Button
        aria-label={`${label}, account menu`}
        isPending={isPending}
        className={cx("cv-account-menu", className)}
      >
        <span className="cv-account-menu__identity">
          <Avatar
            name={label}
            {...(user.avatarSrc ? { src: user.avatarSrc } : {})}
          />
          <span className="cv-account-menu__name">{label}</span>
        </span>
        <EllipsisHorizontal
          className="cv-account-menu__more"
          aria-hidden="true"
        />
      </Button>
      <Popover
        placement={placement}
        offset={8}
        className="cv-popover cv-account-menu__popover"
      >
        {description != null && (
          <div className="cv-account-menu__description">{description}</div>
        )}
        <Menu className="cv-menu" aria-label="Account">
          {children}
        </Menu>
      </Popover>
    </MenuTrigger>
  );
}

export interface ThemeOption {
  id: string;
  label: string;
  icon: ReactNode;
}

export const defaultThemeOptions: readonly ThemeOption[] = [
  { id: "light", label: "Light", icon: <Sun /> },
  { id: "dark", label: "Dark", icon: <Moon /> },
];

export interface ThemeMenuSectionProps {
  value: string;
  onChange: (theme: string) => void;
  label?: string;
  options?: readonly ThemeOption[];
}

/**
 * Appearance row with a segmented theme switch. The options are real
 * single-select menu items, so arrow keys, typeahead, and screen readers treat
 * them like any other menu choice, and choosing one keeps the menu open.
 */
export function ThemeMenuSection({
  value,
  onChange,
  label = "Appearance",
  options = defaultThemeOptions,
}: ThemeMenuSectionProps) {
  return (
    <MenuSection
      className="cv-theme-section"
      selectionMode="single"
      disallowEmptySelection
      shouldCloseOnSelect={false}
      selectedKeys={[value]}
      onSelectionChange={(keys) => {
        if (keys === "all") return;
        const [key] = [...keys];
        if (key != null) onChange(String(key));
      }}
    >
      <Header className="cv-theme-section__label">{label}</Header>
      {options.map((option) => (
        <MenuItem
          key={option.id}
          id={option.id}
          textValue={option.label}
          className="cv-theme-option"
        >
          <span className="cv-theme-option__swatch" aria-hidden="true">
            {option.icon}
          </span>
          <span className="cv-sr-only">{option.label} theme</span>
        </MenuItem>
      ))}
    </MenuSection>
  );
}

export interface ThemeSwitchProps {
  value: string;
  onChange: (theme: string) => void;
  label?: string;
  options?: readonly ThemeOption[];
}

/**
 * The same segmented sun/moon control as `ThemeMenuSection`, as a standalone
 * control for contexts outside a menu (a settings page, `MobileNav`).
 */
export function ThemeSwitch({
  value,
  onChange,
  label = "Appearance",
  options = defaultThemeOptions,
}: ThemeSwitchProps) {
  return (
    <ToggleButtonGroup
      aria-label={label}
      selectionMode="single"
      disallowEmptySelection
      selectedKeys={[value]}
      onSelectionChange={(keys) => {
        const [key] = Array.from(keys);
        if (key != null) onChange(String(key));
      }}
      className="cv-theme-switch"
    >
      {options.map((option) => (
        <ToggleButton
          key={option.id}
          id={option.id}
          aria-label={`${option.label} theme`}
          className="cv-theme-option"
        >
          <span className="cv-theme-option__swatch" aria-hidden="true">
            {option.icon}
          </span>
        </ToggleButton>
      ))}
    </ToggleButtonGroup>
  );
}
