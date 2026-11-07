import { ConnectionIssue } from "./connection-health";
import {
  applicationDraftErrors,
  normalizedHostname,
  type ApplicationDraft,
} from "./haproxy-application-form";
import { HAProxyTrafficRules } from "./haproxy-traffic-rules";
import { HAProxyApplicationGeography } from "./haproxy-geography";
import { Link as AriaLink } from "@ecraela/ui";
import { applicationTabs, type ApplicationTab } from "./haproxy-navigation";
import { useEffect, useRef, useState, type ComponentProps } from "react";
import {
  Badge,
  focusFormError,
  Button,
  Checkbox,
  DataTable,
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
  SearchField,
  Select,
  TablePagination,
  TextField,
  Toolbar,
} from "@ecraela/ui";
import { Plus } from "@ecraela/ui/icons";
import { ApiError } from "./api";
import {
  HAProxyClient,
  haproxyErrorMessage,
  validApplication,
  validApplicationProbeGrants,
  type HAProxyApplication,
  type HAProxyApplicationRevision,
  type HAProxyCluster,
  type HAProxyApplicationHealth,
  type HAProxyApplicationAction,
  type HAProxyApplicationInput,
} from "./haproxy-api";
import {
  HAProxyApplicationDetails,
  applicationHealthLabel,
  currentApplicationHealth,
} from "./haproxy-application-details";
import { HAProxyConnectDNS } from "./haproxy-connect-dns";
import {
  HAProxyApplicationLifecycle,
  applicationActionLabel,
} from "./haproxy-application-lifecycle";
import { isActionCancelled, usePermissionAction } from "./permissions";
import { rolloutStatus } from "./haproxy-rollouts";
import { configurationControlReason } from "./haproxy-configuration";
import { HAProxyApplicationTraffic } from "./haproxy-requests";

export type { ApplicationDraft } from "./haproxy-application-form";
export function applicationSpec(draft: ApplicationDraft): HAProxyApplication {
  const firstError = Object.values(applicationDraftErrors(draft))[0];
  if (firstError) throw new Error(firstError);
  if (
    draft.servers.some(
      (server) => !/^\d+$/.test(server.port) || !/^\d+$/.test(server.weight),
    )
  ) {
    throw new Error("Enter whole numbers for each server port and weight.");
  }
  const spec = {
    ...draft,
    name: draft.name.trim(),
    hostname: normalizedHostname(draft.hostname),
    servers: draft.servers.map((s) => ({
      ...s,
      address: s.address.trim(),
      port: Number(s.port),
      weight: Number(s.weight),
      maintenance: false,
    })),
  };
  if (
    !validApplication(spec) ||
    !spec.servers.some((s) => s.weight > 0) ||
    new Set(spec.servers.map((s) => s.name)).size !== spec.servers.length ||
    spec.servers.some(
      (s) =>
        !/^[-A-Za-z0-9_.:]{1,128}$/.test(s.name) ||
        !s.address ||
        !/^[A-Za-z0-9_.:-]+$/.test(s.address),
    ) ||
    !/^\/[A-Za-z0-9/._~-]*$/.test(spec.health_path) ||
    spec.path_prefix.includes("..") ||
    spec.health_path.includes("..")
  )
    throw new Error(
      "Enter an exact hostname, paths beginning with /, and unique backend servers with valid addresses, ports and weights. Keep at least one positive weight.",
    );
  return spec;
}
export function latestApplications(
  revisions: HAProxyApplicationRevision[],
): HAProxyApplicationRevision[] {
  const latest = new Map<string, HAProxyApplicationRevision>();
  for (const r of revisions)
    if ((latest.get(r.application_id)?.number ?? 0) < r.number)
      latest.set(r.application_id, r);
  return [...latest.values()].sort((a, b) =>
    a.spec.name.localeCompare(b.spec.name),
  );
}
export function applicationEditSource(
  revision: HAProxyApplicationRevision,
  records: HAProxyApplicationRevision[],
): HAProxyApplicationRevision {
  return revision.status === "rolled_back" ||
    revision.status === "rollback_failed"
    ? (records
        .filter(
          (row) =>
            row.application_id === revision.application_id &&
            row.status === "succeeded",
        )
        .sort((a, b) => b.number - a.number)[0] ?? revision)
    : revision;
}
export function applicationLifecycle(
  revision: HAProxyApplicationRevision,
): "active" | "disabled" | "deleted" {
  return revision.deleted
    ? "deleted"
    : revision.spec.disabled
      ? "disabled"
      : "active";
}
const statusTone = (status: string) =>
  status === "succeeded"
    ? ("success" as const)
    : status === "rollback_failed"
      ? ("error" as const)
      : status === "rolled_back" || status === "rolling_back"
        ? ("warning" as const)
        : ("neutral" as const);
