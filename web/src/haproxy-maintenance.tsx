import { useEffect, useRef, useState } from "react";
import {
  Badge,
  Button,
  Modal,
  Dialog,
  DialogBody,
  DialogFooter,
  DialogForm,
  DescriptionItem,
  DescriptionList,
  InlineTip,
  LoadingStatus,
  Section,
  Select,
  Steps,
  Step,
  Toolbar,
} from "@ecraela/ui";
import {
  HAProxyClient,
  haproxyErrorMessage,
  type HAProxyMaintenance,
  type HAProxyMaintenanceView,
} from "./haproxy-api";
import { isActionCancelled, usePermissionAction } from "./permissions";

export const maintenanceStatus = (status: string): string =>
  (
    ({
      withdrawing: "Withdrawing from entrypoint",
      draining: "Draining connections",
      maintenance: "In maintenance",
      checking_rejoin: "Checking rejoin health",
      rejoining: "Rejoining traffic",
      attention: "Needs attention",
      completed: "Serving traffic",
      failed: "Failed",
    }) as Record<string, string>
  )[status] ?? "Serving traffic";
export const maintenanceTone = (status: string) =>
  status === "completed"
    ? ("success" as const)
    : status === "attention" || status === "failed"
      ? ("error" as const)
      : ("warning" as const);
export const maintenanceError = (code: string): string =>
  (
    ({
      maintenance_deadline:
        "The deadline passed. The node remains reserved for maintenance. Check its connection count and entrypoint state before retrying.",
      entrypoint_unavailable:
        "The entrypoint agent is unavailable. Isolation cannot be verified until it reconnects.",
      entrypoint_configuration_changed:
        "The entrypoint configuration changed outside maintenance. Restore its expected configuration before retrying.",
      entrypoint_binding_changed:
        "The entrypoint binding changed. Check the declared topology before retrying.",
      entrypoint_not_withdrawn:
        "The entrypoint is no longer verified as withdrawn. Check its saved and running configuration.",
      entrypoint_changed:
        "The entrypoint changed before the operation could start. Refresh its state and retry.",
      target_changed:
        "The node restarted or changed during its health checks. Retry with its current state.",
      operation_unverified:
        "The entrypoint operation could not be verified. Its outcome must settle before another change.",
      operation_failed:
        "The entrypoint rejected or could not verify the operation. Check its configuration history before retrying.",
      rejoin_failed:
        "Rejoin health checks failed. The manager is withdrawing the node again and preserving its maintenance reservation.",
    }) as Record<string, string>
  )[code] ??
  "Maintenance needs attention. Check the node and entrypoint before retrying.";
export function maintenanceReason(
  view: HAProxyMaintenanceView | null,
  permissionReason?: string,
  now = Date.now(),
): string | undefined {
  if (permissionReason) return permissionReason;
  if (!view) return "Wait for maintenance discovery.";
  if (view.reason_code === "entrypoint_not_managed")
    return "Bind an enrolled entrypoint agent to this cluster to enable node maintenance.";
  if (view.reason_code) return "Wait for the entrypoint agent to reconnect.";
  for (const control of [view.target, view.controller]) {
    if (control.active_rollout_id)
      return "Wait for the cluster configuration update to finish.";
    if (
      control.maintenance_blocked &&
      control.maintenance_id !== view.maintenance?.id
    )
      return "Another node is already reserved for maintenance.";
    if (
      !control.discovery ||
      !!control.discovery.error_code ||
      now - Date.parse(control.last_seen) > 6000 ||
      now - control.discovery.observed_at_ms > 6000
    )
      return "Wait for fresh node and entrypoint discovery.";
  }
  const controller = view.controller.discovery!;
  if (
    !controller.configuration ||
    now - controller.configuration.observed_at_ms > 6000
  )
    return "Wait for fresh saved entrypoint configuration.";
  if (!controller.control_allowed || !controller.configuration?.control_allowed)
    return "Enable runtime and persistent configuration control on the entrypoint agent.";
  if (
    !view.target.discovery!.proxies.some(
      (p) =>
        p.kind === "frontend" &&
        p.name === view.frontend &&
        p.active_connections !== undefined,
    )
  )
    return "Upgrade the node agent to report traffic frontend connections.";
  return undefined;
}
export function maintenanceConfirmation(
  action: "withdraw" | "rejoin",
  nodeName: string,
  controllerName: string,
  timeout: number,
) {
  return action === "withdraw"
    ? {
        title: "Start node maintenance?",
        confirmLabel: "Start maintenance",
        description: `Withdraw ${nodeName} from ${controllerName || "the shared entrypoint"}. New traffic will use the remaining available nodes. Allow up to ${timeout} seconds for existing connections to drain; connections are not forcibly closed. The node stays withdrawn until you rejoin it.`,
      }
    : {
        title: "Rejoin traffic?",
        confirmLabel: "Check health and rejoin",
        description: `Check ${nodeName}'s health, then allow new traffic through ${controllerName || "the shared entrypoint"}. The manager verifies health before and after rejoining, and withdraws the node again if verification fails.`,
      };
}

