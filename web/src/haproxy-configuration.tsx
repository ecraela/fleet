import { useEffect, useRef, useState } from "react";
import {
  Badge,
  Button,
  Checkbox,
  DataTable,
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
  RemoveButton,
  Section,
  Select,
  TablePagination,
  TextField,
} from "@ecraela/ui";
import { ApiError } from "./api";
import {
  HAProxyClient,
  haproxyErrorMessage,
  type HAProxyConfigurationBackend,
  type HAProxyConfigurationDocument,
  type HAProxyConfigurationInput,
  type HAProxyConfigurationRevision,
  type HAProxyConfigurationServer,
  type HAProxyConfigurationView,
} from "./haproxy-api";
import { isActionCancelled, usePermissionAction } from "./permissions";
import { maintenanceReason } from "./haproxy-maintenance";

type ServerDraft = {
  key: string;
  original: boolean;
  name: string;
  address: string;
  port: string;
  weight: string;
  maintenance: boolean;
};
type Edit = {
  backend: string;
  algorithm: HAProxyConfigurationBackend["algorithm"];
  servers: ServerDraft[];
  document: HAProxyConfigurationDocument;
  base: HAProxyConfigurationInput;
};
const algorithms = [
  { id: "roundrobin", label: "Round robin" },
  { id: "leastconn", label: "Least connections" },
  { id: "first", label: "First available" },
];
const algorithmName = (value: string) =>
  algorithms.find((option) => option.id === value)?.label ?? value;