export function HAProxyApplications({
  cluster,
  nodeIds,
  client,
  permissionReason,
  dnsPermissionReason,
  nodeNames,
  applicationId,
  tab = "overview",
  getHref,
  onOpen,
  onBack,
  query = "",
  onQueryChange,
  onTitleChange,
}: {
  applicationId?: string;
  onTitleChange?: (id: string, name: string) => void;
  tab?: ApplicationTab;
  getHref?: (id: string, tab?: string) => string;
  onOpen?: (id: string, tab?: string) => void;
  onBack?: () => void;
  query?: string;
  onQueryChange?: (query: string) => void;
  nodeNames?: Record<string, string> | undefined;
  dnsPermissionReason?: string | undefined;
  cluster: HAProxyCluster;
  nodeIds: string[];
  client: HAProxyClient;
  permissionReason?: string | undefined;
}) {
  const { runAction } = usePermissionAction();
  const [records, setRecords] = useState<HAProxyApplicationRevision[] | null>(
    () => client.cachedApplications(cluster.id) ?? null,
  );
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [draft, setDraft] = useState<ApplicationDraft | null>(null);
  const [validatedFields, setValidatedFields] = useState<Set<string>>(
    new Set(),
  );
  const editorForm = useRef<HTMLFormElement>(null);
  const fieldErrors: Record<string, string> = draft
    ? Object.fromEntries(
        Object.entries(applicationDraftErrors(draft)).filter(([name]) =>
          validatedFields.has(name),
        ),
      )
    : {};
  const [editing, setEditing] = useState<HAProxyApplicationRevision | null>(
    null,
  );
  const [connecting, setConnecting] = useState<string | null>(null);
  const [changing, setChanging] = useState<{
    id: string;
    action: HAProxyApplicationAction;
  } | null>(null);
  const [showDeleted, setShowDeleted] = useState(false);
  const [health, setHealth] = useState<HAProxyApplicationHealth[]>(
    () => client.cachedApplicationHealth(cluster.id) ?? [],
  );
  const [healthError, setHealthError] = useState<string | null>(null);
  const [, setNow] = useState(Date.now());
  const [page, setPage] = useState(1);
  const [busy, setBusy] = useState(false);
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
        const [historyResult, healthResult] = await Promise.allSettled([
          client.applications(cluster.id, controller.signal),
          client.applicationHealth(cluster.id, controller.signal),
        ]);
        if (!controller.signal.aborted) {
          if (historyResult.status === "fulfilled") {
            setRecords(historyResult.value);
            setError(null);
          } else setError(haproxyErrorMessage(historyResult.reason));
          if (healthResult.status === "fulfilled") {
            setHealth(healthResult.value);
            setHealthError(null);
          } else setHealthError(haproxyErrorMessage(healthResult.reason));
        }
      } catch (cause) {
        if (!controller.signal.aborted) setError(haproxyErrorMessage(cause));
      } finally {
        if (!controller.signal.aborted) setNow(Date.now());
        pending = false;
      }
    };
    refresh.current = update;
    void update();
    const resume = () => {
      setNow(Date.now());
      void update();
    };
    const timer = setInterval(resume, 2000);
    document.addEventListener("visibilitychange", resume);
    return () => {
      controller.abort();
      clearInterval(timer);
      document.removeEventListener("visibilitychange", resume);
    };
  }, [client, cluster.id]);
  const latest = latestApplications(records ?? []);
  const currentTitle = latest.find(
    (row) => row.application_id === applicationId,
  )?.spec.name;
  useEffect(() => {
    if (applicationId && currentTitle)
      onTitleChange?.(applicationId, currentTitle);
  }, [applicationId, currentTitle, onTitleChange]);
  const source = (r: HAProxyApplicationRevision) =>
    applicationEditSource(r, records ?? []);
  const visible = latest.filter(
    (r) =>
      (showDeleted ||
        !source(r).deleted ||
        r.status === "running" ||
        r.status === "rolling_back") &&
      (r.spec.name + " " + r.spec.hostname + r.spec.path_prefix)
        .toLowerCase()
        .includes(query.toLowerCase()),
  );
  const changeTarget = latest.find((r) => r.application_id === changing?.id);
  const currentHealth = (id: string) =>
    currentApplicationHealth(
      health.find((h) => h.application_id === id),
      Date.now(),
    );
  const running = latest.some(
    (r) => r.status === "running" || r.status === "rolling_back",
  );
  const reason =
    permissionReason ??
    (!cluster.topology?.control
      ? "Bind an enrolled entrypoint to this cluster to deploy applications."
      : nodeIds.length < 2 || nodeIds.length > 12
        ? "Application rolling deployment requires 2–12 enrolled nodes."
        : running
          ? "Wait for the current application deployment to finish."
          : undefined);
  const begin = (revision?: HAProxyApplicationRevision) => {
    setActionError(null);
    setEditing(revision ?? null);
    setValidatedFields(new Set());
    const source = revision
      ? applicationEditSource(revision, records ?? [])
      : null;
    setDraft(
      source
        ? {
            ...structuredClone(source.spec),
            servers: source.spec.servers.map((s) => ({
              ...s,
              port: String(s.port),
              weight: String(s.weight),
            })),
          }
        : {
            id: "",
            name: "",
            listener: cluster.topology?.frontend ?? "public",
            hostname: "",
            path_prefix: "/",
            algorithm: "roundrobin",
            health_path: "/healthz",
            servers: [
              { name: "web-1", address: "", port: "8080", weight: "100" },
            ],
          },
    );
  };
  const closeEditor = () => {
    if (!busy) {
      setDraft(null);
      setEditing(null);
      setActionError(null);
    }
  };
  const deploy = async (
    spec: HAProxyApplication,
    revision: HAProxyApplicationRevision | null,
    rollback?: HAProxyApplicationRevision,
    action?: HAProxyApplicationAction,
  ) => {
    if (busyRef.current || reason) return false;
    busyRef.current = true;
    setBusy(true);
    setActionError(null);
    const signal = lifetime.current!.signal;
    try {
      await runAction(async () => {
        await client.login(true);
        if (signal.aborted) return;
        const configurations = await Promise.all(
          nodeIds.map((id) => client.configuration(id, signal)),
        );
        const candidate = rollback?.spec ?? spec;
        for (const data of configurations) {
          const blocked = configurationControlReason(data);
          if (blocked) throw new Error(blocked);
          const version =
            data.control.discovery?.configuration
              ?.application_control_version ?? 0;
          if (
            version < 1 ||
            version > 6 ||
            (action && version < 2) ||
            (candidate.verification && version < 3) ||
            (candidate.verification?.entrypoint && version < 4) ||
            (candidate.request_telemetry &&
              (version < 5 ||
                !data.control.discovery?.configuration?.request_logs_allowed))
          )
            throw new Error(
              "Configure and upgrade every node’s agent for the selected application features.",
            );
          if (candidate.traffic_policy) {
            const caps = data.control.discovery?.configuration?.traffic_rules;
            if (
              version < 6 ||
              !caps ||
              candidate.traffic_policy.rules.some(
                (r) =>
                  (r.countries?.length && !caps.country_ready) ||
                  (r.asns?.length && !caps.asn_ready),
              )
            )
              throw new Error(
                "Configure the local traffic policy grant, logging profile and required datasets on every node.",
              );
          }
          if (
            !candidate.disabled &&
            candidate.verification &&
            action !== "disable" &&
            action !== "delete"
          ) {
            const grants = data.control.discovery?.application_probe_grants;
            for (const scheme of ["http", "https"] as const) {
              for (const scope of candidate.verification.entrypoint
                ? ["node", "entrypoint"]
                : ["node"]) {
                if (
                  candidate.verification[scheme] &&
                  (!validApplicationProbeGrants(grants) ||
                    !grants.some(
                      (g) =>
                        g.scope === scope &&
                        g.scheme === scheme &&
                        g.listener === candidate.listener &&
                        g.hostname === candidate.hostname &&
                        g.path_prefix === candidate.path_prefix,
                    ))
                )
                  throw new Error(
                    `Configure a local ${scheme.toUpperCase()} ${scope} check for this route on every agent before deploying.`,
                  );
              }
            }
          }
        }
        const view = await client.maintenance(nodeIds[0]!, signal);
        if (
          view.reason_code ||
          !view.controller?.discovery?.configuration ||
          view.controller_node_id !== cluster.topology?.control?.node_id
        )
          throw new Error(
            "Wait for a healthy managed entrypoint and fresh configuration.",
          );
        const d = view.controller.discovery,
          c = d.configuration!;
        const input: HAProxyApplicationInput = {
          request_id: crypto.randomUUID(),
          expected_revision: revision?.number ?? 0,
          spec,
          nodes: configurations.map((data) => ({
            node_id: data.control.node_id,
            expected_generation: data.control.discovery!.generation,
            expected_artifact_id:
              data.control.discovery!.configuration!.artifact_id,
            expected_version: data.control.discovery!.configuration!.version,
          })),
          entrypoint: {
            node_id: view.controller_node_id!,
            binding_id: view.binding_id!,
            expected_generation: d.generation,
            expected_artifact_id: c.artifact_id,
            expected_version: c.version,
          },
        };
        if (action && revision) {
          const { spec: _spec, ...targets } = input;
          await client.applicationLifecycle(
            cluster.id,
            revision.application_id,
            { ...targets, action },
          );
        } else
          await client.deployApplication(
            cluster.id,
            revision?.application_id ?? "",
            input,
            rollback?.id,
          );
      });
      if (!signal.aborted) {
        setDraft(null);
        setEditing(null);
        setChanging(null);
        if (action === "delete") setShowDeleted(true);
        setNotice(
          "Application deployment started. Follow each node under Activity → Deployments.",
        );
        await refresh.current();
        return true;
      }
      return false;
    } catch (cause) {
      if (!signal.aborted && !isActionCancelled(cause))
        setActionError(
          cause instanceof Error && !("status" in cause)
            ? cause.message
            : cause instanceof ApiError
              ? cause.message
              : haproxyErrorMessage(cause),
        );
      return false;
    } finally {
      busyRef.current = false;
      if (!signal.aborted) setBusy(false);
    }
  };
  const save = () => {
    if (!draft) return;
    const invalid = Object.keys(applicationDraftErrors(draft));
    if (invalid.length) {
      setValidatedFields((previous) => new Set([...previous, ...invalid]));
      setActionError(null);
      focusFormError(editorForm.current);
      return;
    }
    try {
      void deploy(applicationSpec(draft), editing);
    } catch (cause) {
      setActionError(
        cause instanceof Error
          ? cause.message
          : "Check the application settings.",
      );
    }
  };
  const displayPage = Math.min(
    page,
    Math.max(1, Math.ceil(visible.length / 10)),
  );
  const server = (
    index: number,
    patch: Partial<ApplicationDraft["servers"][number]>,
  ) =>
    setDraft((value) =>
      value
        ? {
            ...value,
            servers: value.servers.map((s, i) =>
              i === index ? { ...s, ...patch } : s,
            ),
          }
        : value,
    );
  return (
    <ApplicationFrame
      detail={!!applicationId}
      size="sm"
      title={cluster.name}
      description="Publish HTTP hostname and path routes on this cluster’s shared listener. Every deployment drains, updates and rejoins one node at a time."
      actions={
        <Button
          leadingIcon={<Plus />}
          disabledReason={
            reason ??
            (latest.filter((r) => !source(r).deleted).length >= 16
              ? "This cluster supports up to 16 applications."
              : undefined)
          }
          onPress={() => begin()}
        >
          Create application
        </Button>
      }
    >
      <div className="cv-haproxy-configuration">
        {error && (
          <ConnectionIssue message="Applications: refresh interrupted; retrying automatically." />
        )}
        {notice && (
          <InlineTip role="status" label="Status">
            {notice}
          </InlineTip>
        )}
        {healthError && (
          <ConnectionIssue message="Applications: refresh interrupted; retrying automatically." />
        )}
        {actionError && !draft && !changing && (
          <InlineTip variant="error" role="alert">
            {actionError}
          </InlineTip>
        )}
        {applicationId ? (
          !records ? (
            <LoadingStatus delayMs={1000}>Loading application…</LoadingStatus>
          ) : !latest.some((r) => r.application_id === applicationId) ? (
            <EmptyState
              title="Application not found"
              description="This application is not in the selected cluster."
            />
          ) : null
        ) : !records && !error ? (
          <LoadingStatus delayMs={1000}>Loading applications…</LoadingStatus>
        ) : records && latest.length === 0 ? (
          <EmptyState
            size="sm"
            title="No applications yet"
            description="Create an HTTP application with a hostname, path prefix and backend health check."
          />
        ) : (
          records && (
            <>
              <Checkbox
                isSelected={showDeleted}
                onChange={(value) => {
                  setShowDeleted(value);
                  setPage(1);
                }}
              >
                Show deleted applications
              </Checkbox>
              <SearchField
                aria-label="Find applications"
                placeholder="Find applications…"
                value={query}
                onChange={(value) => {
                  setPage(1);
                  onQueryChange?.(value);
                }}
              />
              <DataTable
                aria-label="HAProxy applications"
                rows={visible.slice((displayPage - 1) * 10, displayPage * 10)}
                rowLabel={(r) => r.spec.name}
                onRowAction={(id) => {
                  const row = latest.find((r) => r.id === id);
                  if (row) onOpen?.(row.application_id);
                }}
                columns={[
                  {
                    id: "name",
                    header: "Application",
                    isRowHeader: true,
                    cell: (r) => (
                      <AriaLink href={getHref?.(r.application_id) ?? "#"}>
                        {r.spec.name}
                      </AriaLink>
                    ),
                  },
                  {
                    id: "lifecycle",
                    header: "State",
                    cell: (r) => {
                      const state = applicationLifecycle(source(r));
                      const pending =
                        r.status === "running" || r.status === "rolling_back";
                      return (
                        <Badge
                          tone={state === "active" ? "success" : "neutral"}
                        >
                          {pending
                            ? "Updating"
                            : state === "deleted"
                              ? "Deleted"
                              : state === "disabled"
                                ? "Disabled"
                                : "Active"}
                        </Badge>
                      );
                    },
                  },
                  {
                    id: "reachability",
                    header: "Entrypoint checks",
                    cell: (r) => {
                      const state = currentHealth(
                        r.application_id,
                      )?.reachability;
                      return (
                        <Badge
                          tone={
                            state === "reachable"
                              ? "success"
                              : state === "unhealthy"
                                ? "error"
                                : "neutral"
                          }
                        >
                          {applicationHealthLabel(state)}
                        </Badge>
                      );
                    },
                  },
                  {
                    id: "route",
                    header: "HTTP route",
                    cell: (r) => `${r.spec.hostname}${r.spec.path_prefix}`,
                  },
                  {
                    id: "listener",
                    header: "Listener",
                    cell: (r) => r.spec.listener,
                  },
                  {
                    id: "serving",
                    header: "Last deployed",
                    cell: (r) => {
                      const serving = (records ?? [])
                        .filter(
                          (v) =>
                            v.application_id === r.application_id &&
                            v.status === "succeeded",
                        )
                        .sort((a, b) => b.number - a.number)[0];
                      return serving
                        ? `Version ${serving.number}`
                        : "Not verified";
                    },
                  },
                  {
                    id: "status",
                    header: "Latest deployment",
                    cell: (r) => (
                      <Badge tone={statusTone(r.status)}>
                        {rolloutStatus(r.status)}
                      </Badge>
                    ),
                  },
                ]}
                rowActions={(r) => (
                  <>
                    <MenuItem onAction={() => onOpen?.(r.application_id)}>
                      View application
                    </MenuItem>
                    <MenuItem
                      onAction={() => onOpen?.(r.application_id, "traffic")}
                    >
                      Traffic and requests
                    </MenuItem>
                    <MenuItem
                      disabledReason={
                        reason ??
                        (busy
                          ? "Wait for the current application change to finish."
                          : source(r).deleted
                            ? "Restore this application before editing its settings."
                            : undefined)
                      }
                      onAction={() => begin(r)}
                    >
                      Edit application
                    </MenuItem>
                    <MenuItem
                      onAction={() => onOpen?.(r.application_id, "deployments")}
                    >
                      Version history
                    </MenuItem>
                    <MenuItem
                      disabledReason={
                        reason ??
                        (busy
                          ? "Wait for the current application change to finish."
                          : undefined)
                      }
                      onAction={() => {
                        setActionError(null);
                        setChanging({
                          id: r.application_id,
                          action: source(r).deleted
                            ? "restore"
                            : source(r).spec.disabled
                              ? "enable"
                              : "disable",
                        });
                      }}
                    >
                      {applicationActionLabel(
                        source(r).deleted
                          ? "restore"
                          : source(r).spec.disabled
                            ? "enable"
                            : "disable",
                      )}
                    </MenuItem>
                    {!source(r).deleted && (
                      <MenuItem
                        disabledReason={
                          reason ??
                          (busy
                            ? "Wait for the current application change to finish."
                            : undefined)
                        }
                        onAction={() => {
                          setActionError(null);
                          setChanging({
                            id: r.application_id,
                            action: "delete",
                          });
                        }}
                      >
                        Delete application
                      </MenuItem>
                    )}
                    {source(r).deleted && (
                      <MenuItem
                        onAction={() => setConnecting(r.application_id)}
                      >
                        DNS cleanup instructions
                      </MenuItem>
                    )}
                  </>
                )}
              />
              <TablePagination
                page={displayPage}
                totalItems={visible.length}
                pageSize={10}
                onPageChange={setPage}
                itemLabel="applications"
              />
            </>
          )
        )}
        {applicationId && (
          <HAProxyApplicationDetails
            revision={
              latest.find((r) => r.application_id === applicationId) ?? null
            }
            revisions={records ?? []}
            health={currentHealth(applicationId)}
            now={Date.now()}
            tab={tab}
            links={applicationTabs.map((id) => ({
              id,
              label:
                id === "rules"
                  ? "Traffic rules"
                  : id[0]!.toUpperCase() + id.slice(1),
              href: getHref?.(applicationId, id) ?? "#",
            }))}
            permissionReason={
              reason ??
              (busy
                ? "Wait for the current application change to finish."
                : undefined)
            }
            dnsPermissionReason={permissionReason ?? dnsPermissionReason}
            dnsLabel="DNS instructions"
            onClose={() => onBack?.()}
            onEdit={(r) => begin(r)}
            onHistory={(id) => onOpen?.(id, "deployments")}
            onRestoreVersion={(r) => {
              const current = latest.find(
                (v) => v.application_id === r.application_id,
              );
              if (
                current &&
                r.id !== current.id &&
                !r.deleted &&
                r.status === "succeeded"
              )
                void deploy(r.spec, current, r);
            }}
            onLifecycle={(action) => {
              setChanging({ id: applicationId, action });
              setActionError(null);
            }}
            onConnectDNS={() => setConnecting(applicationId)}
            rules={
              tab === "rules" &&
              latest.some((r) => r.application_id === applicationId) ? (
                <HAProxyTrafficRules
                  client={client}
                  clusterId={cluster.id}
                  revision={latest.find(
                    (r) => r.application_id === applicationId,
                  )!}
                  effective={source(
                    latest.find((r) => r.application_id === applicationId)!,
                  )}
                  nodeIds={nodeIds}
                  nodeNames={
                    nodeNames ??
                    Object.fromEntries(nodeIds.map((id) => [id, id]))
                  }
                  busy={busy}
                  error={actionError}
                  permissionReason={reason}
                  onSave={async (policy, base) => {
                    const { traffic_policy: _previous, ...settings } =
                      base.spec;
                    return !!(await deploy(
                      {
                        ...settings,
                        ...(policy
                          ? { traffic_policy: policy, request_telemetry: true }
                          : {}),
                      },
                      base,
                    ));
                  }}
                  requestHref={getHref?.(applicationId, "traffic") ?? "#"}
                />
              ) : null
            }
            geography={
              tab === "geography" ? (
                <HAProxyApplicationGeography
                  key={applicationId}
                  client={client}
                  clusterId={cluster.id}
                  applicationId={applicationId}
                  nodeNames={
                    nodeNames ??
                    Object.fromEntries(nodeIds.map((id) => [id, id]))
                  }
                  requestHref={getHref?.(applicationId, "traffic") ?? "#"}
                />
              ) : null
            }
            traffic={
              tab === "traffic" &&
              latest.some((r) => r.application_id === applicationId) ? (
                <HAProxyApplicationTraffic
                  inline
                  clusterId={cluster.id}
                  application={source(
                    latest.find((r) => r.application_id === applicationId)!,
                  )}
                  client={client}
                  nodeNames={
                    nodeNames ??
                    Object.fromEntries(nodeIds.map((id) => [id, id]))
                  }
                  onClose={() => onOpen?.(applicationId)}
                  {...(!reason
                    ? {
                        onConfigure: () =>
                          begin(
                            latest.find(
                              (r) => r.application_id === applicationId,
                            ),
                          ),
                      }
                    : {})}
                />
              ) : null
            }
          />
        )}
        {connecting && latest.find((r) => r.application_id === connecting) && (
          <HAProxyConnectDNS
            key={connecting}
            client={client}
            revision={latest.find((r) => r.application_id === connecting)!}
            health={currentHealth(connecting)}
            cleanup={
              source(latest.find((r) => r.application_id === connecting)!)
                .deleted
                ? {
                    sharedHostname: latest.some(
                      (r) =>
                        r.application_id !== connecting &&
                        !source(r).deleted &&
                        r.spec.hostname ===
                          latest.find((a) => a.application_id === connecting)!
                            .spec.hostname,
                    ),
                  }
                : undefined
            }
            permissionReason={permissionReason ?? dnsPermissionReason}
            onClose={() => {
              if (connecting && !applicationId) onOpen?.(connecting);
              setConnecting(null);
            }}
          />
        )}
        <HAProxyApplicationLifecycle
          revision={changeTarget}
          action={changing?.action}
          busy={busy}
          error={actionError}
          permissionReason={reason}
          onClose={() => {
            setChanging(null);
            setActionError(null);
          }}
          onApply={() => {
            if (changing && changeTarget)
              void deploy(
                source(changeTarget).spec,
                changeTarget,
                undefined,
                changing.action,
              );
          }}
        />
        <Drawer
          size="lg"
          isOpen={!!draft}
          onOpenChange={(open) => {
            if (!open) closeEditor();
          }}
          isDismissable={!busy}
        >
          <DrawerDialog
            title={editing ? `Edit ${editing.spec.name}` : "Create application"}
            hideCloseButton={busy}
          >
            <DialogForm ref={editorForm} onSubmit={save}>
              <DrawerBody>
                {Object.keys(fieldErrors).length > 0 && (
                  <InlineTip
                    variant="error"
                    role="alert"
                    title="Check the highlighted fields"
                  >
                    Update the highlighted fields before deploying.
                  </InlineTip>
                )}
                {actionError && (
                  <InlineTip variant="error" role="alert">
                    {actionError}
                  </InlineTip>
                )}
                {editing &&
                  applicationEditSource(editing, records ?? []).id !==
                    editing.id && (
                    <InlineTip>
                      Starts from the last verified settings. Saving creates a
                      new revision.
                    </InlineTip>
                  )}
                <TextField
                  label="Application name"
                  isInvalid={!!fieldErrors.name}
                  errorDisplay="inline"
                  errorMessage={fieldErrors.name}
                  value={draft?.name ?? ""}
                  onChange={(name) => setDraft((v) => (v ? { ...v, name } : v))}
                  maxLength={80}
                  isRequired
                  isDisabled={busy}
                />
                <TextField
                  label="HTTP listener"
                  value={draft?.listener ?? ""}
                  isReadOnly
                  description="Uses the cluster’s declared traffic frontend. Existing fallback routing is preserved."
                />
                <TextField
                  label="Hostname"
                  isInvalid={!!fieldErrors.hostname}
                  errorDisplay="inline"
                  errorMessage={fieldErrors.hostname}
                  value={draft?.hostname ?? ""}
                  onChange={(hostname) =>
                    setDraft((v) => (v ? { ...v, hostname } : v))
                  }
                  maxLength={253}
                  placeholder="shop.example.com"
                  description={
                    fieldErrors.hostname
                      ? undefined
                      : "Exact hostname, without scheme or port. DNS is configured separately."
                  }
                  isRequired
                  isDisabled={busy}
                />
                <TextField
                  label="Path prefix"
                  isInvalid={!!fieldErrors.path_prefix}
                  errorDisplay="inline"
                  errorMessage={fieldErrors.path_prefix}
                  value={draft?.path_prefix ?? "/"}
                  onChange={(path_prefix) =>
                    setDraft((v) => (v ? { ...v, path_prefix } : v))
                  }
                  maxLength={128}
                  description="Longest matching prefix wins. The path is forwarded unchanged."
                  isRequired
                  isDisabled={busy}
                />
                <Select
                  label="Balancing strategy"
                  selectedKey={draft?.algorithm ?? "roundrobin"}
                  options={[
                    { id: "roundrobin", label: "Round robin" },
                    { id: "leastconn", label: "Least connections" },
                    { id: "first", label: "First available" },
                  ]}
                  onSelectionChange={(key) =>
                    setDraft((v) =>
                      v
                        ? {
                            ...v,
                            algorithm: key as HAProxyApplication["algorithm"],
                          }
                        : v,
                    )
                  }
                  isDisabled={busy}
                />
                <TextField
                  label="Health check path"
                  isInvalid={!!fieldErrors.health_path}
                  errorDisplay="inline"
                  errorMessage={fieldErrors.health_path}
                  value={draft?.health_path ?? "/healthz"}
                  onChange={(health_path) =>
                    setDraft((v) => (v ? { ...v, health_path } : v))
                  }
                  maxLength={128}
                  description="HTTP GET every second. HAProxy accepts 2xx/3xx; two passing checks mark the server ready, two failures mark it down."
                  isRequired
                  isDisabled={busy}
                />
                <Section
                  layout="stack"
                  size="sm"
                  title="Rollout request verification"
                  description="Require two fresh passing checks on each node before and after it rejoins traffic. A failed HTTP response or TLS check triggers automatic rollback."
                >
                  <Toolbar aria-label="Required request protocols">
                    {(["http", "https"] as const).map((scheme) => (
                      <Checkbox
                        key={scheme}
                        isSelected={draft?.verification?.[scheme] ?? false}
                        isDisabled={busy}
                        onChange={(selected) =>
                          setDraft((current) => {
                            if (!current) return current;
                            const policy = {
                              ...{ http: false, https: false },
                              ...current.verification,
                              [scheme]: selected,
                            };
                            const { verification: _previous, ...rest } =
                              current;
                            return policy.http || policy.https
                              ? { ...rest, verification: policy }
                              : rest;
                          })
                        }
                      >
                        Verify {scheme.toUpperCase()} requests
                      </Checkbox>
                    ))}
                  </Toolbar>
                  <Checkbox
                    isSelected={draft?.verification?.entrypoint ?? false}
                    isDisabled={busy}
                    onChange={(entrypoint) =>
                      setDraft((current) =>
                        current
                          ? {
                              ...current,
                              verification: {
                                http: true,
                                https: false,
                                ...current.verification,
                                entrypoint,
                              },
                            }
                          : current,
                      )
                    }
                  >
                    Also verify through the shared entrypoint after rejoin
                  </Checkbox>
                  <InlineTip>
                    Configure matching node checks on every agent. Entrypoint
                    verification also requires local entrypoint checks for each
                    selected protocol and upgraded agents. Destinations, trust
                    roots and expected responses stay in your infrastructure.
                    These checks prove access from those locations, not public
                    reachability.
                  </InlineTip>
                </Section>
                <Section
                  layout="stack"
                  size="sm"
                  title="Request telemetry"
                  description="Record application traffic and timing through the node’s private logging pipeline."
                >
                  <Checkbox
                    isSelected={draft?.request_telemetry ?? false}
                    isDisabled={busy}
                    onChange={(request_telemetry) =>
                      setDraft((v) => (v ? { ...v, request_telemetry } : v))
                    }
                  >
                    Collect request telemetry
                  </Checkbox>
                  <InlineTip>
                    Requires upgraded agents with a local request-log socket,
                    the private HAProxy logging profile and a configured log
                    store. Path collection follows each agent’s local policy.
                    Excludes full URLs, query strings, bodies, client addresses,
                    cookies and headers. Changing this setting deploys a new
                    version using the normal rolling update.
                  </InlineTip>
                </Section>
                {draft?.servers.map((s, i) => (
                  <Section
                    layout="stack"
                    key={i}
                    size="sm"
                    title={`Backend server ${i + 1}`}
                    actions={
                      <RemoveButton
                        label={`Remove backend server ${i + 1}`}
                        disabledReason={
                          busy
                            ? "Wait for deployment."
                            : draft.servers.length === 1
                              ? "Keep at least one backend server."
                              : undefined
                        }
                        onPress={() =>
                          setDraft((v) =>
                            v
                              ? {
                                  ...v,
                                  servers: v.servers.filter(
                                    (_, index) => index !== i,
                                  ),
                                }
                              : v,
                          )
                        }
                      />
                    }
                  >
                    <TextField
                      label={`Server ${i + 1} name`}
                      isInvalid={!!fieldErrors[`servers.${i}.name`]}
                      errorDisplay="inline"
                      errorMessage={fieldErrors[`servers.${i}.name`]}
                      value={s.name}
                      onChange={(name) => server(i, { name })}
                      isRequired
                      isDisabled={busy}
                    />
                    <TextField
                      label={`Server ${i + 1} address`}
                      isInvalid={!!fieldErrors[`servers.${i}.address`]}
                      errorDisplay="inline"
                      errorMessage={fieldErrors[`servers.${i}.address`]}
                      value={s.address}
                      onChange={(address) => server(i, { address })}
                      placeholder="10.0.0.20"
                      isRequired
                      isDisabled={busy}
                    />
                    <TextField
                      label={`Server ${i + 1} port`}
                      isInvalid={!!fieldErrors[`servers.${i}.port`]}
                      errorDisplay="inline"
                      errorMessage={fieldErrors[`servers.${i}.port`]}
                      value={s.port}
                      onChange={(port) => server(i, { port })}
                      inputMode="numeric"
                      isRequired
                      isDisabled={busy}
                    />
                    <TextField
                      label={`Server ${i + 1} weight`}
                      isInvalid={!!fieldErrors[`servers.${i}.weight`]}
                      errorDisplay="inline"
                      errorMessage={fieldErrors[`servers.${i}.weight`]}
                      value={s.weight}
                      onChange={(weight) => server(i, { weight })}
                      inputMode="numeric"
                      isRequired
                      isDisabled={busy}
                    />
                  </Section>
                ))}
                <div>
                  <Button
                    isDisabled={busy || (draft?.servers.length ?? 0) >= 16}
                    onPress={() =>
                      setDraft((v) =>
                        v
                          ? {
                              ...v,
                              servers: [
                                ...v.servers,
                                {
                                  name: `web-${v.servers.length + 1}`,
                                  address: "",
                                  port: "8080",
                                  weight: "100",
                                },
                              ],
                            }
                          : v,
                      )
                    }
                  >
                    Add backend server
                  </Button>
                </div>
                <InlineTip>
                  Deploys to all {nodeIds.length} nodes in {cluster.name}, with
                  connection draining and automatic rollback on failed health
                  checks. Other applications and unmanaged settings are
                  preserved. Progress continues when you close this page.
                </InlineTip>
              </DrawerBody>
              <DrawerFooter>
                <Button
                  type="submit"
                  variant="primary"
                  isDisabled={busy}
                  isLoading={busy}
                >
                  Save and deploy
                </Button>
                <Button isDisabled={busy} onPress={closeEditor}>
                  Cancel
                </Button>
              </DrawerFooter>
            </DialogForm>
          </DrawerDialog>
        </Drawer>
      </div>
    </ApplicationFrame>
  );
}

function ApplicationFrame({
  detail,
  ...props
}: ComponentProps<typeof Section> & { detail: boolean }) {
  return detail ? (
    <div className="cv-haproxy-configuration">{props.children}</div>
  ) : (
    <Section layout="stack" {...props} />
  );
}
