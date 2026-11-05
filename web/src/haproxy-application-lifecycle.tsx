import { useEffect, useRef, useState } from "react";
import {
  Button,
  Dialog,
  DialogBody,
  DialogFooter,
  DialogForm,
  Drawer,
  DrawerBody,
  DrawerDialog,
  DrawerFooter,
  InlineTip,
  Modal,
  TextField,
  focusFormError,
} from "@ecraela/ui";
import type {
  HAProxyApplicationAction,
  HAProxyApplicationRevision,
} from "./haproxy-api";

export const applicationActionLabel = (action: HAProxyApplicationAction) =>
  ({
    disable: "Disable application",
    enable: "Re-enable application",
    delete: "Delete application",
    restore: "Restore application",
  })[action];

export function HAProxyApplicationLifecycle({
  revision,
  action,
  busy,
  error,
  permissionReason,
  onApply,
  onClose,
}: {
  revision: HAProxyApplicationRevision | undefined;
  action: HAProxyApplicationAction | undefined;
  busy: boolean;
  error: string | null;
  permissionReason: string | undefined;
  onApply: () => void;
  onClose: () => void;
}) {
  const [confirmation, setConfirmation] = useState("");
  const [confirmationError, setConfirmationError] = useState<string | null>(
    null,
  );
  const form = useRef<HTMLFormElement>(null);
  useEffect(() => {
    setConfirmation("");
    setConfirmationError(null);
  }, [action, revision?.id]);
  const destructive = action === "disable" || action === "delete";
  const Overlay = destructive ? Modal : Drawer;
  const Content = destructive ? Dialog : DrawerDialog;
  const Body = destructive ? DialogBody : DrawerBody;
  const Footer = destructive ? DialogFooter : DrawerFooter;

  function apply() {
    if (busy || permissionReason || !action || !revision) return;
    if (destructive && confirmation !== revision.spec.name) {
      setConfirmationError(
        "Enter the application name to confirm this change.",
      );
      focusFormError(form.current);
      return;
    }
    onApply();
  }
  return (
    <Overlay
      size={destructive ? "md" : "lg"}
      isOpen={!!action && !!revision}
      onOpenChange={(open) => {
        if (!open && !busy) onClose();
      }}
      isDismissable={!busy}
      isKeyboardDismissDisabled={busy}
    >
      <Content
        role={destructive ? "alertdialog" : "dialog"}
        title={action ? applicationActionLabel(action) : "Application"}
        description={revision?.spec.name}
        hideCloseButton={busy}
      >
        <DialogForm ref={form} onSubmit={apply}>
          <Body>
            {error && (
              <InlineTip variant="error" role="alert">
                {error}
              </InlineTip>
            )}
            {action && revision && (
              <>
                <p className="cv-dialog__text">
                  <strong>
                    {revision.spec.hostname}
                    {revision.spec.path_prefix}
                  </strong>
                </p>
                <p className="cv-dialog__text">
                  {action === "disable"
                    ? "Return 503 for this route while retaining its hostname, path and settings for re-enabling. More specific neighboring routes keep serving. The rolling update drains existing node connections."
                    : action === "delete"
                      ? "Remove this application's routes and backend through a rolling update. Its immutable history remains available for restoration. Shared listeners, TLS certificates and other applications are preserved. Removed routes may match an existing broader route."
                      : action === "restore"
                        ? "Restore this application's saved settings through a rolling update. Conflicting routes or changed configuration must be resolved first."
                        : "Restore this application's backend endpoints through a rolling update and verify health before each node rejoins traffic."}
                </p>
                <InlineTip>
                  DNS records stay in place. Any DNS cleanup is a separate
                  change in the zone’s normal review, approval, apply and
                  verification flow.
                </InlineTip>
                {destructive && (
                  <TextField
                    label={`Enter ${revision.spec.name} to confirm`}
                    value={confirmation}
                    onChange={(value) => {
                      setConfirmation(value);
                      setConfirmationError(null);
                    }}
                    autoComplete="off"
                    autoFocus
                    isInvalid={!!confirmationError}
                    errorMessage={confirmationError ?? undefined}
                    errorDisplay="inline"
                  />
                )}
              </>
            )}
          </Body>
          <Footer>
            <Button
              type="submit"
              variant={destructive ? "danger" : "primary"}
              isLoading={busy}
              disabledReason={permissionReason}
            >
              {action ? applicationActionLabel(action) : "Apply"}
            </Button>
            <Button isDisabled={busy} onPress={onClose}>
              Cancel
            </Button>
          </Footer>
        </DialogForm>
      </Content>
    </Overlay>
  );
}
