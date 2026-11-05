import { useEffect, useRef, useState } from "react";
import {
  Badge,
  Button,
  PaginatedDataTable,
  DescriptionItem,
  DescriptionList,
  DrawerDialog,
  DrawerBody,
  DrawerFooter,
  DialogForm,
  EmptyState,
  InlineTip,
  LoadingStatus,
  MenuItem,
  Drawer,
  Section,
  TextField,
} from "@ecraela/ui";
import { ApiError } from "./api";
import {
  HAProxyClient,
  haproxyErrorMessage,
  type HAProxyBackendServer,
  type HAProxyNodeDiscovery,
} from "./haproxy-api";
import { isActionCancelled, usePermissionAction } from "./permissions";
import { HAProxyAgentVersion } from "./haproxy-agent-version";
import { HAProxyNodeSetupPanel } from "./haproxy-node-setup";

export function runtimeOperationMessage(code: string): string {
  const messages: Record<string, string> = {
    generation_changed:
      "HAProxy or its configuration changed. Refresh discovery and try again.",
    state_changed:
      "This server changed after the operation was requested. Check its current state.",
    last_available_server:
      "Another healthy, enabled server is required in this backend pool.",
    target_missing: "This backend server is no longer present.",
    runtime_rejected: "HAProxy rejected the runtime change.",
    runtime_unavailable:
      "The agent could not reach HAProxy’s local Runtime API.",
    verification_deadline:
      "The deadline passed before the result could be verified. Check the current state.",
    control_reconnected:
      "The control connection changed during the operation. Check the current state.",
    outcome_unverified:
      "The operation’s outcome could not be verified. Check the current state before another change.",
    control_disabled: "Runtime control is disabled on this node.",
    journal_unavailable: "The agent could not record the operation locally.",
  };
  return (
    messages[code] ??
    "The operation could not be verified. Check the current state."
  );
}
export function runtimeControlReason(
  data: HAProxyNodeDiscovery | null,
  permissionReason?: string,
  now = Date.now(),
): string | undefined {
  if (permissionReason) return permissionReason;
  if (data?.control.maintenance_blocked)
    return "Whole-node maintenance is active. Follow its progress under Node maintenance.";
  if (data?.control.active_rollout_id)
    return "Wait for the cluster update to finish.";
  if (!data?.connected || now - Date.parse(data.control.last_seen) > 6000)
    return "Wait for the agent’s control connection.";
  if (
    !data.fresh ||
    !data.control.discovery ||
    now - data.control.discovery.observed_at_ms > 6000
  )
    return "Wait for fresh configuration discovery.";
  if (!data.control.discovery.control_allowed)
    return "The agent must allow runtime control for this supported HAProxy version.";
  if (
    data.operations.some(
      (op) => op.status === "queued" || op.status === "running",
    )
  )
    return "Wait for the current HAProxy change to finish.";
  return undefined;
}
type WeightEdit = {
  target: HAProxyBackendServer;
  generation: string;
  requestID: string;
};

export function hasOtherCapacity(
  discovery: HAProxyNodeDiscovery["control"]["discovery"] | undefined,
  target: HAProxyBackendServer,
): boolean {
  return !!discovery?.servers.some(
    (server) =>
      server.backend === target.backend &&
      server.name !== target.name &&
      server.state === "ready" &&
      server.weight > 0 &&
      server.health.startsWith("UP"),
  );
}