export function configurationControlReason(
  data: HAProxyConfigurationView | null,
  permissionReason?: string,
  now = Date.now(),
): string | undefined {
  if (permissionReason) return permissionReason;
  if (data?.control.maintenance_blocked)
    return "Whole-node maintenance is active. Follow its progress under Node maintenance.";
  if (data?.control.active_rollout_id)
    return "A cluster update is running. Follow its progress under Rolling configuration.";
  if (!data?.connected || now - Date.parse(data.control.last_seen) > 6000)
    return "Wait for the agent’s control connection.";
  const current = data.control.discovery?.configuration;
  if (!data.fresh || !current || now - current.observed_at_ms > 6000)
    return "Wait for fresh saved configuration.";
  if (!data.configuration_enabled)
    return "Persistent editing must be enabled on this node’s agent.";
  if (
    data.versions.some(
      (version) => version.status === "queued" || version.status === "running",
    )
  )
    return "Wait for the current configuration change to finish.";
  return undefined;
}
export function rolloutReadinessReason(
  data: HAProxyConfigurationView | null,
): string | undefined {
  const blocked = configurationControlReason(data);
  if (blocked) return blocked;
  const d = data!.control.discovery!;
  if (
    !d.proxies.some((p) => p.kind === "frontend") ||
    d.proxies.some((p) => p.kind === "frontend" && p.status !== "OPEN")
  )
    return "Every node needs an open HAProxy frontend before a cluster update.";
  for (const backend of d.configuration!.document!.backends)
    for (const server of backend.servers) {
      if (server.maintenance || server.weight === 0) continue;
      const actual = d.servers.find(
        (s) => s.backend === backend.name && s.name === server.name,
      );
      if (
        !actual ||
        actual.state !== "ready" ||
        actual.weight !== server.weight ||
        actual.health !== "UP" ||
        !["L4OK", "L6OK", "L7OK", "L7OKC"].includes(
          (actual.check_status ?? "").replace(/^\* /, ""),
        )
      )
        return "Every enabled backend server must use its saved weight and pass its HAProxy health check before a cluster update.";
    }
  return undefined;
}
export function configurationOperationMessage(code: string): string {
  const messages: Record<string, string> = {
    configuration_changed:
      "The saved configuration changed. Refresh it before saving again.",
    configuration_rejected:
      "HAProxy rejected the configuration. Its previous settings remain available in history.",
    configuration_reload_failed:
      "HAProxy could not reload the configuration. Check the node’s current settings before retrying.",
    configuration_history_missing:
      "This version’s full snapshot is unavailable on the node. Restore its agent state backup before retrying.",
    configuration_storage_full:
      "The node’s configuration history storage is full or unavailable.",
    generation_changed:
      "HAProxy restarted or changed while this operation was starting. Reload the editor and try again.",
    configuration_unavailable:
      "The agent could not reach its local configuration service.",
    control_reconnected:
      "The control connection changed during the save. The agent will check the outcome when it reconnects.",
    verification_deadline:
      "The reload could not be verified before the deadline. Check the current settings.",
    outcome_unverified:
      "The outcome could not be verified. Check the current settings before another change.",
  };
  return (
    messages[code] ??
    "The change could not be verified. Check the node’s current settings."
  );
}
function baseInput(data: HAProxyConfigurationView): HAProxyConfigurationInput {
  const discovery = data.control.discovery!;
  const state = discovery.configuration!;
  return {
    request_id: crypto.randomUUID(),
    expected_generation: discovery.generation,
    expected_artifact_id: state.artifact_id,
    expected_version: state.version,
  };
}
export function configurationDraft(
  backend: HAProxyConfigurationBackend,
): ServerDraft[] {
  return backend.servers.map((server, index) => ({
    ...server,
    key: `server-${index}`,
    original: true,
    port: String(server.port),
    weight: String(server.weight),
  }));
}
export function documentFromEdit(
  edit: Pick<Edit, "document" | "backend" | "algorithm" | "servers">,
): HAProxyConfigurationDocument {
  const names = new Set<string>();
  const servers: HAProxyConfigurationServer[] = edit.servers.map((server) => {
    if (!/^[a-zA-Z0-9_.:-]{1,128}$/.test(server.name) || names.has(server.name))
      throw new Error(
        "Each server needs a unique name using letters, numbers, dots, hyphens, colons or underscores.",
      );
    names.add(server.name);
    if (
      !server.address ||
      server.address.length > 253 ||
      /[\s/;#]/.test(server.address)
    )
      throw new Error(`Enter a hostname or IP address for ${server.name}.`);
    if (
      !/^\d+$/.test(server.port) ||
      Number(server.port) < 1 ||
      Number(server.port) > 65535
    )
      throw new Error(`Enter a port from 1 to 65535 for ${server.name}.`);
    if (!/^\d+$/.test(server.weight) || Number(server.weight) > 256)
      throw new Error(`Enter a weight from 0 to 256 for ${server.name}.`);
    return {
      name: server.name,
      address: server.address,
      port: Number(server.port),
      weight: Number(server.weight),
      maintenance: server.maintenance,
    };
  });
  if (!servers.some((server) => !server.maintenance && server.weight > 0))
    throw new Error(
      "Keep at least one enabled, positive-weight server in the backend.",
    );
  return {
    ...structuredClone(edit.document),
    backends: edit.document.backends.map((backend) =>
      backend.name === edit.backend
        ? { ...backend, algorithm: edit.algorithm, servers }
        : structuredClone(backend),
    ),
  };
}
export interface HAProxyConfigurationScope {
  id: string;
  name: string;
  nodeIds: string[];
  entrypointNodeId?: string | undefined;
}

export function HAProxyConfiguration({
  nodeId,
  client,
  permissionReason,
  cluster,
}: {
  cluster?: HAProxyConfigurationScope;
  nodeId: string;
  client: HAProxyClient;
  permissionReason?: string;
}) {
  const { runAction } = usePermissionAction();
  const [data, setData] = useState<HAProxyConfigurationView | null>(
    () => client.cachedConfiguration(nodeId) ?? null,
  );
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [edit, setEdit] = useState<Edit | null>(null);
  const [selected, setSelected] = useState<HAProxyConfigurationRevision | null>(
    null,
  );
  const [busy, setBusy] = useState(false);
  const [rolling, setRolling] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [historyPage, setHistoryPage] = useState(1);
  const [now, setNow] = useState(Date.now());
  const busyRef = useRef(false);
  const lifetime = useRef<AbortController | null>(null);
  const refresh = useRef<() => Promise<void>>(async () => {});
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
        const next = await client.configuration(nodeId, controller.signal);
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
  const state = data?.control.discovery?.configuration;
  const reason = configurationControlReason(data, permissionReason, now);
  const current = data?.versions.find(
    (version) => version.id === data.control.current_revision_id,
  );
  const beginEdit = (backend: HAProxyConfigurationBackend) => {
    if (!data || !state?.document) return;
    setSelected(null);
    setActionError(null);
    setRolling(
      Boolean(cluster?.entrypointNodeId && cluster.nodeIds.length >= 2),
    );
    setEdit({
      backend: backend.name,
      algorithm: backend.algorithm,
      servers: configurationDraft(backend),
      document: structuredClone(state.document),
      base: baseInput(data),
    });
  };
  const close = () => {
    if (!busyRef.current) {
      setEdit(null);
      setSelected(null);
      setActionError(null);
    }
  };
  const editReason =
    reason ??
    (edit &&
    (edit.base.expected_artifact_id !== state?.artifact_id ||
      edit.base.expected_version !== state.version ||
      edit.base.expected_generation !== data?.control.discovery?.generation)
      ? "The configuration changed while you were editing. Reopen the editor to use the current version."
      : undefined);
  const apply = async (
    input: HAProxyConfigurationInput,
    rollback?: HAProxyConfigurationRevision,
  ) => {
    if (busyRef.current || !lifetime.current || lifetime.current.signal.aborted)
      return;
    const blocked = rollback ? reason : editReason;
    if (blocked) {
      setActionError(blocked);
      return;
    }
    busyRef.current = true;
    setBusy(true);
    setActionError(null);
    const signal = lifetime.current.signal;
    try {
      await runAction(async () => {
        await client.login(true);
        signal.throwIfAborted();
        if (rolling && !rollback && cluster && input.document) {
          const views = await Promise.all(
            cluster.nodeIds.map(async (id) => ({
              id,
              data: await client.configuration(id, signal),
            })),
          );
          const source = views.find((value) => value.id === nodeId)?.data
            .control.discovery;
          if (
            source?.generation !== input.expected_generation ||
            source.configuration?.artifact_id !== input.expected_artifact_id ||
            source.configuration.version !== input.expected_version
          )
            throw new Error(
              "The source settings changed. Reopen the editor before starting a rollout.",
            );
          const blocked = views
            .map((value) => rolloutReadinessReason(value.data))
            .find(Boolean);
          if (blocked) throw new Error(blocked);
          const maintenance = await client.maintenance(nodeId, signal);
          const maintenanceBlocked = maintenanceReason(maintenance);
          if (maintenanceBlocked) throw new Error(maintenanceBlocked);
          const entrypoint = maintenance.controller.discovery!;
          await client.startRollout(cluster.id, {
            request_id: input.request_id,
            document: input.document,
            entrypoint: {
              node_id: maintenance.controller_node_id!,
              binding_id: maintenance.binding_id!,
              expected_generation: entrypoint.generation,
              expected_artifact_id: entrypoint.configuration!.artifact_id,
              expected_version: entrypoint.configuration!.version,
            },
            drain_timeout_seconds: 30,
            nodes: views.map(({ id, data }) => ({
              node_id: id,
              expected_generation: data.control.discovery!.generation,
              expected_artifact_id:
                data.control.discovery!.configuration!.artifact_id,
              expected_version: data.control.discovery!.configuration!.version,
            })),
          });
          setNotice(
            "Cluster update started. Follow each node under Rolling configuration.",
          );
        } else {
          await client.saveConfiguration(nodeId, input, rollback?.id);
          setNotice(null);
        }
      });
      if (!signal.aborted) {
        setEdit(null);
        setSelected(null);
        setHistoryPage(1);
        await refresh.current();
      }
    } catch (cause) {
      if (!signal.aborted && !isActionCancelled(cause))
        setActionError(
          cause instanceof Error && !("status" in cause)
            ? cause.message
            : cause instanceof ApiError
              ? cause.message
              : haproxyErrorMessage(cause),
        );
    } finally {
      busyRef.current = false;
      if (!signal.aborted) setBusy(false);
    }
  };
  const save = () => {
    if (!edit) return;
    try {
      void apply({ ...edit.base, document: documentFromEdit(edit) });
    } catch (cause) {
      setActionError(
        cause instanceof Error ? cause.message : "Check the backend settings.",
      );
    }
  };
  const restore = (revision: HAProxyConfigurationRevision) => {
    if (data) void apply(baseInput(data), revision);
  };
  const restoreReason = (revision: HAProxyConfigurationRevision) =>
    reason ??
    ((state?.document?.applications?.length ?? 0) > 0
      ? "Use application version history to restore application-owned configuration."
      : undefined) ??
    ((state?.document?.listeners?.length ?? 0) > 0
      ? "Use HTTPS listener history to restore TLS configuration."
      : undefined) ??
    (revision.id === data?.control.current_revision_id &&
    !data?.control.configuration_history_limited
      ? "This is already the current version."
      : !revision.artifact_id
        ? "This version has no applied snapshot to restore."
        : undefined);
  const updateServer = (key: string, patch: Partial<ServerDraft>) =>
    setEdit((value) =>
      value
        ? {
            ...value,
            servers: value.servers.map((server) =>
              server.key === key ? { ...server, ...patch } : server,
            ),
          }
        : value,
    );
  return (
    <Section
      layout="stack"
      size="sm"
      title="Persistent configuration"
      description="Saved backend settings survive reloads. Saving validates the configuration, reloads HAProxy and verifies the running settings."
    >
      <div className="cv-haproxy-configuration">
        {notice && (
          <InlineTip role="status" label="Status">
            {notice}
          </InlineTip>
        )}
        {data?.control.configuration_history_limited && (
          <InlineTip
            variant="warning"
            title="Automatic configuration history is full"
          >
            Latest settings remain visible. You can still save changes and roll
            back saved versions. Further external changes will not be added to
            this node’s history.
          </InlineTip>
        )}
        {error && (
          <InlineTip
            variant="warning"
            title="Saved configuration could not refresh"
          >
            {error} Last received settings remain visible.
          </InlineTip>
        )}
        {actionError && !edit && !selected && (
          <InlineTip variant="error" role="alert">
            {actionError}
          </InlineTip>
        )}
        {!data && !error ? (
          <LoadingStatus delayMs={1000}>
            Loading saved HAProxy settings…
          </LoadingStatus>
        ) : !state?.document || state.error_code ? (
          <EmptyState
            size="sm"
            title="Persistent configuration unavailable"
            description={
              state?.error_code === "configuration_unsupported"
                ? "This configuration uses settings outside the supported backend editing profile. Runtime metrics remain available."
                : "Connect the agent to a private local Data Plane API socket and enable configuration control to edit saved backend settings."
            }
          />
        ) : (
          <>
            <DescriptionList columns={2}>
              <DescriptionItem label="Current version">
                {data?.control.configuration_history_limited
                  ? "Latest observation"
                  : current
                    ? `Version ${current.number}`
                    : "Discovering"}
              </DescriptionItem>
              <DescriptionItem label="Configuration access">
                <Badge
                  tone={data?.configuration_enabled ? "success" : "neutral"}
                >
                  {data?.configuration_enabled
                    ? "Editing enabled"
                    : "Read only"}
                </Badge>
              </DescriptionItem>
            </DescriptionList>
            {reason && <InlineTip>{reason}</InlineTip>}
            <PaginatedDataTable
              aria-label="Saved HAProxy backends"
              itemLabel="backends"
              rows={state.document.backends.map((backend) => ({
                ...backend,
                id: backend.name,
              }))}
              rowLabel={(backend) => backend.name}
              columns={[
                {
                  id: "name",
                  header: "Backend pool",
                  isRowHeader: true,
                  cell: (backend) => backend.name,
                },
                {
                  id: "algorithm",
                  header: "Balancing",
                  cell: (backend) => algorithmName(backend.algorithm),
                },
                {
                  id: "servers",
                  header: "Servers",
                  align: "end",
                  cell: (backend) => backend.servers.length,
                },
                {
                  id: "enabled",
                  header: "Enabled",
                  align: "end",
                  cell: (backend) =>
                    backend.servers.filter(
                      (server) => !server.maintenance && server.weight > 0,
                    ).length,
                },
              ]}
              rowActions={(backend) => (
                <MenuItem
                  isDisabled={
                    !!reason ||
                    busy ||
                    backend.servers.some((s) => s.forward_incoming_port)
                  }
                  onAction={() => beginEdit(backend)}
                >
                  Edit backend
                </MenuItem>
              )}
            />
          </>
        )}
        {!!data?.versions.length && (
          <Section
            layout="stack"
            size="sm"
            title="Version history"
            description="Recent revisions and the current version. Restoring creates a new change and keeps the earlier history."
          >
            <DataTable
              aria-label="HAProxy configuration versions"
              rows={data.versions.slice(
                (historyPage - 1) * 10,
                historyPage * 10,
              )}
              rowLabel={(revision) => `Version ${revision.number}`}
              columns={[
                {
                  id: "number",
                  header: "Version",
                  isRowHeader: true,
                  cell: (revision) => `Version ${revision.number}`,
                },
                {
                  id: "source",
                  header: "Change",
                  cell: (revision) =>
                    revision.source === "rollback"
                      ? `Restored from version ${data.versions.find((version) => version.id === revision.rollback_of)?.number ?? "history"}`
                      : revision.source === "edit"
                        ? "Backend settings"
                        : "Discovered settings",
                },
                {
                  id: "status",
                  header: "Status",
                  cell: (revision) => (
                    <Badge
                      tone={
                        revision.status === "applied"
                          ? "success"
                          : revision.status === "failed"
                            ? "error"
                            : revision.status === "unknown"
                              ? "warning"
                              : "neutral"
                      }
                    >
                      {revision.id === data.control.current_revision_id
                        ? data.control.configuration_history_limited
                          ? "Last saved"
                          : "Current"
                        : revision.status === "applied"
                          ? "Applied"
                          : revision.status === "observed"
                            ? "Observed"
                            : revision.status === "running"
                              ? "Verifying reload"
                              : revision.status === "unknown"
                                ? "Outcome unknown"
                                : revision.status === "failed"
                                  ? "Failed"
                                  : "Queued"}
                    </Badge>
                  ),
                },
                {
                  id: "created",
                  header: "Saved",
                  cell: (revision) =>
                    new Date(revision.created_at).toLocaleString(),
                },
              ]}
              rowActions={(revision) => (
                <>
                  <MenuItem
                    onAction={() => {
                      setEdit(null);
                      setActionError(null);
                      setSelected(revision);
                    }}
                  >
                    View settings
                  </MenuItem>
                  <MenuItem
                    disabledReason={
                      restoreReason(revision) ??
                      (busy
                        ? "Wait for the current configuration change to finish."
                        : undefined)
                    }
                    onAction={() => restore(revision)}
                  >
                    Restore version
                  </MenuItem>
                </>
              )}
            />
            <TablePagination
              page={historyPage}
              pageSize={10}
              totalItems={data.versions.length}
              itemLabel="revisions"
              onPageChange={setHistoryPage}
              previousLabel="Previous revisions"
              nextLabel="Next revisions"
            />
          </Section>
        )}
      </div>
      <Drawer
        size="lg"
        isOpen={!!edit}
        onOpenChange={(open) => {
          if (!open) close();
        }}
        isDismissable={!busy}
      >
        <DrawerDialog
          title={`Edit ${edit?.backend ?? "backend"}`}
          hideCloseButton={busy}
        >
          <DialogForm onSubmit={save}>
            <DrawerBody>
              {actionError && (
                <InlineTip variant="error" role="alert">
                  {actionError}
                </InlineTip>
              )}
              {editReason && <InlineTip>{editReason}</InlineTip>}
              {cluster &&
                cluster.nodeIds.length >= 2 &&
                cluster.nodeIds.length <= 12 && (
                  <Checkbox
                    isSelected={rolling}
                    isDisabled={busy || !cluster.entrypointNodeId}
                    onChange={setRolling}
                  >
                    Roll out to all {cluster.nodeIds.length} nodes in{" "}
                    {cluster.name}
                  </Checkbox>
                )}
              {cluster &&
                cluster.nodeIds.length >= 2 &&
                !cluster.entrypointNodeId && (
                  <InlineTip>
                    Bind an enrolled entrypoint agent to enable rolling updates
                    with connection draining.
                  </InlineTip>
                )}
              {rolling && (
                <InlineTip>
                  Each node is withdrawn from the entrypoint and drained before
                  updating. It must pass five seconds of health checks before
                  and after rejoining traffic, then the next node starts.
                  Changes roll back on failure using the same drain and rejoin
                  sequence. Connections are never forcibly closed. A drain that
                  exceeds 30 seconds stops the update and leaves the node in
                  maintenance. Progress continues if you close this page.
                </InlineTip>
              )}
              <Select
                label="Balancing strategy"
                options={algorithms}
                selectedKey={edit?.algorithm ?? "roundrobin"}
                onSelectionChange={(key) =>
                  setEdit((value) =>
                    value
                      ? {
                          ...value,
                          algorithm:
                            key as HAProxyConfigurationBackend["algorithm"],
                        }
                      : value,
                  )
                }
                isDisabled={busy}
              />
              {edit?.servers.map((server, index) => (
                <Section
                  layout="stack"
                  key={server.key}
                  size="sm"
                  title={
                    server.original ? server.name : `New server ${index + 1}`
                  }
                  actions={
                    <RemoveButton
                      label={`Remove server ${server.name || index + 1}`}
                      disabledReason={
                        busy
                          ? "Wait for the current save."
                          : edit.servers.length === 1
                            ? "Keep at least one server in the backend."
                            : undefined
                      }
                      onPress={() =>
                        setEdit((value) =>
                          value
                            ? {
                                ...value,
                                servers: value.servers.filter(
                                  (row) => row.key !== server.key,
                                ),
                              }
                            : value,
                        )
                      }
                    />
                  }
                >
                  <TextField
                    label={`Server ${index + 1} name`}
                    value={server.name}
                    isReadOnly={server.original}
                    isDisabled={busy}
                    maxLength={128}
                    onChange={(name) => updateServer(server.key, { name })}
                  />
                  <TextField
                    label={`${server.name || `Server ${index + 1}`} address`}
                    value={server.address}
                    isDisabled={busy}
                    maxLength={253}
                    onChange={(address) =>
                      updateServer(server.key, { address })
                    }
                  />
                  <TextField
                    label={`${server.name || `Server ${index + 1}`} port`}
                    value={server.port}
                    inputMode="numeric"
                    isDisabled={busy}
                    onChange={(port) => updateServer(server.key, { port })}
                  />
                  <TextField
                    label={`${server.name || `Server ${index + 1}`} weight`}
                    value={server.weight}
                    inputMode="numeric"
                    isDisabled={busy}
                    description="0–256. Zero stops normal balancing selection."
                    onChange={(weight) => updateServer(server.key, { weight })}
                  />
                  <Checkbox
                    isSelected={server.maintenance}
                    isDisabled={busy}
                    onChange={(maintenance) =>
                      updateServer(server.key, { maintenance })
                    }
                  >
                    Maintenance mode for {server.name || `server ${index + 1}`}
                  </Checkbox>
                </Section>
              ))}
              <div>
                <Button
                  isDisabled={busy || (edit?.servers.length ?? 0) >= 128}
                  onPress={() =>
                    setEdit((value) =>
                      value
                        ? {
                            ...value,
                            servers: [
                              ...value.servers,
                              {
                                key: crypto.randomUUID(),
                                original: false,
                                name: "",
                                address: "",
                                port: "8080",
                                weight: "100",
                                maintenance: false,
                              },
                            ],
                          }
                        : value,
                    )
                  }
                >
                  Add server
                </Button>
              </div>
              <InlineTip>
                These are saved settings for this HAProxy node. Reloading
                applies them to its backend servers and resets temporary runtime
                overrides. Other settings in the file are preserved.
              </InlineTip>
            </DrawerBody>
            <DrawerFooter>
              <Button
                variant="primary"
                type="submit"
                isLoading={busy}
                disabledReason={editReason}
              >
                {rolling ? "Save and roll out" : "Save and reload"}
              </Button>
              <Button isDisabled={busy} onPress={close}>
                Cancel
              </Button>
            </DrawerFooter>
          </DialogForm>
        </DrawerDialog>
      </Drawer>
      <Drawer
        size="lg"
        isOpen={!!selected}
        onOpenChange={(open) => {
          if (!open) close();
        }}
        isDismissable={!busy}
      >
        <DrawerDialog
          title={`Version ${selected?.number ?? ""}`}
          hideCloseButton={busy}
        >
          <DrawerBody>
            {actionError && (
              <InlineTip variant="error" role="alert">
                {actionError}
              </InlineTip>
            )}
            {selected?.error_code && (
              <InlineTip variant="warning">
                {configurationOperationMessage(selected.error_code)}
              </InlineTip>
            )}
            {selected?.document.backends.map((backend) => (
              <Section
                layout="stack"
                key={backend.name}
                size="sm"
                title={backend.name}
                description={algorithmName(backend.algorithm)}
              >
                <DataTable
                  aria-label={`Version ${selected.number} ${backend.name} servers`}
                  rows={backend.servers.map((server) => ({
                    ...server,
                    id: server.name,
                  }))}
                  rowLabel={(server) => server.name}
                  columns={[
                    {
                      id: "name",
                      header: "Server",
                      isRowHeader: true,
                      cell: (server) => server.name,
                    },
                    {
                      id: "address",
                      header: "Address",
                      cell: (server) => `${server.address}:${server.port}`,
                    },
                    {
                      id: "weight",
                      header: "Weight",
                      align: "end",
                      cell: (server) => server.weight,
                    },
                    {
                      id: "state",
                      header: "State",
                      cell: (server) =>
                        server.maintenance ? "Maintenance" : "Enabled",
                    },
                  ]}
                />
              </Section>
            ))}
          </DrawerBody>
          <DrawerFooter>
            <Button
              variant="primary"
              isLoading={busy}
              disabledReason={selected ? restoreReason(selected) : undefined}
              onPress={() => {
                if (selected) restore(selected);
              }}
            >
              Restore version {selected?.number}
            </Button>
            <Button isDisabled={busy} onPress={close}>
              Close
            </Button>
          </DrawerFooter>
        </DrawerDialog>
      </Drawer>
    </Section>
  );
}
