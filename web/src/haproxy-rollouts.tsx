import { ConnectionIssue } from "./connection-health";
import { useEffect, useRef, useState } from "react";
import {
  Badge,
  PaginatedDataTable,
  DescriptionItem,
  DescriptionList,
  InlineTip,
  LoadingStatus,
  Section,
  Select,
} from "@ecraela/ui";
import {
  haproxyErrorMessage,
  type HAProxyClient,
  type HAProxyRollout,
  type HAProxyApplicationVerification,
} from "./haproxy-api";
import { maintenanceError } from "./haproxy-maintenance";

export function rolloutStatus(status: string): string {
  return (
    (
      {
        running: "Updating nodes",
        rolling_back: "Rolling back",
        succeeded: "Completed",
        rolled_back: "Rolled back",
        rollback_failed: "Recovery needs attention",
        pending: "Pending",
        waiting: "Waiting for healthy nodes",
        applying: "Applying configuration",
        checking: "Checking health",
        checking_application: "Checking application requests",
        verifying_application: "Verifying requests after rejoin",
        verifying_application_rollback: "Verifying restored application",
        applied: "Applied",
        withdrawing: "Withdrawing from entrypoint",
        draining: "Draining connections",
        rejoining: "Rejoining traffic",
        rejoining_rollback: "Rejoining after rollback",
        waiting_for_rollback: "Waiting for node to reconnect",
        checking_rollback: "Verifying rollback",
        skipped: "Not changed",
      } as Record<string, string>
    )[status] ?? "Waiting"
  );
}
export function rolloutError(code: string): string {
  return (
    (
      {
        preflight_health_failed:
          "A node was not healthy before the next update.",
        peer_health_failed: "A peer stopped reporting healthy state.",
        health_check_failed: "The updated node did not remain healthy.",
        application_http_failed:
          "An application HTTP check failed on the updated node.",
        application_https_failed:
          "An application HTTPS check failed on the updated node.",
        application_entrypoint_http_failed:
          "An application HTTP check through the shared entrypoint failed after rejoin.",
        application_entrypoint_https_failed:
          "An application HTTPS check through the shared entrypoint failed after rejoin.",
        application_entrypoint_verification_timeout:
          "Required entrypoint checks did not report two fresh passes before the deadline.",
        application_entrypoint_probe_policy_changed:
          "An agent’s local entrypoint check policy changed during the update.",
        application_verification_timeout:
          "Required application checks did not report two fresh passes before the deadline.",
        application_probe_policy_changed:
          "An agent’s local check policy changed during the update.",
        rollback_application_verification_failed:
          "The restored application did not pass node or entrypoint request verification. The node remains withdrawn for recovery.",
        rollback_entrypoint_verification_failed:
          "All changed nodes were restored and directly verified, but the shared entrypoint still fails. Restored nodes remain in traffic; investigate the entrypoint path.",
        cluster_health_failed: "The cluster was not healthy at completion.",
        configuration_changed: "Configuration changed outside this update.",
        cluster_membership_changed:
          "Cluster membership changed during this update.",
        configuration_rejected: "HAProxy rejected the configuration.",
        configuration_reload_failed:
          "HAProxy could not reload the new settings.",
        verification_deadline:
          "The node update could not be verified before its deadline.",
        node_unavailable: "The node is unavailable for management.",
        rollback_health_failed:
          "The restored node did not pass health verification.",
        rollback_deadline:
          "The node could not be restored before the recovery deadline.",
        rollout_deadline: "The rollout exceeded its deadline.",
        control_reconnected: "The node connection changed during the update.",
        rollback_operation_failed:
          "The previous configuration could not be restored.",
      } as Record<string, string>
    )[code] ??
    (code
      ? [
          "maintenance_deadline",
          "entrypoint_unavailable",
          "entrypoint_configuration_changed",
          "entrypoint_binding_changed",
          "entrypoint_not_withdrawn",
          "entrypoint_changed",
          "target_changed",
          "operation_unverified",
          "operation_failed",
          "rejoin_failed",
        ].includes(code)
        ? maintenanceError(code)
        : "The node could not complete its update. Check its configuration history."
      : "")
  );
}
export function rolloutRequestResult(
  check: HAProxyApplicationVerification["checks"][number],
  gate: HAProxyApplicationVerification,
): string {
  if (check.error_code === "tls_failed") return "TLS verification failed";
  if (check.error_code === "response_status")
    return `HTTP ${check.status_code}; expected ${check.expected_status}`;
  if (check.error_code === "response_mismatch") return "Response did not match";
  if (check.error_code === "probe_policy_changed")
    return "Local policy changed";
  if (check.error_code) return "Check failed";
  if (check.passes >= 2) return "Passed";
  if (gate.status === "failed") {
    if (gate.error_code.endsWith("verification_timeout")) return "Timed out";
    return check.passes > 0
      ? `HTTP ${check.status_code} (${check.passes} / 2 passes)`
      : "Not completed";
  }
  return `${check.passes} / 2 passes`;
}
export function rolloutRequestRows(rollout: HAProxyRollout) {
  return rollout.nodes.flatMap((node) =>
    [
      { direction: "Deployment", gate: node.application_verification },
      { direction: "Rollback", gate: node.rollback_application_verification },
    ].flatMap(({ direction, gate }) =>
      gate
        ? gate.checks.map((check) => ({
            ...check,
            id: `${node.node_id}/${direction}/${check.name}`,
            node: node.name,
            direction,
            location:
              check.scope === "entrypoint" ? "Entrypoint" : "Direct node",
            phase:
              gate.phase === "before_rejoin"
                ? "Before rejoin"
                : gate.phase === "after_cluster_restore"
                  ? "Cluster restored"
                  : "After rejoin",
            result: rolloutRequestResult(check, gate),
          }))
        : [],
    ),
  );
}
const tone = (status: string) =>
  status === "succeeded" || status === "applied" || status === "rolled_back"
    ? ("success" as const)
    : status === "rollback_failed"
      ? ("error" as const)
      : status === "rolling_back"
        ? ("warning" as const)
        : ("neutral" as const);
