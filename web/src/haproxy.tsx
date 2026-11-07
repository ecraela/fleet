import { ConnectionIssue } from "./connection-health";
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useLocation, useNavigate } from "react-router-dom";
import {
  Button,
  Drawer,
  DrawerDialog,
  DrawerBody,
  DrawerFooter,
  DialogForm,
  EmptyState,
  InlineTip,
  LoadingStatus,
  TextField,
} from "@ecraela/ui";
import { Directions, ShieldCheck } from "@ecraela/ui/icons";
import { ApiError, type Organization } from "./api";
import { hasOrganizationCapability as can } from "./capabilities";
import { isActionCancelled, usePermissionAction } from "./permissions";
import {
  HAProxyClient,
  HAProxyError,
  haproxyErrorMessage,
  type HAProxyFleet,
} from "./haproxy-api";
import { HAProxyEnrollment } from "./haproxy-enrollment";
import { HAProxyWorkspace } from "./haproxy-workspace";
import { haproxyPath } from "./haproxy-navigation";
import "./haproxy.css";
function actionError(error: unknown): string {
  return error instanceof ApiError ? error.message : haproxyErrorMessage(error);
}

function mergeFleet(pages: HAProxyFleet[]): HAProxyFleet {
  return {
    clusters: pages[0]?.clusters ?? [],
    nodes: [
      ...new Map(
        pages.flatMap((page) => page.nodes).map((node) => [node.id, node]),
      ).values(),
    ],
    next_cursor: pages.at(-1)?.next_cursor ?? null,
    partial: pages.some((page) => page.partial),
  };
}

export type ManagerConnectionStatus =
  "connecting" | "connected" | "degraded" | "disconnected" | "restricted";

