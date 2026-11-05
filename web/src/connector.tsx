import { useEffect, useState } from "react";
import {
  Badge,
  Button,
  Checkbox,
  DescriptionItem,
  DescriptionList,
  Drawer,
  DrawerBody,
  DrawerDialog,
  DrawerFooter,
  InlineTip,
  LoadingStatus,
  TextField,
} from "@ecraela/ui";
import { localRequest, LocalAuthError, type LocalSession } from "./auth";

interface Connection {
  enabled: boolean;
  relay_url: string;
  state: string;
  linked: boolean;
  cloud_organization_id?: string;
  allow_manage?: boolean;
}
export function ConvergeConnector({ session }: { session: LocalSession }) {
  const [open, setOpen] = useState(false);
  const [connection, setConnection] = useState<Connection | null>(null);
  const [unavailable, setUnavailable] = useState(false);
  const [code, setCode] = useState("");
  const [allowManage, setAllowManage] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const manage = session.capabilities.includes("haproxy.manage");
  useEffect(() => {
    let active = true;
    async function load() {
      try {
        const value = await localRequest<Connection>("connector", {}, session);
        if (active) {
          setConnection(value);
          setUnavailable(false);
        }
      } catch (e) {
        if (active && e instanceof LocalAuthError && e.status === 404)
          setUnavailable(true);
      }
    }
    void load();
    const timer = setInterval(() => {
      if (document.visibilityState !== "hidden") void load();
    }, 3000);
    return () => {
      active = false;
      clearInterval(timer);
    };
  }, [session.user_id, session.csrf_token]);
  async function act(method: "POST" | "DELETE") {
    setBusy(true);
    setError(null);
    try {
      await localRequest(
        "connector",
        {
          method,
          ...(method === "POST"
            ? {
                body: JSON.stringify({
                  code: code.trim(),
                  allow_manage: allowManage,
                }),
              }
            : {}),
        },
        session,
      );
      setCode("");
      setConnection(await localRequest<Connection>("connector", {}, session));
    } catch (e) {
      setError(
        e instanceof LocalAuthError
          ? e.message
          : "Could not update the Converge connection. Retry shortly.",
      );
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <Button onPress={() => setOpen(true)}>Converge connection</Button>
      {open && (
        <Drawer
          isOpen
          onOpenChange={(value) => {
            if (!busy) setOpen(value);
          }}
          isDismissable={!busy}
        >
          <DrawerDialog
            title="Converge connection"
            description="Link this manager using an outbound connection. Local login works independently."
          >
            <DrawerBody>
              {error && <InlineTip variant="error">{error}</InlineTip>}
              {unavailable ? (
                <InlineTip>
                  Enable the connector on this manager with a relay URL and a
                  private persistent connector state directory. See the
                  self-hosted setup guide.
                </InlineTip>
              ) : !connection ? (
                <LoadingStatus>Checking connection</LoadingStatus>
              ) : (
                <>
                  <DescriptionList columns={1}>
                    <DescriptionItem label="Relay">
                      {connection.relay_url}
                    </DescriptionItem>
                    <DescriptionItem label="Status">
                      <Badge
                        tone={
                          connection.state === "connected"
                            ? "success"
                            : connection.linked
                              ? "warning"
                              : "neutral"
                        }
                      >
                        {connection.state === "connected"
                          ? "Connected"
                          : connection.linked
                            ? "Connecting or offline"
                            : "Not linked"}
                      </Badge>
                    </DescriptionItem>
                    {connection.cloud_organization_id && (
                      <DescriptionItem label="Converge organization">
                        {connection.cloud_organization_id}
                      </DescriptionItem>
                    )}
                  </DescriptionList>
                  {connection.linked ? (
                    <InlineTip>
                      {connection.allow_manage
                        ? "Authorized Converge operators can observe and manage this fleet. Agent policies and health checks still apply."
                        : "Converge can observe this fleet. Changes through the connector are disabled locally."}{" "}
                      Unlinking stops remote access and keeps local users, node
                      identities and TLS keys.
                    </InlineTip>
                  ) : (
                    <>
                      <p>
                        In Converge, open HAProxy → Connect manager and create a
                        linking code for your organization. Paste it here within
                        ten minutes.
                      </p>
                      <TextField
                        label="Linking code"
                        value={code}
                        onChange={setCode}
                        autoComplete="off"
                        isDisabled={!manage || busy}
                      />
                      <Checkbox
                        isSelected={allowManage}
                        onChange={setAllowManage}
                        isDisabled={!manage || busy}
                      >
                        Allow operators with Manage HAProxy permission to make
                        changes
                      </Checkbox>
                      <InlineTip>
                        Only HAProxy management uses this connection. Your
                        application traffic stays on your infrastructure. DNS
                        changes keep their normal review flow.
                      </InlineTip>
                    </>
                  )}
                </>
              )}
            </DrawerBody>
            <DrawerFooter>
              <Button onPress={() => setOpen(false)} isDisabled={busy}>
                Close
              </Button>
              {connection &&
                (connection.linked ? (
                  <Button
                    variant="danger"
                    disabledReason={
                      !manage
                        ? "Local administrator permission required."
                        : undefined
                    }
                    isLoading={busy}
                    onPress={() => void act("DELETE")}
                  >
                    Unlink Converge
                  </Button>
                ) : (
                  <Button
                    variant="primary"
                    isDisabled={!code.trim()}
                    disabledReason={
                      !manage
                        ? "Local administrator permission required."
                        : undefined
                    }
                    isLoading={busy}
                    onPress={() => void act("POST")}
                  >
                    Link to Converge
                  </Button>
                ))}
            </DrawerFooter>
          </DrawerDialog>
        </Drawer>
      )}
    </>
  );
}
