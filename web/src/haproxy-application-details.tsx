import { useEffect, useState, type ReactNode } from "react";
import {
  Badge,
  Button,
  DataTable,
  PaginatedDataTable,
  DescriptionItem,
  DescriptionList,
  EmptyState,
  InlineTip,
  IconButton,
  Menu,
  MenuItem,
  MenuSeparator,
  PageHeader,
  PageTabs,
  Section,
  TablePagination,
} from "@ecraela/ui";
import {
  ArrowLeft,
  ArrowPath,
  EllipsisHorizontal,
  PencilSquare,
  ScrollText,
  Trash,
  XCircle,
} from "@ecraela/ui/icons";
import type {
  HAProxyApplicationHealth,
  HAProxyApplicationProbe,
  HAProxyApplicationRevision,
  HAProxyApplicationAction,
} from "./haproxy-api";
import type { ApplicationTab } from "./haproxy-navigation";
import { rolloutStatus } from "./haproxy-rollouts";
export function probeStatus(
  p: HAProxyApplicationProbe,
  fresh: boolean,
  now = Date.now(),
): string {
  if (
    !fresh ||
    now - p.observed_at_ms > 15000 ||
    now - p.observed_at_ms < -1000
  )
    return "Unknown";
  if (p.error_code)
    return (
      (
        {
          connection_failed: "Connection failed",
          tls_failed: "TLS verification failed",
          response_status: `HTTP ${p.status_code}; expected ${p.expected_status}`,
          response_mismatch: "Response did not match",
          response_too_large: "Response exceeded check limit",
          deadline: "Check timed out",
          route_conflict: "Path belongs to another route",
        } as Record<string, string>
      )[p.error_code] ?? "Unknown"
    );
  return p.status_code === p.expected_status &&
    (p.scheme !== "https" || p.tls_verified)
    ? "Passed"
    : "Unknown";
}
export const applicationHealthLabel = (value?: string) =>
  (
    ({
      loaded: "Loaded",
      updating: "Updating",
      drifted: "Different configuration",
      reachable: "Reachable",
      unhealthy: "Unhealthy",
      unknown: "Unknown",
      disabled: "Disabled",
      deleted: "Deleted",
    }) as Record<string, string>
  )[value ?? "unknown"] ?? "Unknown";
export function currentApplicationHealth(
  health: HAProxyApplicationHealth | undefined,
  now = Date.now(),
): HAProxyApplicationHealth | undefined {
  if (!health) return undefined;
  const nodes = health.nodes.map((n) => ({
    ...n,
    fresh:
      n.fresh &&
      now - n.observed_at_ms <= 6000 &&
      now - n.observed_at_ms >= -1000,
  }));
  const complete =
    nodes.length > 0 &&
    nodes.every(
      (n) =>
        n.fresh &&
        n.loaded &&
        n.probes.length > 0 &&
        n.probes.every(
          (p) =>
            p.spec_digest === n.spec_digest &&
            now - p.observed_at_ms <= 15000 &&
            now - p.observed_at_ms >= -1000,
        ),
    );
  const failed = nodes.some(
    (n) =>
      n.fresh &&
      n.loaded &&
      n.probes.some(
        (p) =>
          now - p.observed_at_ms <= 15000 &&
          now - p.observed_at_ms >= -1000 &&
          p.spec_digest === n.spec_digest &&
          !!p.error_code,
      ),
  );
  return {
    ...health,
    nodes,
    configuration: nodes.every((n) => n.fresh)
      ? health.configuration
      : "unknown",
    reachability:
      (health.reachability === "disabled" ||
        health.reachability === "deleted") &&
      nodes.length > 0 &&
      nodes.every((n) => n.fresh && n.loaded) &&
      health.configuration === "loaded"
        ? health.reachability
        : health.reachability === "unhealthy" && failed
          ? "unhealthy"
          : health.reachability === "reachable" && complete
            ? "reachable"
            : "unknown",
  };
}
const tone = (s: string) =>
  s === "Passed" || s === "Reachable" || s === "Loaded"
    ? ("success" as const)
    : s === "Unknown" || s === "Updating" || s === "Disabled" || s === "Deleted"
      ? ("neutral" as const)
      : ("error" as const);

