import { SuspendableOverlay } from "../dialog/overlay-suspension";
import type { ReactNode } from "react";
import {
  Dialog as AriaDialog,
  Heading,
  Modal as AriaModal,
  ModalOverlay,
  type ModalOverlayProps,
} from "react-aria-components";
import { cx } from "../../utils/cx";
import { ArrowLeft, XMark } from "../../icons";
import { Button, IconButton } from "../button/button";
import type { DialogProps } from "../dialog/dialog";

export interface DrawerProps extends Omit<
  ModalOverlayProps,
  "children" | "className" | "style"
> {
  children: ReactNode;
  className?: string;
  size?: "md" | "lg";
}

/**
 * A panel that slides in from the right edge and fills the screen on small
 * viewports. Place a React Aria `Dialog` inside; control it with `isOpen` or
 * wrap it in a `DialogTrigger`. `DrawerHeader`, `DrawerBody`, and
 * `DrawerFooter` give it a fixed header and footer around a scrolling body.
 */
export function Drawer({
  children,
  className,
  size = "md",
  isDismissable = true,
  ...props
}: DrawerProps) {
  return (
    <SuspendableOverlay>
      <ModalOverlay
        {...props}
        isDismissable={isDismissable}
        className="cv-drawer-overlay"
      >
        <AriaModal className={cx("cv-drawer", `cv-drawer--${size}`, className)}>
          {children}
        </AriaModal>
      </ModalOverlay>
    </SuspendableOverlay>
  );
}

/** A titled drawer with the same close and form semantics as `Dialog`. */
export interface DrawerDialogProps extends DialogProps {
  onBack?: (() => void) | undefined;
  backLabel?: string | undefined;
  backDisabledReason?: string | undefined;
}
export function DrawerDialog({
  title,
  description,
  children,
  hideCloseButton = false,
  onBack,
  backLabel = "Back",
  backDisabledReason,
  ...props
}: DrawerDialogProps) {
  return (
    <AriaDialog {...props} className="cv-drawer__dialog">
      {({ close }) => (
        <>
          <DrawerHeader>
            <div className="cv-drawer__heading">
              {onBack && (
                <Button
                  className="cv-drawer__back"
                  variant="ghost"
                  size="sm"
                  leadingIcon={<ArrowLeft />}
                  disabledReason={backDisabledReason}
                  onPress={onBack}
                >
                  {backLabel}
                </Button>
              )}
              <Heading slot="title" className="cv-drawer__title">
                {title}
              </Heading>
              {description != null && (
                <div className="cv-drawer__description">{description}</div>
              )}
            </div>
            {!hideCloseButton && (
              <IconButton
                aria-label="Close"
                variant="ghost"
                size="sm"
                onPress={close}
              >
                <XMark />
              </IconButton>
            )}
          </DrawerHeader>
          {typeof children === "function" ? children({ close }) : children}
        </>
      )}
    </AriaDialog>
  );
}

interface DrawerSectionProps {
  children: ReactNode;
  className?: string;
}

export function DrawerHeader({ children, className }: DrawerSectionProps) {
  return <div className={cx("cv-drawer__header", className)}>{children}</div>;
}

export function DrawerBody({ children, className }: DrawerSectionProps) {
  return <div className={cx("cv-drawer__body", className)}>{children}</div>;
}

export function DrawerFooter({
  children,
  className,
  description,
}: DrawerSectionProps & { description?: ReactNode }) {
  return (
    <div className={cx("cv-drawer__footer", className)}>
      {description != null && (
        <p className="cv-drawer__footer-description">{description}</p>
      )}
      {children}
    </div>
  );
}