export function HAProxyView({
  organization,
  settingsContent,
  onConnectionStatus,
}: {
  organization: Organization;
  settingsContent?: ReactNode;
  onConnectionStatus?: (status: ManagerConnectionStatus) => void;
}) {
  const navigate = useNavigate();
  const location = useLocation();
  const basePath = "";
  const { runAction } = usePermissionAction();
  const read = can(organization, "haproxy.read");
  const manage = can(organization, "haproxy.manage");
  const [attempt, setAttempt] = useState(0);
  const [fleet, setFleet] = useState<HAProxyFleet | null>(null);
  const [loading, setLoading] = useState(true);
  const [connectionError, setConnectionError] = useState<string | null>(null);
  const [refreshError, setRefreshError] = useState<string | null>(null);
  const managerConnection: ManagerConnectionStatus = !read
    ? "restricted"
    : connectionError
      ? "degraded"
      : refreshError
        ? "degraded"
        : !fleet
          ? "connecting"
          : fleet.partial
            ? "degraded"
            : "connected";
  useEffect(() => {
    onConnectionStatus?.(managerConnection);
  }, [onConnectionStatus, managerConnection]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [dialog, setDialog] = useState<"cluster" | "enroll" | "revoke" | null>(
    null,
  );
  const [name, setName] = useState("");
  const [clusterId, setClusterId] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [fieldError, setFieldError] = useState<string | null>(null);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const clientRef = useRef<HAProxyClient | null>(null);
  const cursors = useRef<Array<string | undefined>>([undefined]);
  const loadingRef = useRef(false);
  const busyRef = useRef(false);
  const enrollmentBusy = useCallback((value: boolean) => {
    busyRef.current = value;
  }, []);

  useEffect(() => {
    if (!read || !manage) {
      setDialog(null);
      setDialogError(null);
    }
  }, [read, manage]);

  const reload = useCallback(
    async (client: HAProxyClient, next?: string, background = false) => {
      if (loadingRef.current || client.controller.signal.aborted) return;
      loadingRef.current = true;
      if (!background) setLoading(true);
      try {
        const requested = next ? [...cursors.current, next] : cursors.current;
        const pages: HAProxyFleet[] = [];
        for (const cursor of requested) pages.push(await client.fleet(cursor));
        if (clientRef.current !== client || client.controller.signal.aborted)
          return;
        const result = mergeFleet(pages);
        setFleet(result);
        cursors.current = requested;
        setSelectedId((current) =>
          result.nodes.some((node) => node.id === current) ? current : null,
        );
        setRefreshError(null);
      } finally {
        if (clientRef.current === client) {
          loadingRef.current = false;
          setLoading(false);
        }
      }
    },
    [],
  );

  useEffect(() => {
    if (!read) {
      setLoading(false);
      return;
    }
    const client = new HAProxyClient(organization.id);
    clientRef.current = client;
    cursors.current = [undefined];
    loadingRef.current = false;
    setFleet(null);
    setConnectionError(null);
    setLoading(true);
    const failConnection = (error: unknown) => {
      if (client.controller.signal.aborted || clientRef.current !== client)
        return;
      if (!(
        (error instanceof HAProxyError || error instanceof ApiError) &&
        (error.status === 401 || error.status === 403)
      )) {
        setRefreshError(actionError(error));
        setLoading(false);
        return;
      }
      setConnectionError(actionError(error));
      setFleet(null);
      setDialog(null);
      setLoading(false);
      client.dispose();
    };
    void client
      .connect()
      .then(() => reload(client))
      .catch(failConnection);
    const refresh = setInterval(() => {
      if (
        busyRef.current ||
        loadingRef.current ||
        client.controller.signal.aborted
      )
        return;
      void client
        .refresh()
        .then(() => reload(client, undefined, true))
        .catch(failConnection);
    }, 30_000);
    const observations = setInterval(() => {
      if (
        !busyRef.current &&
        !loadingRef.current &&
        !client.controller.signal.aborted
      )
        void reload(client, undefined, true).catch(failConnection);
    }, 5_000);
    return () => {
      clearInterval(observations);
      clearInterval(refresh);
      client.dispose();
      if (clientRef.current === client) clientRef.current = null;
    };
  }, [organization.id, read, attempt, reload]);

  const selected = fleet?.nodes.find((node) => node.id === selectedId) ?? null;
  function closeDialog() {
    if (busyRef.current) return;
    setDialog(null);
    setDialogError(null);
    setFieldError(null);
    setName("");
    setConfirmation("");
  }

  function openDialog(
    kind: "cluster" | "enroll" | "revoke",
    initialCluster?: string,
  ) {
    setName("");
    setConfirmation("");
    setDialogError(null);
    setFieldError(null);
    setClusterId(initialCluster ?? fleet?.clusters[0]?.id ?? "");
    setDialog(kind);
  }

  async function submit() {
    const client = clientRef.current;
    if (!client || busyRef.current || !manage) return;
    if (dialog === "cluster" && (!name.trim() || name.trim().length > 255)) {
      setFieldError("Enter a cluster name up to 255 characters.");
      return;
    }
    if (dialog === "revoke" && confirmation !== selected?.name) {
      setFieldError("Enter the node name exactly as shown.");
      return;
    }
    busyRef.current = true;
    setBusy(true);
    setDialogError(null);
    setFieldError(null);
    let createdCluster: string | undefined;
    try {
      await runAction(async () => {
        await client.login(true);
        if (dialog === "cluster")
          createdCluster = (await client.createCluster(name.trim())).cluster.id;
        if (dialog === "revoke" && selected) await client.revoke(selected.id);
      });
      if (client.controller.signal.aborted) return;
      await reload(client);
      if (createdCluster)
        navigate(
          haproxyPath(basePath, ["infrastructure", "clusters", createdCluster]),
        );
      if (dialog !== "enroll") {
        setDialog(null);
        setName("");
        setConfirmation("");
      }
    } catch (error) {
      if (!client.controller.signal.aborted && !isActionCancelled(error))
        setDialogError(actionError(error));
    } finally {
      if (clientRef.current === client) {
        busyRef.current = false;
        setBusy(false);
      }
    }
  }

  const permissionReason = !manage
    ? "Your role needs Manage HAProxy fleets permission."
    : undefined;
  const refreshFleet = async (next?: string) => {
    const client = clientRef.current;
    if (!client) return;
    try {
      await reload(client, next);
    } catch (error) {
      setRefreshError(actionError(error));
      throw error;
    }
  };
  const status = !read ? (
    <EmptyState
      icon={<ShieldCheck />}
      title="HAProxy access unavailable"
      description="Your role cannot view HAProxy fleets in this organization."
    />
  ) : connectionError ? (
    <EmptyState
      icon={<Directions />}
      title="HAProxy could not connect"
      description={connectionError}
      actions={
        <Button onPress={() => setAttempt((value) => value + 1)}>
          Reconnect
        </Button>
      }
    />
  ) : !fleet ? (
    <LoadingStatus>Connecting to HAProxy…</LoadingStatus>
  ) : null;
  return (
    <div className="cv-haproxy">
      {refreshError && (
        <ConnectionIssue
          connection
          message="Fleet inventory: refresh interrupted; retrying automatically."
        />
      )}
      {connectionError && (
        <ConnectionIssue
          connection
          message="Cannot connect to the Fleet manager. Check the manager service and your network connection."
        />
      )}
      <HAProxyWorkspace
        basePath={basePath}
        fleet={fleet}
        client={clientRef.current}
        loading={loading}
        status={status}
        settingsContent={settingsContent}
        permissionReason={permissionReason}
        onRefresh={() => {
          void refreshFleet().catch(() => {});
        }}
        onCreateCluster={() => openDialog("cluster")}
        onEnroll={(id) => openDialog("enroll", id)}
        onRevoke={(node) => {
          setSelectedId(node.id);
          openDialog("revoke");
        }}
        onLoadMore={() => refreshFleet(fleet?.next_cursor ?? undefined)}
      />
      {read && manage && dialog === "enroll" && clientRef.current && fleet && (
        <HAProxyEnrollment
          client={clientRef.current}
          clusters={fleet.clusters}
          initialCluster={clusterId}
          onClose={closeDialog}
          onBusy={enrollmentBusy}
          onConnected={(node) => {
            closeDialog();
            setSelectedId(node.id);
            navigate(
              haproxyPath(
                basePath,
                ["infrastructure", "nodes", node.id],
                location.search,
              ),
            );
            const client = clientRef.current;
            if (client)
              void reload(client).catch((error: unknown) =>
                setRefreshError(actionError(error)),
              );
          }}
        />
      )}
      <Drawer
        isOpen={read && manage && dialog !== null && dialog !== "enroll"}
        onOpenChange={(open) => {
          if (!open) closeDialog();
        }}
        isDismissable={!busy}
      >
        <DrawerDialog
          title={
            dialog === "cluster"
              ? "Create HAProxy cluster"
              : dialog === "enroll"
                ? "Enroll HAProxy node"
                : "Revoke HAProxy node"
          }
          hideCloseButton={busy}
        >
          <DialogForm onSubmit={() => void submit()}>
            <DrawerBody>
              {dialogError && (
                <InlineTip variant="error" role="alert">
                  {dialogError}
                </InlineTip>
              )}
              {dialog === "cluster" && (
                <TextField
                  label="Cluster name"
                  description="Groups existing HAProxy nodes. Traffic endpoints and failover are configured separately."
                  value={name}
                  onChange={setName}
                  isInvalid={!!fieldError}
                  errorMessage={fieldError ?? undefined}
                  errorDisplay="inline"
                  autoFocus
                />
              )}
              {dialog === "revoke" && selected && (
                <>
                  <InlineTip variant="warning" label="Revoke access">
                    The agent will lose management and telemetry access. HAProxy
                    keeps its applied configuration. Enrolling again requires a
                    new identity.
                  </InlineTip>
                  <TextField
                    label={`Enter ${selected.name} to confirm`}
                    value={confirmation}
                    onChange={setConfirmation}
                    isInvalid={!!fieldError}
                    errorMessage={fieldError ?? undefined}
                    errorDisplay="inline"
                    autoFocus
                  />
                </>
              )}
            </DrawerBody>
            <DrawerFooter>
              <Button
                type="submit"
                variant={dialog === "revoke" ? "danger" : "primary"}
                isLoading={busy}
              >
                {dialog === "cluster" ? "Create cluster" : "Revoke node"}
              </Button>
              <Button isDisabled={busy} onPress={closeDialog}>
                Cancel
              </Button>
            </DrawerFooter>
          </DialogForm>
        </DrawerDialog>
      </Drawer>
    </div>
  );
}

export { HAProxyObservation } from "./haproxy-node";