export function HAProxyMaintenancePanel({
  nodeId,
  nodeName = nodeId,
  client,
  permissionReason,
}: {
  nodeId: string;
  nodeName?: string | undefined;
  client: HAProxyClient;
  permissionReason?: string;
}) {
  const { runAction } = usePermissionAction();
  const [data, setData] = useState<HAProxyMaintenanceView | null>(
    () => client.cachedMaintenance(nodeId) ?? null,
  );
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState<"withdraw" | "rejoin" | null>(null);
  const [timeout, setTimeoutSeconds] = useState("30");
  const busyRef = useRef(false);
  const refresh = useRef<() => Promise<void>>(async () => {});
  const lifetime = useRef<AbortController | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    lifetime.current = controller;
    let pending = false;
    const update = async () => {
      if (
        pending ||
        controller.signal.aborted ||
        document.visibilityState === "hidden"
      )
        return;
      pending = true;
      try {
        const next = await client.maintenance(nodeId, controller.signal);
        if (!controller.signal.aborted) {
          setData(next);
          setError(null);
        }
      } catch (cause) {
        if (!controller.signal.aborted) setError(haproxyErrorMessage(cause));
      } finally {
        pending = false;
      }
    };
    refresh.current = update;
    void update();
    const timer = setInterval(() => void update(), 1000);
    const resume = () => void update();
    document.addEventListener("visibilitychange", resume);
    return () => {
      controller.abort();
      clearInterval(timer);
      document.removeEventListener("visibilitychange", resume);
      lifetime.current = null;
    };
  }, [client, nodeId]);
  const current = data?.maintenance;
  const held = current && !["completed", "failed"].includes(current.status);
  const idle =
    !held || current.status === "maintenance" || current.status === "attention";
  const reason =
    maintenanceReason(data, permissionReason) ??
    (error
      ? "Refresh maintenance discovery before changing this node."
      : undefined);
  const apply = async (action: "withdraw" | "rejoin") => {
    if (
      busyRef.current ||
      confirm !== action ||
      reason ||
      !idle ||
      !lifetime.current ||
      !data
    )
      return;
    busyRef.current = true;
    setBusy(true);
    setActionError(null);
    const signal = lifetime.current.signal;
    // Retain the request identity if the normal shared-login MFA flow retries.
    const requestId = crypto.randomUUID();
    try {
      await runAction(async () => {
        await client.login(true);
        signal.throwIfAborted();
        const latest = await client.maintenance(nodeId, signal);
        const blocked = maintenanceReason(latest, permissionReason);
        if (blocked) throw new Error(blocked);
        const latestCurrent = latest.maintenance;
        const latestHeld =
          latestCurrent &&
          !["completed", "failed"].includes(latestCurrent.status);
        if (
          (action === "rejoin" &&
            (!latestHeld ||
              !["maintenance", "attention"].includes(latestCurrent.status))) ||
          (action === "withdraw" &&
            latestHeld &&
            latestCurrent.status !== "attention")
        )
          throw new Error(
            "Maintenance state changed. Close this confirmation and check the current node state.",
          );
        const target = latest.target.discovery!,
          controller = latest.controller.discovery!,
          saved = controller.configuration!;
        await client.changeMaintenance(nodeId, latest.cluster_id, {
          request_id: requestId,
          action,
          ...(latestHeld && latestCurrent
            ? { maintenance_id: latestCurrent.id }
            : {}),
          binding_id: latest.binding_id!,
          expected_generation: target.generation,
          expected_entrypoint_generation: controller.generation,
          expected_artifact_id: saved.artifact_id,
          expected_version: saved.version,
          drain_timeout_seconds: Number(timeout),
        });
      });
      if (!signal.aborted) {
        setConfirm(null);
        await refresh.current();
      }
    } catch (cause) {
      if (!signal.aborted && !isActionCancelled(cause))
        setActionError(
          cause instanceof Error ? cause.message : haproxyErrorMessage(cause),
        );
    } finally {
      busyRef.current = false;
      if (!signal.aborted) setBusy(false);
    }
  };
  const confirmation = maintenanceConfirmation(
    confirm ?? "withdraw",
    nodeName,
    data?.controller_name ?? "",
    Number(timeout),
  );
  const observedConnections =
    data?.target.discovery &&
    Date.now() - Date.parse(data.target.last_seen) <= 6000 &&
    Date.now() - data.target.discovery.observed_at_ms <= 6000
      ? data.target.discovery.proxies.find(
          (p) => p.kind === "frontend" && p.name === data.frontend,
        )?.active_connections
      : undefined;
  const phase =
    current?.status === "completed"
      ? 4
      : current?.status === "rejoining" || current?.status === "checking_rejoin"
        ? 3
        : current?.status === "maintenance"
          ? 2
          : current?.status === "draining"
            ? 1
            : 0;
  return (
    <Section
      layout="stack"
      size="sm"
      title="Node maintenance"
      description="Withdraw this node from the shared entrypoint, let existing traffic finish, then explicitly rejoin after health checks."
    >
      <div className="cv-haproxy-configuration">
        {error && (
          <InlineTip variant="warning">
            {error} Last received progress remains visible.
          </InlineTip>
        )}
        {!data && !error && (
          <LoadingStatus delayMs={1000}>
            Loading maintenance state…
          </LoadingStatus>
        )}
        {data && (
          <>
            <DescriptionList columns={3}>
              <DescriptionItem label="Traffic state">
                <Badge
                  tone={
                    held && current
                      ? maintenanceTone(current.status)
                      : "neutral"
                  }
                >
                  {current
                    ? maintenanceStatus(current.status)
                    : "No maintenance reservation"}
                </Badge>
              </DescriptionItem>
              <DescriptionItem label="Managed entrypoint">
                {data.controller_name || "Not bound"}
              </DescriptionItem>
              <DescriptionItem label="Traffic connections">
                {observedConnections ?? "Unknown"}
                {data.frontend && ` on ${data.frontend}`}
              </DescriptionItem>
            </DescriptionList>
            {held && (
              <Steps density="compact">
                {[
                  [
                    "Withdraw from entrypoint",
                    "Block new traffic and save the withdrawal across entrypoint restarts.",
                  ],
                  [
                    "Drain existing connections",
                    "Wait for zero traffic frontend connections for 3 seconds. Connections are never forcibly closed.",
                  ],
                  [
                    "Maintain node",
                    "The node stays withdrawn until you choose Rejoin traffic. You can edit this node’s configuration here.",
                  ],
                  [
                    "Check health and rejoin",
                    "Require 5 seconds of healthy observations before and after rejoining. Failed rejoin checks withdraw the node again.",
                  ],
                ].map(([title, description], index) => (
                  <Step
                    key={title}
                    title={title}
                    description={description}
                    status={
                      phase > index
                        ? "complete"
                        : phase === index
                          ? "current"
                          : "upcoming"
                    }
                  />
                ))}
              </Steps>
            )}
            {current?.error_code && (
              <InlineTip
                variant={current.status === "attention" ? "error" : "warning"}
              >
                {maintenanceError(current.error_code)}
              </InlineTip>
            )}
            {actionError && !confirm && (
              <InlineTip variant="error" role="alert">
                {actionError}
              </InlineTip>
            )}
            <Toolbar>
              {(!held || current?.status === "attention") && (
                <Select
                  label="Drain deadline"
                  selectedKey={timeout}
                  options={[
                    { id: "30", label: "30 seconds" },
                    { id: "60", label: "1 minute" },
                    { id: "300", label: "5 minutes" },
                  ]}
                  onSelectionChange={(key) => setTimeoutSeconds(String(key))}
                />
              )}
              {(!held || current?.status === "attention") && (
                <Button
                  disabledReason={
                    reason ??
                    (!idle
                      ? "Wait for the current maintenance stage to finish."
                      : undefined)
                  }
                  isDisabled={busy}
                  onPress={() => {
                    setActionError(null);
                    setConfirm("withdraw");
                  }}
                >
                  {held ? "Retry withdrawal" : "Start node maintenance"}
                </Button>
              )}
              {held && (
                <Button
                  variant="primary"
                  disabledReason={
                    reason ??
                    (!idle
                      ? "Wait for the current maintenance stage to finish."
                      : undefined)
                  }
                  isDisabled={busy}
                  onPress={() => {
                    setActionError(null);
                    setConfirm("rejoin");
                  }}
                >
                  Rejoin traffic
                </Button>
              )}
            </Toolbar>
            {reason && <InlineTip>{reason}</InlineTip>}
            {current && (
              <p className="cv-haproxy-caption" role="status">
                Updated {new Date(current.updated_at).toLocaleTimeString()}.
                Progress continues when you leave this page.
              </p>
            )}
          </>
        )}
      </div>
      <Modal
        size="md"
        isOpen={confirm !== null}
        isDismissable={!busy}
        isKeyboardDismissDisabled={busy}
        onOpenChange={(open) => {
          if (!open && !busy) setConfirm(null);
        }}
      >
        <Dialog
          title={confirmation.title}
          description={nodeName}
          role="alertdialog"
          hideCloseButton={busy}
        >
          <DialogForm
            onSubmit={() => {
              if (confirm) void apply(confirm);
            }}
          >
            <DialogBody>
              {actionError && (
                <InlineTip variant="error" role="alert">
                  {actionError}
                </InlineTip>
              )}
              <p className="cv-dialog__text">{confirmation.description}</p>
            </DialogBody>
            <DialogFooter>
              <Button
                type="submit"
                variant={confirm === "withdraw" ? "danger" : "primary"}
                isLoading={busy}
                disabledReason={
                  reason ??
                  (!idle
                    ? "Wait for the current maintenance stage to finish."
                    : undefined)
                }
              >
                {confirmation.confirmLabel}
              </Button>
              <Button isDisabled={busy} onPress={() => setConfirm(null)}>
                Cancel
              </Button>
            </DialogFooter>
          </DialogForm>
        </Dialog>
      </Modal>
    </Section>
  );
}