const recoveryCompleted = (node: HAProxyRollout["nodes"][number]) =>
  !!node.recovery_maintenance_id &&
  [node.maintenance, node.rollback_maintenance].some(
    (maintenance) =>
      maintenance?.id === node.recovery_maintenance_id &&
      maintenance?.status === "completed",
  );
const maintenanceRecovered = (rollout: HAProxyRollout) => {
  const failed = rollout.nodes.filter(
    (node) => node.recovery_maintenance_id || node.status === "rollback_failed",
  );
  return (
    rollout.status === "rollback_failed" &&
    failed.length > 0 &&
    failed.every(recoveryCompleted)
  );
};
const updateLabel = (rollout: HAProxyRollout) =>
  maintenanceRecovered(rollout)
    ? "Stopped (maintenance recovered)"
    : rolloutStatus(rollout.status);
export function HAProxyRollouts({
  clusterId,
  client,
}: {
  clusterId: string;
  client: HAProxyClient;
}) {
  const [rows, setRows] = useState<HAProxyRollout[] | null>(
    () => client.cachedRollouts(clusterId) ?? null,
  );
  const [selected, setSelected] = useState("");
  const head = useRef(rows?.[0]?.id);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    const controller = new AbortController();
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
        const data = await client.rollouts(clusterId, controller.signal);
        if (!controller.signal.aborted) {
          if (head.current !== data[0]?.id) {
            head.current = data[0]?.id;
            setSelected((selected) =>
              data.some((row) => row.id === selected) ? selected : "",
            );
          }
          setRows(data);
          setError(null);
        }
      } catch (cause) {
        if (!controller.signal.aborted) setError(haproxyErrorMessage(cause));
      } finally {
        pending = false;
      }
    };
    const resume = () => void update();
    void update();
    const timer = setInterval(resume, 2000);
    document.addEventListener("visibilitychange", resume);
    return () => {
      controller.abort();
      clearInterval(timer);
      document.removeEventListener("visibilitychange", resume);
    };
  }, [client, clusterId]);
  const current = rows?.find((row) => row.id === selected) ?? rows?.[0];
  const recovered = current ? maintenanceRecovered(current) : false;
  return (
    <Section
      layout="stack"
      size="sm"
      title="Rolling configuration"
      description="Withdraw, drain, update, check health and rejoin each node before continuing. Rollback follows the same sequence. Start from a node’s Edit backend form."
    >
      <div className="cv-haproxy-configuration">
        {error && (
          <ConnectionIssue message="Rollout progress: refresh interrupted; retrying automatically." />
        )}
        {!rows && !error && (
          <LoadingStatus delayMs={1000}>Loading cluster updates…</LoadingStatus>
        )}
        {rows?.length === 0 && (
          <InlineTip>
            No cluster updates yet. Select a node, edit its backend settings,
            and choose Roll out to all nodes.
          </InlineTip>
        )}
        {current && (
          <>
            <Select
              label="Cluster update"
              selectedKey={current.id}
              options={(rows ?? []).map((row) => ({
                id: row.id,
                label: `${new Date(row.created_at).toLocaleString()} – ${updateLabel(row)}`,
              }))}
              onSelectionChange={(key) => setSelected(String(key))}
            />
            <DescriptionList columns={3}>
              <DescriptionItem label="Status">
                <Badge tone={recovered ? "warning" : tone(current.status)}>
                  {updateLabel(current)}
                </Badge>
              </DescriptionItem>
              <DescriptionItem label="Nodes applied">
                {
                  current.nodes.filter((node) => node.status === "applied")
                    .length
                }{" "}
                / {current.nodes.length}
              </DescriptionItem>
              <DescriptionItem label="Health window">
                {current.entrypoint
                  ? "5 seconds before and after rejoin"
                  : "5 seconds per node"}
              </DescriptionItem>
              <DescriptionItem label="Application verification">
                {current.nodes.some(
                  (n) => (n.application_checks?.length ?? 0) > 0,
                )
                  ? current.nodes.some((n) =>
                      n.application_checks?.some(
                        (c) => c.scope === "entrypoint",
                      ),
                    )
                    ? "Two node passes before and after rejoin; two entrypoint passes after rejoin"
                    : "Two node passes before and after rejoin"
                  : "Node health checks"}
              </DescriptionItem>
            </DescriptionList>
            {recovered ? (
              <InlineTip variant="warning">
                This update stopped before completion. Maintenance recovery
                finished and affected nodes rejoined traffic. Check per-node
                version history before retrying; nodes updated before the stop
                may keep their applied version.
              </InlineTip>
            ) : current.error_code ? (
              <InlineTip
                variant={
                  current.status === "rollback_failed" ? "error" : "warning"
                }
              >
                {rolloutError(current.error_code)}{" "}
                {current.status === "rolled_back"
                  ? "Changed nodes were restored to their starting versions."
                  : current.status === "rolling_back"
                    ? "Changed nodes are being restored in reverse order."
                    : current.status === "rollback_failed"
                      ? "Check failed nodes and their version history before another change."
                      : ""}
              </InlineTip>
            ) : null}
            {current.nodes.some(
              (node) =>
                node.recovery_maintenance_id && !recoveryCompleted(node),
            ) && (
              <InlineTip variant="warning">
                The rollout stopped with a node reserved for maintenance. Select
                that node and use Node maintenance to inspect or retry its drain
                and rejoin. Its peer continues serving traffic.
              </InlineTip>
            )}
            <PaginatedDataTable
              paginationKey={current.id}
              aria-label="HAProxy cluster rollout nodes"
              itemLabel="nodes"
              rows={current.nodes.map((node) => ({
                ...node,
                id: node.node_id,
              }))}
              rowLabel={(node) => node.name}
              columns={[
                {
                  id: "node",
                  header: "Node",
                  isRowHeader: true,
                  cell: (node) => node.name,
                },
                {
                  id: "state",
                  header: "Progress",
                  cell: (node) => (
                    <Badge
                      tone={
                        recoveryCompleted(node) ? "success" : tone(node.status)
                      }
                    >
                      {recoveryCompleted(node)
                        ? "Rejoined after recovery"
                        : rolloutStatus(node.status)}
                    </Badge>
                  ),
                },
                {
                  id: "connections",
                  header: "Connections to drain",
                  cell: (node) => {
                    const maintenance =
                      current.status === "rolling_back" ||
                      current.status === "rolled_back" ||
                      current.status === "rollback_failed"
                        ? (node.rollback_maintenance ?? node.maintenance)
                        : node.maintenance;
                    return maintenance?.status === "completed"
                      ? "Rejoined"
                      : maintenance?.connections !== undefined
                        ? maintenance.connections
                        : maintenance
                          ? "Waiting for observation"
                          : "—";
                  },
                },
                {
                  id: "health",
                  header: "Health verification",
                  cell: (node) => {
                    if (recoveryCompleted(node)) return "Passed";
                    const maintenance =
                      node.rollback_maintenance ?? node.maintenance;
                    if (
                      maintenance &&
                      (maintenance.status === "checking_rejoin" ||
                        maintenance.status === "rejoining")
                    ) {
                      if (
                        maintenance.status === "rejoining" &&
                        maintenance.step !== "verify"
                      )
                        return "Waiting for entrypoint";
                      const seconds = Math.min(
                        5,
                        Math.max(
                          0,
                          Math.floor(
                            ((maintenance.last_health_ms ?? 0) -
                              (maintenance.health_since_ms ?? 0)) /
                              1000,
                          ),
                        ),
                      );
                      return `${maintenance.status === "checking_rejoin" ? "Before rejoin" : "After rejoin"}: ${seconds} / 5 seconds`;
                    }
                    return node.status === "checking" ||
                      node.status === "checking_rollback"
                      ? `${Math.min(5, Math.max(0, Math.floor((node.last_health_ms - node.health_since_ms) / 1000)))} / 5 seconds`
                      : node.status === "applied" ||
                          node.status === "rolled_back"
                        ? "Passed"
                        : "Waiting";
                  },
                },
                {
                  id: "error",
                  header: "Details",
                  cell: (node) =>
                    recoveryCompleted(node)
                      ? node.error_code === "maintenance_deadline"
                        ? "The maintenance deadline passed. Recovery later completed."
                        : "The update stopped. Maintenance recovery later completed."
                      : rolloutError(node.error_code) || "—",
                },
              ]}
            />
            {rolloutRequestRows(current).length > 0 && (
              <Section
                layout="stack"
                size="sm"
                title="Application request verification"
                description="Direct checks verify the updated node before and after rejoin. When enabled, entrypoint checks must also pass after rejoin before the next node is changed. These are checks from agent locations, not proof of public reachability."
              >
                <PaginatedDataTable
                  paginationKey={current.id}
                  aria-label="HAProxy rollout application checks"
                  itemLabel="checks"
                  rows={rolloutRequestRows(current)}
                  rowLabel={(row) => `${row.node} ${row.direction} ${row.name}`}
                  columns={[
                    {
                      id: "check",
                      header: "Check",
                      isRowHeader: true,
                      cell: (row) => row.name,
                    },
                    { id: "node", header: "Node", cell: (row) => row.node },
                    {
                      id: "location",
                      header: "Path",
                      cell: (row) => row.location,
                    },
                    {
                      id: "direction",
                      header: "Operation",
                      cell: (row) => row.direction,
                    },
                    { id: "phase", header: "Phase", cell: (row) => row.phase },
                    {
                      id: "request",
                      header: "Request",
                      cell: (row) => `${row.scheme.toUpperCase()} ${row.path}`,
                    },
                    {
                      id: "result",
                      header: "Result",
                      cell: (row) => (
                        <Badge
                          tone={
                            row.result === "Passed"
                              ? "success"
                              : row.result.includes("passes") ||
                                  row.result === "Not completed"
                                ? "neutral"
                                : "error"
                          }
                        >
                          {row.result}
                        </Badge>
                      ),
                    },
                  ]}
                />
              </Section>
            )}
          </>
        )}
      </div>
    </Section>
  );
}
