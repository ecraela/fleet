import { SuspendableOverlay } from "./overlay-suspension";
import { XMark } from "../../icons";
import { useState, type ReactNode, type Ref } from "react";
import {
  Dialog as AriaDialog,
  Heading,
  Modal as AriaModal,
  ModalOverlay,
  type DialogProps as AriaDialogProps,
  type ModalOverlayProps,
} from "react-aria-components";
import { cx } from "../../utils/cx";
import { Button, IconButton } from "../button/button";
import { CopyableText } from "../copy/copy";
import { TextField } from "../text-field/text-field";

export interface ModalProps extends Omit<
  ModalOverlayProps,
  "children" | "className" | "style"
> {
  children: ReactNode;
  size?: "sm" | "md" | "lg";
}

/** Overlay and panel. Place a `Dialog` inside; use within `DialogTrigger` or control `isOpen`. */
export function Modal({
  children,
  size = "md",
  isDismissable = true,
  ...props
}: ModalProps) {
  return (
    <SuspendableOverlay>
      <ModalOverlay
        {...props}
        isDismissable={isDismissable}
        className="cv-modal-overlay"
      >
        <AriaModal className={cx("cv-modal", `cv-modal--${size}`)}>
          {children}
        </AriaModal>
      </ModalOverlay>
    </SuspendableOverlay>
  );
}

export interface DialogProps extends Omit<
  AriaDialogProps,
  "children" | "className" | "style"
> {
  title: ReactNode;
  description?: ReactNode;
  children?: ReactNode | ((options: { close: () => void }) => ReactNode);
  hideCloseButton?: boolean;
}

export function Dialog({
  title,
  description,
  children,
  hideCloseButton = false,
  ...props
}: DialogProps) {
  return (
    <AriaDialog {...props} className="cv-dialog">
      {({ close }) => (
        <>
          <Heading slot="title" className="cv-dialog__title">
            {title}
          </Heading>
          {hideCloseButton ? null : (
            <IconButton
              aria-label="Close"
              variant="ghost"
              size="sm"
              className="cv-dialog__close"
              onPress={close}
            >
              <XMark />
            </IconButton>
          )}
          {description != null ? (
            <div className="cv-dialog__description">{description}</div>
          ) : null}
          {typeof children === "function" ? children({ close }) : children}
        </>
      )}
    </AriaDialog>
  );
}

export interface DialogFormProps {
  ref?: Ref<HTMLFormElement>;
  children: ReactNode;
  /**
   * Called on Enter in a single-line field, and on ⌘/Ctrl + Enter anywhere
   * when the footer's `type="submit"` button carries `submitShortcut`.
   */
  onSubmit: () => void;
  className?: string;
}

export function DialogForm({
  children,
  onSubmit,
  className,
  ref,
}: DialogFormProps) {
  return (
    <form
      ref={ref}
      noValidate
      className={cx("cv-dialog__form", className)}
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit();
      }}
    >
      {children}
    </form>
  );
}

export function DialogBody({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return <div className={cx("cv-dialog__body", className)}>{children}</div>;
}

export function DialogFooter({ children }: { children: ReactNode }) {
  return <div className="cv-dialog__footer">{children}</div>;
}

/** Shortcut hints used by dialog actions. `submitShortcut` is live (see `Button`). */
export const submitShortcut = ["⌘", "↩︎"] as const;
export const cancelShortcut = ["Esc"] as const;

export interface ConfirmDeleteDialogProps extends Omit<
  ModalProps,
  "children" | "size"
> {
  /** Lowercase noun, e.g. "zone". Used in the title, copy, and button. */
  resourceType: string;
  resourceName: string;
  /** Called on confirm. Rejecting keeps the dialog open and shows the error. */
  onConfirm: () => void | Promise<void>;
  description?: ReactNode;
  consequence?: ReactNode;
  /** Require typing the resource name before the action enables. */
  requireConfirmation?: boolean;
}

export function ConfirmDeleteDialog({
  resourceType,
  resourceName,
  onConfirm,
  description,
  consequence = "This cannot be undone.",
  requireConfirmation = true,
  ...modalProps
}: ConfirmDeleteDialogProps) {
  return (
    <Modal {...modalProps}>
      <Dialog role="alertdialog" title={`Delete ${resourceType}`}>
        {({ close }) => (
          <ConfirmDeleteForm
            close={close}
            resourceType={resourceType}
            resourceName={resourceName}
            onConfirm={onConfirm}
            description={
              description ??
              `Are you sure you want to delete this ${resourceType}?`
            }
            consequence={consequence}
            requireConfirmation={requireConfirmation}
          />
        )}
      </Dialog>
    </Modal>
  );
}

function ConfirmDeleteForm({
  close,
  resourceType,
  resourceName,
  onConfirm,
  description,
  consequence,
  requireConfirmation,
}: {
  close: () => void;
  resourceType: string;
  resourceName: string;
  onConfirm: () => void | Promise<void>;
  description: ReactNode;
  consequence: ReactNode;
  requireConfirmation: boolean;
}) {
  const [value, setValue] = useState("");
  const [isPending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const isConfirmed = !requireConfirmation || value === resourceName;

  const submit = async () => {
    if (!isConfirmed || isPending) return;
    setPending(true);
    setError(null);
    try {
      await onConfirm();
      close();
    } catch (reason) {
      setError(
        reason instanceof Error ? reason.message : "Delete failed. Try again.",
      );
      setPending(false);
    }
  };

  return (
    <DialogForm onSubmit={submit}>
      <DialogBody>
        <div>
          <p className="cv-dialog__text">{description}</p>
          {consequence != null ? (
            <p className="cv-dialog__text cv-dialog__text--danger">
              {consequence}
            </p>
          ) : null}
        </div>
        {requireConfirmation ? (
          <>
            <p className="cv-dialog__text">
              Type{" "}
              <CopyableText
                variant="inline"
                value={resourceName}
                label={`Copy ${resourceType} name`}
              />{" "}
              to confirm.
            </p>
            <TextField
              aria-label={`${resourceType} name`}
              placeholder={`Enter ${resourceType} name`}
              value={value}
              onChange={setValue}
              autoComplete="off"
              autoFocus
            />
          </>
        ) : null}
        {error != null ? (
          <p role="alert" className="cv-dialog__text cv-dialog__text--danger">
            {error}
          </p>
        ) : null}
      </DialogBody>
      <DialogFooter>
        <Button
          type="submit"
          variant="danger"
          shortcut={submitShortcut}
          isDisabled={!isConfirmed}
          isLoading={isPending}
        >
          Delete {resourceType}
        </Button>
        <Button shortcut={cancelShortcut} onPress={close}>
          Cancel
        </Button>
      </DialogFooter>
    </DialogForm>
  );
}