export function HAProxyRuntime({
  nodeId,
  client,
  permissionReason,
}: {
  nodeId: string;
  client: HAProxyClient;
  permissionReason?: string;
}) {
  const { runAction } = usePermissionAction();
  const [data, setData] = useState<HAProxyNodeDiscovery | null>(
    () => client.cachedDiscovery(nodeId) ?? null,
  );
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [edit, setEdit] = useState<WeightEdit | null>(null);
  const [weight, setWeight] = useState("");
  const [fieldError, setFieldError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [now, setNow] = useState(Date.now());
  const busyRef = useRef(false);
  const lifetime = useRef<AbortController | null>(null);
  const refreshRef = useRef<() => Promise<void>>(async () => {});
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
        const next = await client.discovery(nodeId, controller.signal);
        if (!controller.signal.aborted) {
          setData(next);
          setError(null);
        }
      } catch (reason) {
        if (!controller.signal.aborted) setError(haproxyErrorMessage(reason));
      } finally {
        pending = false;
      }
    };
    refreshRef.current = update;
    const resume = () => {
      if (document.visibilityState !== "hidden") void update();
    };
    document.addEventListener("visibilitychange", resume);
    void update();
    const timer = setInterval(() => {
      if (document.visibilityState !== "hidden") {
        setNow(Date.now());
        void update();
      }
    }, 2000);
    return () => {
      controller.abort();
      clearInterval(timer);
      document.removeEventListener("visibilitychange", resume);
      lifetime.current = null;
    };
  }, [client, nodeId]);
  const discovery = data?.control.discovery;
  const reason = runtimeControlReason(data, permissionReason, now);
  const openWeight = (target: HAProxyBackendServer) => {
    setEdit({
      target,
      generation: discovery?.generation ?? "",
      requestID: crypto.randomUUID(),
    });
    setWeight(String(target.weight));
    setFieldError(null);
    setActionError(null);
  };
  const close = () => {
    if (!busyRef.current) {
      setEdit(null);
      setActionError(null);
    }
  };
  const currentTarget =
    edit &&
    discovery?.servers.find(
      (server) =>
        server.backend === edit.target.backend &&
        server.name === edit.target.name,
    );
  const editReason =
    reason ??
    (edit &&
    (edit.generation !== discovery?.generation ||
      !currentTarget ||
      currentTarget.state !== edit.target.state ||
      currentTarget.weight !== edit.target.weight)
      ? "This server changed while you were editing. Reopen the weight editor to use its current state."
      : undefined) ??
    (edit && Number(weight) === 0 && !hasOtherCapacity(discovery, edit.target)
      ? "Another healthy, enabled server must remain in this backend pool."
      : undefined);
  const apply = async (
    kind: "drain" | "restore" | "weight",
    target: HAProxyBackendServer,
    value = 0,
  ) => {
    if (busyRef.current || !lifetime.current || lifetime.current.signal.aborted)
      return;
    const blocked =
      kind === "weight"
        ? editReason
        : runtimeControlReason(data, permissionReason);
    if (blocked) {
      setActionError(blocked);
      return;
    }
    if (
      (kind === "drain" || (kind === "weight" && value === 0)) &&
      !hasOtherCapacity(discovery, target)
    ) {
      setActionError(
        "Another healthy, enabled server must remain in this backend pool.",
      );
      return;
    }
    const input = {
      request_id:
        kind === "weight" && edit ? edit.requestID : crypto.randomUUID(),
      kind,
      backend: target.backend,
      server: target.name,
      weight: value,
      expected_generation:
        kind === "weight" && edit
          ? edit.generation
          : (discovery?.generation ?? ""),
      expected_state: target.state,
      expected_weight: target.weight,
    };
    busyRef.current = true;
    setBusy(true);
    setActionError(null);
    const signal = lifetime.current.signal;
    try {
      await runAction(async () => {
        await client.login(true);
        signal.throwIfAborted();
        const operation = await client.operate(nodeId, input);
        if (!signal.aborted)
          setData((previous) =>
            previous
              ? {
                  ...previous,
                  operations: [
                    operation,
                    ...previous.operations.filter(
                      (op) => op.id !== operation.id,
                    ),
                  ],
                }
              : previous,
          );
      });
      if (!signal.aborted) {
        setEdit(null);
        await refreshRef.current();
      }
    } catch (cause) {
      if (!signal.aborted && !isActionCancelled(cause))
        setActionError(
          cause instanceof ApiError
            ? cause.message
            : haproxyErrorMessage(cause),
        );
    } finally {
      busyRef.current = false;
      if (!signal.aborted) setBusy(false);
    }
  };
  const saveWeight = () => {
    const parsed = Number(weight);
    if (
      !/^\d+$/.test(weight) ||
      !Number.isInteger(parsed) ||
      parsed < 0 ||
      parsed > 256
    ) {
      setFieldError("Enter a whole number from 0 to 256.");
      return;
    }
    if (edit) void apply("weight", edit.target, parsed);
  };
  return (
    <Section
      layout="stack"
      size="sm"
      title="Runtime controls"
      description="Manage backend servers inside this HAProxy node. Changes apply immediately and last until HAProxy reloads."
    >
      <div className="cv-haproxy-runtime">
        {error && (
          <InlineTip variant="warning" title="Configuration could not refresh">
            {error} Last received discovery remains visible.
          </InlineTip>
        )}
        {actionError && !edit && (
          <InlineTip variant="error" role="alert">
            {actionError}
          </InlineTip>
        )}
        {data && (
          <HAProxyNodeSetupPanel
            setup={discovery?.setup}
            connected={
              data.connected && now - Date.parse(data.control.last_seen) <= 6000
            }
            now={now}
          />
        )}
        {data && (
          <HAProxyAgentVersion
            agent={discovery?.agent}
            manager={data.manager_build}
            fresh={
              !!data.connected &&
              now - Date.parse(data.control.last_seen) <= 6000
            }
          />
        )}
        {!data && !error ? (
          <LoadingStatus delayMs={1000}>
            Discovering HAProxy configuration…
          </LoadingStatus>
        ) : !discovery || discovery.error_code ? (
          <EmptyState
            size="sm"
            title={
              discovery?.error_code === "unsupported_runtime"
                ? "HAProxy runtime version is not supported"
                : "Configuration discovery unavailable"
            }
            description={
              discovery?.error_code === "runtime_not_configured"
                ? "Connect the agent to this node’s local HAProxy Runtime API socket to discover its configuration."
                : "The agent must connect to the manager and reach this node’s local HAProxy Runtime API. Metrics reporting runs independently."
            }
          />
        ) : (
          <>
            <DescriptionList columns={2}>
              <DescriptionItem label="HAProxy version">
                {discovery.version}
              </DescriptionItem>
              <DescriptionItem label="Control connection">
                <Badge
                  tone={
                    data?.connected &&
                    now - Date.parse(data.control.last_seen) <= 6000
                      ? "success"
                      : "warning"
                  }
                >
                  {data?.connected &&
                  now - Date.parse(data.control.last_seen) <= 6000
                    ? "Connected"
                    : "Disconnected"}
                </Badge>
              </DescriptionItem>
              <DescriptionItem label="Management">
                <Badge tone={data?.management_enabled ? "success" : "neutral"}>
                  {data?.management_enabled
                    ? "Runtime controls enabled"
                    : "Observation only"}
                </Badge>
              </DescriptionItem>
              <DescriptionItem label="Configuration fingerprint">
                <code>
                  {discovery.config_fingerprint
                    ? discovery.config_fingerprint.slice(0, 12)
                    : "Not configured"}
                </code>
              </DescriptionItem>
            </DescriptionList>
            {reason && <InlineTip>{reason}</InlineTip>}
            <PaginatedDataTable
              aria-label="Discovered HAProxy configuration"
              itemLabel="proxies"
              rows={discovery.proxies.map((proxy) => ({
                ...proxy,
                id: `${proxy.kind}/${proxy.name}`,
              }))}
              rowLabel={(proxy) => proxy.name}
              columns={[
                {
                  id: "name",
                  header: "Proxy",
                  isRowHeader: true,
                  cell: (proxy) => proxy.name,
                },
                {
                  id: "kind",
                  header: "Type",
                  cell: (proxy) =>
                    proxy.kind === "frontend" ? "Frontend" : "Backend pool",
                },
                {
                  id: "mode",
                  header: "Mode",
                  cell: (proxy) => proxy.mode || "—",
                },
                {
                  id: "algorithm",
                  header: "Balancing",
                  cell: (proxy) => proxy.algorithm || "—",
                },
                {
                  id: "status",
                  header: "Observed status",
                  cell: (proxy) => proxy.status,
                },
              ]}
            />
            <Section
              layout="stack"
              size="sm"
              title="Backend servers"
              description="Drain stops normal balancing and waits up to 30 seconds for active connections to reach zero. Persistence-routed connections can still arrive."
            >
              <PaginatedDataTable
                aria-label="HAProxy backend servers"
                itemLabel="servers"
                rows={discovery.servers.map((server) => ({
                  ...server,
                  id: `${server.backend}/${server.name}`,
                }))}
                rowLabel={(server) => `${server.backend}/${server.name}`}
                rowActions={(server) => (
                  <>
                    <MenuItem
                      disabledReason={
                        reason ??
                        (server.backend.startsWith("cv_app_")
                          ? "Use the application’s Backends page to change this server."
                          : busy
                            ? "Wait for the current runtime change to finish."
                            : server.state === "ready" &&
                                !hasOtherCapacity(discovery, server)
                              ? "Another healthy, enabled server must remain in this backend pool."
                              : undefined)
                      }
                      onAction={() =>
                        void apply(
                          server.state === "ready" ? "drain" : "restore",
                          server,
                        )
                      }
                    >
                      {server.state === "ready"
                        ? "Drain server"
                        : "Restore server"}
                    </MenuItem>
                    <MenuItem
                      disabledReason={
                        reason ??
                        (server.backend.startsWith("cv_app_")
                          ? "Use the application’s Backends page to change its weights."
                          : busy
                            ? "Wait for the current runtime change to finish."
                            : undefined)
                      }
                      onAction={() => openWeight(server)}
                    >
                      Change weight
                    </MenuItem>
                  </>
                )}
                columns={[
                  {
                    id: "name",
                    header: "Server",
                    isRowHeader: true,
                    cell: (server) => server.name,
                  },
                  {
                    id: "backend",
                    header: "Backend pool",
                    cell: (server) => server.backend,
                  },
                  {
                    id: "address",
                    header: "Address",
                    cell: (server) => <code>{server.address || "—"}</code>,
                  },
                  {
                    id: "state",
                    header: "State",
                    cell: (server) => (
                      <Badge
                        tone={server.state === "ready" ? "neutral" : "warning"}
                      >
                        {server.state === "ready"
                          ? "Ready"
                          : server.state === "drain"
                            ? "Draining"
                            : "Maintenance"}
                      </Badge>
                    ),
                  },
                  {
                    id: "health",
                    header: "Health",
                    cell: (server) => server.health,
                  },
                  {
                    id: "weight",
                    header: "Weight",
                    align: "end",
                    cell: (server) =>
                      `${server.weight} / ${server.initial_weight} configured`,
                  },
                  {
                    id: "connections",
                    header: "Connections",
                    align: "end",
                    cell: (server) => server.active_connections,
                  },
                ]}
                emptyState={
                  <EmptyState
                    size="sm"
                    title="No backend servers discovered"
                    description="This HAProxy configuration has no backend servers available for runtime operations."
                  />
                }
              />
            </Section>
          </>
        )}
        {!!data?.operations.some((op) => op.kind !== "configuration") && (
          <Section
            layout="stack"
            size="sm"
            title="Recent runtime operations"
            description="Latest operations on this node. A verified result records observed HAProxy state; an unknown outcome requires checking the current state."
          >
            <PaginatedDataTable
              aria-label="HAProxy runtime operations"
              itemLabel="operations"
              rows={data.operations.filter((op) => op.kind !== "configuration")}
              rowLabel={(op) => `${op.kind} ${op.backend}/${op.server}`}
              columns={[
                {
                  id: "action",
                  header: "Operation",
                  isRowHeader: true,
                  cell: (op) =>
                    op.kind === "weight"
                      ? `Weight → ${op.weight}`
                      : op.kind === "restore"
                        ? "Restore"
                        : "Drain",
                },
                {
                  id: "target",
                  header: "Backend server",
                  cell: (op) => `${op.backend}/${op.server}`,
                },
                {
                  id: "status",
                  header: "Result",
                  cell: (op) => (
                    <Badge
                      tone={
                        op.status === "succeeded"
                          ? "success"
                          : op.status === "failed"
                            ? "error"
                            : op.status === "unknown"
                              ? "warning"
                              : "info"
                      }
                    >
                      {op.status === "succeeded"
                        ? "Verified"
                        : op.status === "running"
                          ? "Verifying"
                          : op.status === "unknown"
                            ? "Outcome unknown"
                            : op.status === "queued"
                              ? "Queued"
                              : "Failed"}
                    </Badge>
                  ),
                },
                {
                  id: "details",
                  header: "Details",
                  cell: (op) =>
                    op.error_code
                      ? runtimeOperationMessage(op.error_code)
                      : op.status === "succeeded"
                        ? "Runtime state verified"
                        : "Waiting for the agent",
                },
                {
                  id: "requested",
                  header: "Requested",
                  cell: (op) => new Date(op.created_at).toLocaleTimeString(),
                },
              ]}
            />
          </Section>
        )}
      </div>
      <Drawer
        isOpen={!!edit}
        onOpenChange={(value) => {
          if (!value) close();
        }}
        isDismissable={!busy}
      >
        <DrawerDialog title="Change backend weight" hideCloseButton={busy}>
          <DialogForm onSubmit={saveWeight}>
            <DrawerBody>
              {actionError && (
                <InlineTip variant="error" role="alert">
                  {actionError}
                </InlineTip>
              )}
              <DescriptionList>
                <DescriptionItem label="Backend pool">
                  {edit?.target.backend}
                </DescriptionItem>
                <DescriptionItem label="Server">
                  {edit?.target.name}
                </DescriptionItem>
              </DescriptionList>
              <TextField
                label="Runtime weight"
                description="Whole number from 0 to 256. Zero stops normal balancing. A weight of 200 gets twice the share of 100. Resets on HAProxy reload."
                value={weight}
                onChange={(value) => {
                  setWeight(value);
                  setFieldError(null);
                }}
                inputMode="numeric"
                isInvalid={!!fieldError}
                errorMessage={fieldError ?? undefined}
                errorDisplay="inline"
                autoFocus
              />
              {editReason && <InlineTip>{editReason}</InlineTip>}
            </DrawerBody>
            <DrawerFooter>
              <Button
                type="submit"
                variant="primary"
                isLoading={busy}
                disabledReason={editReason}
              >
                Save weight
              </Button>
              <Button isDisabled={busy} onPress={close}>
                Cancel
              </Button>
            </DrawerFooter>
          </DialogForm>
        </DrawerDialog>
      </Drawer>
    </Section>
  );
}