export function HAProxyApplicationDetails({
  revision,
  revisions,
  health,
  onClose,
  onEdit,
  onHistory,
  onRestoreVersion,
  onConnectDNS,
  dnsPermissionReason,
  permissionReason,
  onLifecycle,
  now,
  tab,
  links,
  traffic,
  geography,
  rules,
  dnsLabel,
}: {
  revision: HAProxyApplicationRevision | null;
  revisions: HAProxyApplicationRevision[];
  health?: HAProxyApplicationHealth | undefined;
  onClose: () => void;
  onEdit: (r: HAProxyApplicationRevision) => void;
  onHistory: (id: string) => void;
  onRestoreVersion: (revision: HAProxyApplicationRevision) => void;
  onConnectDNS?: () => void;
  dnsPermissionReason?: string | undefined;
  permissionReason?: string | undefined;
  onLifecycle?: ((action: HAProxyApplicationAction) => void) | undefined;
  now: number;
  tab: ApplicationTab;
  links: { id: string; label: string; href: string }[];
  traffic?: ReactNode;
  geography?: ReactNode;
  rules?: ReactNode;
  dnsLabel?: string;
}) {
  const [pages, setPages] = useState({ checks: 1, servers: 1, deployments: 1 });
  useEffect(() => {
    setPages({ checks: 1, servers: 1, deployments: 1 });
  }, [revision?.application_id]);
  if (!revision) return null;
  const source =
    revision.status === "rolled_back" || revision.status === "rollback_failed"
      ? (revisions
          .filter(
            (r) =>
              r.application_id === revision.application_id &&
              r.status === "succeeded",
          )
          .sort((a, b) => b.number - a.number)[0] ?? revision)
      : revision;
  const spec = source.spec;
  const checks =
    health?.nodes.flatMap((n) =>
      n.probes.map((p) => ({
        ...p,
        id: `${n.node_id}/${p.name}`,
        location: n.name,
        result: probeStatus(
          p,
          n.fresh && n.loaded && p.spec_digest === n.spec_digest,
          now,
        ),
      })),
    ) ?? [];
  const servers =
    health?.nodes.flatMap((n) =>
      n.servers.map((s) => ({
        ...s,
        id: `${n.node_id}/${s.name}`,
        location: n.name,
        fresh: n.fresh,
      })),
    ) ?? [];
  const deployments = revisions
    .filter((r) => r.application_id === revision.application_id)
    .sort((a, b) => b.number - a.number);
  const page = (kind: keyof typeof pages, count: number) =>
    Math.min(pages[kind], Math.max(1, Math.ceil(count / 10)));
  const paginate = (kind: keyof typeof pages, count: number, label: string) =>
    count > 10 ? (
      <TablePagination
        page={page(kind, count)}
        pageSize={10}
        totalItems={count}
        itemLabel={label}
        onPageChange={(value) =>
          setPages((current) => ({ ...current, [kind]: value }))
        }
      />
    ) : null;
  const config = applicationHealthLabel(health?.configuration),
    reachability = applicationHealthLabel(health?.reachability);
  const editReason =
    permissionReason ??
    (source.deleted
      ? "Restore this application before editing its settings."
      : undefined);
  const lifecycleAction = source.deleted
    ? "restore"
    : spec.disabled
      ? "enable"
      : "disable";
  return (
    <div className="cv-haproxy-configuration">
      <PageHeader
        title={spec.name}
        description={`${spec.hostname}${spec.path_prefix}`}
        actions={
          <>
            <Button leadingIcon={<ArrowLeft />} onPress={onClose}>
              All applications
            </Button>
            <Menu
              aria-label={`Actions for ${spec.name}`}
              trigger={
                <IconButton
                  variant="ghost"
                  aria-label={`Actions for ${spec.name}`}
                >
                  <EllipsisHorizontal />
                </IconButton>
              }
            >
              <MenuItem
                icon={<PencilSquare />}
                isDisabled={!!editReason}
                description={editReason}
                onAction={() => onEdit(revision)}
              >
                Edit application
              </MenuItem>
              <MenuItem
                icon={<ScrollText />}
                onAction={() => onHistory(revision.application_id)}
              >
                Version history
              </MenuItem>
              {onLifecycle && (
                <>
                  <MenuSeparator />
                  <MenuItem
                    icon={
                      lifecycleAction === "disable" ? (
                        <XCircle />
                      ) : (
                        <ArrowPath />
                      )
                    }
                    tone={lifecycleAction === "disable" ? "danger" : "default"}
                    isDisabled={!!permissionReason}
                    description={permissionReason}
                    onAction={() => onLifecycle(lifecycleAction)}
                  >
                    {source.deleted
                      ? "Restore application"
                      : spec.disabled
                        ? "Re-enable application"
                        : "Disable application"}
                  </MenuItem>
                  {!source.deleted && (
                    <MenuItem
                      icon={<Trash />}
                      tone="danger"
                      isDisabled={!!permissionReason}
                      description={permissionReason}
                      onAction={() => onLifecycle("delete")}
                    >
                      Delete application
                    </MenuItem>
                  )}
                </>
              )}
            </Menu>
          </>
        }
      />
      <PageTabs
        aria-label="Application sections"
        currentId={tab}
        items={links}
      />
      {(source.deleted || spec.disabled) && (
        <InlineTip>
          {source.deleted
            ? "This application has been removed from the nodes. History remains available for restoration; shared listeners, TLS and DNS are retained."
            : "This route is reserved and returns 503 while disabled. Re-enable it to restore its saved backends."}
        </InlineTip>
      )}
      {tab === "overview" && (
        <>
          <Section layout="stack" title="Delivery status">
            <DescriptionList columns={4}>
              <DescriptionItem label="Configuration">
                <Badge tone={tone(config)}>{config}</Badge>
              </DescriptionItem>
              <DescriptionItem label="Entrypoint checks">
                <Badge tone={tone(reachability)}>{reachability}</Badge>
              </DescriptionItem>
              <DescriptionItem label="Latest deployment">
                Version {revision.number} – {rolloutStatus(revision.status)}
              </DescriptionItem>
              <DescriptionItem label="Loaded on nodes">
                {health?.nodes.filter((n) => n.fresh && n.loaded).length ?? 0} /{" "}
                {health?.nodes.length ?? 0}
              </DescriptionItem>
            </DescriptionList>
          </Section>
          <Section
            layout="stack"
            title={source === revision ? "Routing" : "Last verified routing"}
            actions={
              onConnectDNS ? (
                <Button
                  onPress={onConnectDNS}
                  disabledReason={dnsPermissionReason}
                >
                  {source.deleted
                    ? "DNS cleanup"
                    : (dnsLabel ?? "DNS instructions")}
                </Button>
              ) : undefined
            }
          >
            <DescriptionList columns={3}>
              <DescriptionItem label="Hostname">
                {spec.hostname}
              </DescriptionItem>
              <DescriptionItem label="Path prefix">
                {spec.path_prefix}
              </DescriptionItem>
              <DescriptionItem label="Listener">
                {spec.listener}
              </DescriptionItem>
              <DescriptionItem label="Balancing strategy">
                {spec.algorithm}
              </DescriptionItem>
              <DescriptionItem label="Backend health path">
                {spec.health_path}
              </DescriptionItem>
              <DescriptionItem label="Rollout verification">
                {spec.verification
                  ? [
                      spec.verification.http ? "HTTP" : null,
                      spec.verification.https ? "HTTPS" : null,
                    ]
                      .filter(Boolean)
                      .join(" and ") +
                    (spec.verification.entrypoint
                      ? ", including the shared entrypoint"
                      : " on each node")
                  : "Node health checks"}
              </DescriptionItem>
            </DescriptionList>
          </Section>
          <Section
            layout="stack"
            title="HTTP and HTTPS checks"
            description="Checks run from the listed customer-side locations. They do not establish public internet reachability; missing or expired evidence is unknown."
          >
            <DataTable
              aria-label="Application entrypoint checks"
              rows={checks.slice(
                (page("checks", checks.length) - 1) * 10,
                page("checks", checks.length) * 10,
              )}
              rowLabel={(p) => p.name}
              columns={[
                {
                  id: "name",
                  header: "Check",
                  isRowHeader: true,
                  cell: (p) => p.name,
                },
                {
                  id: "result",
                  header: "Result",
                  cell: (p) => <Badge tone={tone(p.result)}>{p.result}</Badge>,
                },
                { id: "from", header: "From", cell: (p) => p.location },
                {
                  id: "request",
                  header: "Request",
                  cell: (p) => `${p.scheme.toUpperCase()} ${p.path}`,
                },
                {
                  id: "tls",
                  header: "TLS verification",
                  cell: (p) =>
                    p.scheme === "https"
                      ? p.result === "Unknown"
                        ? "Unknown"
                        : p.tls_verified
                          ? "Verified"
                          : "Failed"
                      : "Not applicable",
                },
                {
                  id: "duration",
                  header: "Duration",
                  cell: (p) => `${p.duration_ms} ms`,
                },
              ]}
              emptyState={
                <EmptyState
                  title="No fresh entrypoint checks"
                  description="Configure a local probe policy for this application's hostname and route."
                />
              }
            />
            {paginate("checks", checks.length, "checks")}
          </Section>
        </>
      )}
      {tab === "backends" && (
        <>
          <Section
            layout="stack"
            title="Configured backends"
            description="Saved endpoint settings. Edit application to deploy changes across the cluster."
          >
            <PaginatedDataTable
              aria-label="Application configured backends"
              itemLabel="servers"
              rows={spec.servers.map((s) => ({ ...s, id: s.name }))}
              rowLabel={(s) => s.name}
              columns={[
                {
                  id: "name",
                  header: "Server",
                  isRowHeader: true,
                  cell: (s) => s.name,
                },
                {
                  id: "address",
                  header: "Address",
                  cell: (s) => `${s.address}:${s.port}`,
                },
                { id: "weight", header: "Weight", cell: (s) => s.weight },
              ]}
            />
          </Section>
          <Section layout="stack" title="Observed backend health">
            <DataTable
              aria-label="Application backend health"
              rows={servers.slice(
                (page("servers", servers.length) - 1) * 10,
                page("servers", servers.length) * 10,
              )}
              rowLabel={(s) => s.name}
              columns={[
                {
                  id: "name",
                  header: "Server",
                  isRowHeader: true,
                  cell: (s) => s.name,
                },
                { id: "node", header: "Node", cell: (s) => s.location },
                {
                  id: "health",
                  header: "Health",
                  cell: (s) => (
                    <Badge
                      tone={
                        !s.fresh
                          ? "neutral"
                          : s.health.startsWith("UP")
                            ? "success"
                            : "error"
                      }
                    >
                      {s.fresh ? s.health : "Unknown"}
                    </Badge>
                  ),
                },
                {
                  id: "state",
                  header: "State",
                  cell: (s) => (s.fresh ? s.state : "Unknown"),
                },
                {
                  id: "weight",
                  header: "Weight",
                  cell: (s) => (s.fresh ? s.weight : "Unknown"),
                },
              ]}
              emptyState={
                <EmptyState
                  title="No backend observations"
                  description="Waiting for fresh observations from the node agents."
                />
              }
            />
            {paginate("servers", servers.length, "servers")}
          </Section>
        </>
      )}
      {tab === "traffic" && traffic}
      {tab === "geography" && geography}
      {tab === "rules" && rules}
      {tab === "deployments" && (
        <>
          {permissionReason && <InlineTip>{permissionReason}</InlineTip>}
          <InlineTip>
            Restoring a verified version creates a new revision and rolling
            deployment. Other applications keep their current settings.
          </InlineTip>
          <Section layout="stack" title="Deployment history">
            <DataTable
              aria-label="Application deployments"
              rows={deployments.slice(
                (page("deployments", deployments.length) - 1) * 10,
                page("deployments", deployments.length) * 10,
              )}
              rowLabel={(r) => `Version ${r.number}`}
              columns={[
                {
                  id: "version",
                  header: "Version",
                  isRowHeader: true,
                  cell: (r) => `Version ${r.number}`,
                },
                {
                  id: "status",
                  header: "Result",
                  cell: (r) => rolloutStatus(r.status),
                },
                {
                  id: "state",
                  header: "State",
                  cell: (r) =>
                    r.deleted
                      ? "Deleted"
                      : r.spec.disabled
                        ? "Disabled"
                        : "Enabled",
                },
                {
                  id: "route",
                  header: "Route",
                  cell: (r) => r.spec.hostname + r.spec.path_prefix,
                },
                {
                  id: "health",
                  header: "Health check",
                  cell: (r) => r.spec.health_path,
                },
                {
                  id: "traffic_policy",
                  header: "Traffic policy",
                  cell: (r) =>
                    r.spec.traffic_policy
                      ? `${r.spec.traffic_policy.mode === "observe" ? "Observe" : "Enforce"} – ${r.spec.traffic_policy.rules.length} ${r.spec.traffic_policy.rules.length === 1 ? "rule" : "rules"}, unknown ${r.spec.traffic_policy.unknown_action}`
                      : "None",
                },
              ]}
              rowActions={(r) => (
                <MenuItem
                  disabledReason={
                    permissionReason ??
                    (r.id === revision.id
                      ? "This is already the latest application version."
                      : r.deleted
                        ? "Choose a retained version from before the application was deleted."
                        : r.status !== "succeeded"
                          ? "Only successfully deployed versions can be restored."
                          : undefined)
                  }
                  onAction={() => onRestoreVersion(r)}
                >
                  Restore application version
                </MenuItem>
              )}
            />
            {paginate("deployments", deployments.length, "versions")}
          </Section>
          <Section layout="stack" title="Loaded revisions">
            <PaginatedDataTable
              aria-label="Application loaded revisions"
              itemLabel="nodes"
              rows={(health?.nodes ?? []).map((n) => ({ ...n, id: n.node_id }))}
              rowLabel={(n) => n.name}
              columns={[
                {
                  id: "name",
                  header: "Node",
                  isRowHeader: true,
                  cell: (n) => n.name,
                },
                {
                  id: "version",
                  header: "Matching version",
                  cell: (n) =>
                    n.fresh && n.revision ? `Version ${n.revision}` : "Unknown",
                },
                {
                  id: "loaded",
                  header: "Configuration",
                  cell: (n) =>
                    n.fresh
                      ? n.loaded
                        ? "Loaded"
                        : "Different configuration"
                      : "Unknown",
                },
              ]}
            />
          </Section>
        </>
      )}
    </div>
  );
}
