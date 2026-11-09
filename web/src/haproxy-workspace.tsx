import { ConnectionIssue } from "./connection-health";
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { Link as AriaLink } from "@ecraela/ui";
import { useLocation, useNavigate } from "react-router-dom";
import {
  Badge,
  Breadcrumbs,
  Button,
  DataTable,
  PaginatedDataTable,
  DescriptionItem,
  DescriptionList,
  EmptyState,
  IconButton,
  LoadingStatus,
  Menu,
  MenuItem,
  MenuSeparator,
  PageHeader,
  PageTabs,
  SearchField,
  Section,
  Select,
  TablePagination,
} from "@ecraela/ui";
import { ArrowPath, EllipsisHorizontal, XCircle } from "@ecraela/ui/icons";
import type { HAProxyClient, HAProxyFleet, HAProxyNode } from "./haproxy-api";
import { nodeStatus } from "./haproxy-metrics";
import {
  haproxySections,
  haproxyPath,
  readHAProxyRoute,
  historyRange,
  selectedHAProxyCluster,
  clusterTabs,
  nodeTabs,
} from "./haproxy-navigation";
import { HAProxyClusters } from "./haproxy-clusters";
import { HAProxyApplications } from "./haproxy-applications";
import { HAProxyClusterOverview } from "./haproxy-cluster";
import { HAProxyNodeDetail } from "./haproxy-node";
import { HAProxyIncidents } from "./haproxy-incidents";
import { HAProxyRollouts } from "./haproxy-rollouts";
import { HAProxyListeners } from "./haproxy-listeners";
import { HAProxyAutomaticTLS } from "./haproxy-acme";

export function HAProxyWorkspace({
  basePath,
  fleet,
  client,
  loading,
  status,
  settingsContent,
  permissionReason,
  dnsPermissionReason,
  onRefresh,
  onCreateCluster,
  onEnroll,
  onRevoke,
  onLoadMore,
}: {
  basePath: string;
  fleet: HAProxyFleet | null;
  client: HAProxyClient | null;
  loading: boolean;
  status: ReactNode;
  settingsContent?: ReactNode;
  permissionReason?: string | undefined;
  dnsPermissionReason?: string | undefined;
  onRefresh: () => void;
  onCreateCluster: () => void;
  onEnroll: (clusterId: string) => void;
  onRevoke: (node: HAProxyNode) => void;
  onLoadMore: () => Promise<void>;
}) {
  const [applicationLabel, setApplicationLabel] = useState<{
    key: string;
    name: string;
  } | null>(null);
  const location = useLocation(),
    navigate = useNavigate();
  const route = readHAProxyRoute(location.pathname, basePath);
  const params = new URLSearchParams(location.search);
  const range = historyRange(params.get("range"));
  const node =
    route.kind === "node"
      ? fleet?.nodes.find((node) => node.id === route.id)
      : undefined;
  const requestedCluster = selectedHAProxyCluster(
    route,
    params.get("cluster"),
    fleet?.nodes ?? [],
  );
  const cluster = fleet?.clusters.find(
    (cluster) => cluster.id === requestedCluster,
  );
  const clusters = cluster ? [cluster] : [];
  const reportApplicationName = useCallback(
    (id: string, name: string) => {
      const key = `${cluster?.id}/${id}`;
      setApplicationLabel((current) =>
        current?.key === key && current.name === name ? current : { key, name },
      );
    },
    [cluster?.id],
  );
  const filter = requestedCluster ?? "";
  const clusterIndex =
    route.kind === "list" && route.section !== "settings" && !requestedCluster;
  const installationSettings =
    route.kind === "list" && route.section === "settings" && !requestedCluster;
  const settingsTabs = [
    ...(settingsContent ? [{ id: "access", label: "Access" }] : []),
    { id: "software", label: "Software" },
    { id: "recovery", label: "Backups & recovery" },
  ];
  const settingsTab = settingsTabs.some(
    (tab) => tab.id === params.get("settings_tab"),
  )
    ? params.get("settings_tab")!
    : settingsTabs[0]!.id;
  const indexParams = new URLSearchParams();
  for (const key of ["cluster_query", "cluster_health", "cluster_page"])
    if (params.has(key)) indexParams.set(key, params.get(key)!);
  const clusterIndexHref = haproxyPath(
    basePath,
    [],
    indexParams.size ? "?" + indexParams : "",
  );
  const setParam = (key: string, value: string) => {
    const next = new URLSearchParams(location.search);
    next.set(key, value);
    if (key === "node_query") next.delete("node_page");
    if (key === "cluster_query" || key === "cluster_health")
      next.delete("cluster_page");
    navigate(
      { pathname: location.pathname, search: "?" + next.toString() },
      { replace: true },
    );
  };
  const href = (parts: readonly string[]) => {
    const next = new URLSearchParams(location.search);
    const target =
      parts[0] === "applications" && parts.length > 1
        ? parts[1]
        : parts[0] === "infrastructure" && parts[1] === "clusters"
          ? parts[2]
          : parts[0] === "infrastructure" && parts[1] === "nodes"
            ? fleet?.nodes.find((node) => node.id === parts[2])?.cluster_id
            : requestedCluster;
    if (target) next.set("cluster", target);
    else next.delete("cluster");
    const targetParts =
      parts.length === 1 && parts[0] === "infrastructure" && target
        ? ["infrastructure", "clusters", target, "nodes"]
        : parts;
    return haproxyPath(basePath, targetParts, next.size ? "?" + next : "");
  };
  const clusterHref = (id: string) => {
    const next = new URLSearchParams(indexParams);
    next.set("cluster", id);
    if (params.has("range")) next.set("range", range);
    return haproxyPath(basePath, ["overview"], "?" + next);
  };
  const openNode = (id: string) =>
    navigate(href(["infrastructure", "nodes", id, "metrics"]));
  const openApp = (clusterId: string, id: string, tab = "overview") =>
    navigate(href(["applications", clusterId, id, tab]));
  const scopedNodes =
    fleet?.nodes.filter((n) => clusters.some((c) => c.id === n.cluster_id)) ??
    [];
  const cursor = useRef<string | null>(null);
  useEffect(() => {
    if (
      route.kind !== "node" ||
      node ||
      !fleet?.next_cursor ||
      cursor.current === fleet.next_cursor
    )
      return;
    cursor.current = fleet.next_cursor;
    void onLoadMore().catch(() => {});
  }, [route.kind, node, fleet?.next_cursor, onLoadMore]);
  const title =
    route.kind === "node"
      ? (node?.name ?? "Node")
      : clusterIndex
        ? "HAProxy"
        : installationSettings
          ? "Settings"
          : route.section === "overview"
            ? (cluster?.name ?? "Overview")
            : haproxySections.find((s) => s.id === route.section)!.label;
  const clusterNodes = (id: string) =>
    fleet?.nodes.filter((n) => n.cluster_id === id) ?? [];
  const activeIds = (id: string) =>
    clusterNodes(id)
      .filter((n) => !n.revoked_at)
      .map((n) => n.id);
  const names = (id: string) =>
    Object.fromEntries(clusterNodes(id).map((n) => [n.id, n.name]));
  const changeReason =
    permissionReason ??
    (fleet?.next_cursor
      ? "Load the remaining fleet nodes before changing cluster configuration."
      : undefined);
  const clusterOverview = (
    id: string,
    view: "overview" | "topology" | "activity",
  ) => {
    const c = fleet?.clusters.find((c) => c.id === id);
    return c && client ? (
      <HAProxyClusterOverview
        key={c.id}
        view={view}
        cluster={c}
        nodes={clusterNodes(c.id)}
        client={client}
        selectedId={null}
        onSelect={openNode}
        range={range}
        onRangeChange={(value) => setParam("range", value)}
      />
    ) : null;
  };
  const appProps =
    cluster && client
      ? {
          cluster,
          client,
          nodeIds: activeIds(cluster.id),
          nodeNames: names(cluster.id),
          permissionReason: changeReason,
          dnsPermissionReason,
          getHref: (id: string, tab = "overview") =>
            href(["applications", cluster.id, id, tab]),
          onOpen: (id: string, tab?: string) => openApp(cluster.id, id, tab),
          onBack: () => navigate(href(["applications"])),
          onTitleChange: reportApplicationName,
          query: params.get("app_query") ?? "",
          onQueryChange: (value: string) => setParam("app_query", value),
        }
      : null;
  const nodeInventory =
    fleet && client ? (
      <NodeInventory
        fleet={fleet}
        filter={filter}
        query={params.get("node_query") ?? ""}
        page={Math.trunc(Number(params.get("node_page"))) || 1}
        onQuery={(value) => setParam("node_query", value)}
        onPage={(value) => setParam("node_page", String(value))}
        href={(id) => href(["infrastructure", "nodes", id, "metrics"])}
        onOpen={openNode}
      />
    ) : null;
  const labelForTab = (tab: string) =>
    tab === "tls"
      ? "Automatic TLS"
      : tab === "rules"
        ? "Traffic rules"
        : tab[0]!.toUpperCase() + tab.slice(1);
  const breadcrumbItems: { label: string; href?: string }[] = [
    { label: "Clusters", href: clusterIndexHref },
  ];
  if (installationSettings) {
    breadcrumbItems.push(
      { label: "Settings", href: haproxyPath(basePath, ["settings"]) },
      { label: settingsTabs.find((tab) => tab.id === settingsTab)!.label },
    );
  } else if (cluster) {
    breadcrumbItems.push({ label: cluster.name, href: href(["overview"]) });
    const sectionLabel = haproxySections.find(
      (section) => section.id === route.section,
    )!.label;
    if (route.kind === "application") {
      const name =
        applicationLabel?.key === `${cluster.id}/${route.id}`
          ? applicationLabel.name
          : (client
              ?.cachedApplications(cluster.id)
              ?.find((row) => row.application_id === route.id)?.spec.name ??
            "Application");
      breadcrumbItems.push(
        { label: sectionLabel, href: href(["applications"]) },
        {
          label: name,
          href: href(["applications", cluster.id, route.id, "overview"]),
        },
        { label: labelForTab(route.tab) },
      );
    } else if (route.section === "infrastructure") {
      breadcrumbItems.push({
        label: "Infrastructure",
        href: href(["infrastructure"]),
      });
      if (route.kind === "node")
        breadcrumbItems.push(
          { label: "Nodes", href: href(["infrastructure"]) },
          {
            label: node?.name ?? "Node",
            href: href(["infrastructure", "nodes", route.id, "metrics"]),
          },
          { label: labelForTab(route.tab) },
        );
      else
        breadcrumbItems.push({
          label: labelForTab(route.kind === "cluster" ? route.tab : "nodes"),
        });
    } else breadcrumbItems.push({ label: sectionLabel });
  } else
    breadcrumbItems.push({
      label: route.kind === "not-found" ? "Page not found" : title,
    });
  useEffect(() => {
    let canonical: string | undefined;
    if (route.kind === "list" && route.section === "infrastructure" && cluster)
      canonical = haproxyPath(basePath, [
        "infrastructure",
        "clusters",
        cluster.id,
        "nodes",
      ]);
    if (route.kind === "node" && !location.pathname.endsWith("/" + route.tab))
      canonical = haproxyPath(basePath, [
        "infrastructure",
        "nodes",
        route.id,
        route.tab,
      ]);
    if (
      route.kind === "application" &&
      !location.pathname.endsWith("/" + route.tab)
    )
      canonical = haproxyPath(basePath, [
        "applications",
        route.clusterId,
        route.id,
        route.tab,
      ]);
    if (canonical && canonical !== location.pathname)
      navigate(
        { pathname: canonical, search: location.search },
        { replace: true },
      );
  }, [location.pathname, location.search, cluster?.id, basePath, navigate]);
  return (
    <div className="cv-haproxy">
      {!clusterIndex && <Breadcrumbs items={breadcrumbItems} />}
      {cluster && (
        <PageTabs
          aria-label="Cluster navigation"
          currentId={route.section}
          items={haproxySections.map((s) => ({ ...s, href: href([s.id]) }))}
        />
      )}
      {route.kind !== "application" && (
        <PageHeader
          title={title}
          description={
            clusterIndex
              ? "Your HAProxy clusters, their health and recorded traffic."
              : installationSettings
                ? "Local access, connections and recovery for this installation."
                : {
                    overview: "Health and traffic for this HAProxy cluster.",
                    applications:
                      "Route applications through the load balancers you own.",
                    infrastructure:
                      "Topology and the nodes that serve this cluster’s applications.",
                    activity:
                      "Investigate incidents and follow configuration changes.",
                    settings: "Agent software and recovery for this cluster.",
                  }[route.section]
          }
          actions={
            fleet ? (
              <>
                {clusterIndex && (
                  <Button
                    variant="primary"
                    onPress={onCreateCluster}
                    disabledReason={permissionReason}
                  >
                    Create cluster
                  </Button>
                )}
                {(route.kind === "list" ||
                  (route.kind === "cluster" && route.tab === "nodes")) &&
                  route.section === "infrastructure" &&
                  cluster && (
                    <Button
                      variant="primary"
                      onPress={() => onEnroll(cluster.id)}
                      disabledReason={permissionReason}
                    >
                      Enroll node
                    </Button>
                  )}
                <Menu
                  aria-label={`Actions for ${title}`}
                  trigger={
                    <IconButton
                      aria-label={`Actions for ${title}`}
                      variant="ghost"
                    >
                      <EllipsisHorizontal />
                    </IconButton>
                  }
                >
                  <MenuItem
                    icon={<ArrowPath />}
                    isDisabled={loading}
                    onAction={onRefresh}
                  >
                    Refresh now
                  </MenuItem>
                  {node && !node.revoked_at && (
                    <>
                      <MenuSeparator />
                      <MenuItem
                        icon={<XCircle />}
                        tone="danger"
                        isDisabled={!!changeReason}
                        description={changeReason}
                        onAction={() => onRevoke(node)}
                      >
                        Revoke access
                      </MenuItem>
                    </>
                  )}
                </Menu>
              </>
            ) : undefined
          }
        />
      )}
      {route.section === "infrastructure" && cluster && (
        <PageTabs
          aria-label="Infrastructure sections"
          currentId={route.kind === "cluster" ? route.tab : "nodes"}
          items={clusterTabs.map((tab) => ({
            id: tab,
            label: labelForTab(tab),
            href: href(["infrastructure", "clusters", cluster.id, tab]),
          }))}
        />
      )}
      {installationSettings && (
        <PageTabs
          aria-label="Settings navigation"
          currentId={settingsTab}
          items={settingsTabs.map((tab) => ({
            ...tab,
            href: haproxyPath(
              basePath,
              ["settings"],
              "?settings_tab=" + tab.id,
            ),
          }))}
        />
      )}
      {installationSettings && settingsTab === "access" && settingsContent}
      {route.kind === "not-found" ? (
        <EmptyState
          title="Page not found"
          description="Return to the cluster list to choose a cluster."
        />
      ) : !fleet || !client ? (
        <>
          {status}
          {route.section !== "settings" && (
            <AriaLink href={haproxyPath(basePath, ["settings"])}>
              Open connection settings
            </AriaLink>
          )}
        </>
      ) : (
        <>
          {status}
          {fleet.partial && (
            <ConnectionIssue message="Some fleet observations are unavailable." />
          )}
          {!fleet.clusters.length && route.section !== "settings" ? (
            <EmptyState
              title="No HAProxy clusters"
              description="Create a cluster, then enroll the HAProxy nodes you already run."
              actions={
                <Button
                  variant="primary"
                  disabledReason={permissionReason}
                  onPress={onCreateCluster}
                >
                  Create cluster
                </Button>
              }
            />
          ) : clusterIndex ? (
            <HAProxyClusters
              fleet={fleet}
              client={client}
              href={clusterHref}
              onOpen={(id) => navigate(clusterHref(id))}
              query={params.get("cluster_query") ?? ""}
              onQuery={(value) => setParam("cluster_query", value)}
              health={params.get("cluster_health") ?? "all"}
              onHealth={(value) => setParam("cluster_health", value)}
              page={Math.trunc(Number(params.get("cluster_page"))) || 1}
              onPage={(value) => setParam("cluster_page", String(value))}
            />
          ) : !cluster && !installationSettings && route.kind !== "node" ? (
            <EmptyState
              title="Cluster not found"
              description="Choose an available cluster from the cluster list."
              actions={
                <AriaLink href={clusterIndexHref}>Back to clusters</AriaLink>
              }
            />
          ) : (
            <>
              {route.section === "overview" && (
                <>
                  <DescriptionList columns={3}>
                    <DescriptionItem label="Enrolled nodes">
                      {scopedNodes.filter((n) => !n.revoked_at).length}
                    </DescriptionItem>
                    <DescriptionItem label="Reporting">
                      {
                        scopedNodes.filter(
                          (n) =>
                            n.telemetry_fresh && n.scrape_ok && !n.revoked_at,
                        ).length
                      }
                    </DescriptionItem>
                    <DescriptionItem label="Needs attention">
                      {
                        scopedNodes.filter(
                          (n) =>
                            !n.revoked_at &&
                            (!n.telemetry_fresh || !n.scrape_ok),
                        ).length
                      }
                    </DescriptionItem>
                  </DescriptionList>
                  {clusters.slice(0, 4).map((c) => (
                    <div className="cv-haproxy-cluster" key={c.id}>
                      {clusterOverview(c.id, "overview")}
                      <HAProxyIncidents
                        compact
                        clusterId={c.id}
                        client={client}
                        nodeNames={names(c.id)}
                        activityHref={href(["activity"])}
                        onInspectApplication={(id) =>
                          openApp(c.id, id, "traffic")
                        }
                      />
                    </div>
                  ))}
                  {!clusters.length && (
                    <EmptyState
                      title="Cluster not found"
                      description="Choose an available cluster."
                    />
                  )}
                </>
              )}
              {route.section === "applications" &&
                (route.kind === "application" ? (
                  appProps ? (
                    <HAProxyApplications
                      key={cluster!.id}
                      {...appProps}
                      applicationId={route.id}
                      tab={route.tab}
                    />
                  ) : (
                    <EmptyState
                      title="Cluster not found"
                      description="This application’s cluster is not in the current fleet."
                    />
                  )
                ) : (
                  <>
                    {appProps ? (
                      <HAProxyApplications key={cluster!.id} {...appProps} />
                    ) : (
                      <EmptyState
                        title="Cluster not found"
                        description="Choose an available cluster."
                      />
                    )}
                  </>
                ))}
              {route.section === "infrastructure" &&
                (route.kind === "node" ? (
                  node ? (
                    <>
                      <PageTabs
                        aria-label="Node sections"
                        currentId={route.tab}
                        items={nodeTabs.map((tab) => ({
                          id: tab,
                          label: tab[0]!.toUpperCase() + tab.slice(1),
                          href: href(["infrastructure", "nodes", node.id, tab]),
                        }))}
                      />
                      <HAProxyNodeDetail
                        key={node.id}
                        tab={route.tab}
                        node={node}
                        client={client}
                        cluster={{
                          id: node.cluster_id,
                          name:
                            fleet.clusters.find((c) => c.id === node.cluster_id)
                              ?.name ?? "Cluster",
                          entrypointNodeId: fleet.clusters.find(
                            (c) => c.id === node.cluster_id,
                          )?.topology?.control?.node_id,
                          nodeIds: activeIds(node.cluster_id),
                        }}
                        historyRange={range}
                        onHistoryRangeChange={(value) =>
                          setParam("range", value)
                        }
                        revokeReason={
                          node.revoked_at
                            ? "This node has already been revoked."
                            : changeReason
                        }
                      />
                    </>
                  ) : fleet.next_cursor ? (
                    <LoadingStatus delayMs={1000}>
                      Finding this node in the fleet…
                    </LoadingStatus>
                  ) : (
                    <EmptyState
                      title="Node not found"
                      description="The node is no longer in this fleet, or you do not have access."
                    />
                  )
                ) : route.kind === "cluster" ? (
                  cluster ? (
                    <>
                      {route.tab === "nodes" && nodeInventory}
                      {route.tab === "topology" &&
                        clusterOverview(cluster.id, "topology")}
                      {route.tab === "listeners" && (
                        <HAProxyListeners
                          cluster={cluster}
                          client={client}
                          nodeIds={activeIds(cluster.id)}
                          {...(changeReason
                            ? { permissionReason: changeReason }
                            : {})}
                        />
                      )}
                      {route.tab === "tls" && (
                        <HAProxyAutomaticTLS
                          dnsPermissionReason={dnsPermissionReason}
                          cluster={cluster}
                          client={client}
                          nodeNames={names(cluster.id)}
                          {...(changeReason
                            ? { permissionReason: changeReason }
                            : {})}
                        />
                      )}
                      {route.tab === "rollouts" && (
                        <HAProxyRollouts
                          clusterId={cluster.id}
                          client={client}
                        />
                      )}
                    </>
                  ) : (
                    <EmptyState
                      title="Cluster not found"
                      description="Choose an available cluster."
                    />
                  )
                ) : (
                  <>{nodeInventory}</>
                ))}
              {route.section === "activity" && (
                <>
                  <Select
                    label="Activity type"
                    value={params.get("activity") ?? "incidents"}
                    onChange={(value) => setParam("activity", String(value))}
                    options={[
                      { id: "incidents", label: "Incidents" },
                      { id: "deployments", label: "Deployments" },
                      { id: "maintenance", label: "Node maintenance" },
                    ]}
                  />
                  {clusters.map((c) => (
                    <div key={c.id} className="cv-haproxy-cluster">
                      {params.get("activity") === "deployments" ? (
                        <HAProxyRollouts clusterId={c.id} client={client} />
                      ) : params.get("activity") === "maintenance" ? (
                        clusterOverview(c.id, "activity")
                      ) : (
                        <HAProxyIncidents
                          clusterId={c.id}
                          client={client}
                          nodeNames={names(c.id)}
                          onInspectApplication={(id) =>
                            openApp(c.id, id, "traffic")
                          }
                        />
                      )}
                    </div>
                  ))}
                </>
              )}
            </>
          )}
          {route.section === "settings" && (
            <>
              {!installationSettings && (
                <AriaLink href={haproxyPath(basePath, ["settings"])}>
                  Settings
                </AriaLink>
              )}
              {(!installationSettings || settingsTab === "software") && (
                <Section
                  layout="stack"
                  title="Software inventory"
                  description="Versions reported by running processes. Host installers verify signed releases; the agent never replaces itself."
                >
                  <p>
                    Manager version:{" "}
                    {fleet.manager_build?.version ?? "Not reported"}
                  </p>
                  <PaginatedDataTable
                    aria-label="Agent software versions"
                    itemLabel="nodes"
                    rows={installationSettings ? fleet.nodes : scopedNodes}
                    rowLabel={(n) => n.name}
                    columns={[
                      {
                        id: "node",
                        header: "Node",
                        isRowHeader: true,
                        cell: (n) => (
                          <AriaLink
                            href={href([
                              "infrastructure",
                              "nodes",
                              n.id,
                              "runtime",
                            ])}
                          >
                            {n.name}
                          </AriaLink>
                        ),
                      },
                      {
                        id: "version",
                        header: "Agent version",
                        cell: (n) => n.agent?.version ?? "Not reported",
                      },
                      {
                        id: "haproxy",
                        header: "HAProxy version",
                        cell: (n) => n.haproxy_version || "Not reported",
                      },
                      {
                        id: "fresh",
                        header: "Observation",
                        cell: (n) =>
                          n.agent_fresh ? "Current" : "Last reported",
                      },
                    ]}
                    emptyState={
                      <EmptyState
                        title="No agent versions yet"
                        description="Enroll a node to see its reported software version."
                      />
                    }
                  />
                </Section>
              )}
              {(!installationSettings || settingsTab === "recovery") && (
                <Section
                  layout="stack"
                  title="Backups and recovery"
                  description="Back up management storage and each node’s identity, configuration and TLS keys. Manager backups cannot recover private node keys they never held."
                >
                  <p>
                    Installation upgrades and backups run on the host with the
                    signed installer. Keep recovery copies encrypted and
                    off-host.
                  </p>
                  <AriaLink
                    href="https://github.com/ecraela/fleet/blob/main/docs/self-hosted-installation.md#encrypted-and-authenticated-backup"
                    isExternal
                  >
                    Installation and recovery guide
                  </AriaLink>
                </Section>
              )}
            </>
          )}
          {fleet.next_cursor && (
            <Button
              isLoading={loading}
              onPress={() => void onLoadMore().catch(() => {})}
            >
              Load more fleet nodes
            </Button>
          )}
        </>
      )}
    </div>
  );
}
function NodeInventory({
  fleet,
  filter,
  query,
  page,
  onQuery,
  onPage,
  href,
  onOpen,
}: {
  fleet: HAProxyFleet;
  filter: string;
  query: string;
  page: number;
  onQuery: (q: string) => void;
  onPage: (p: number) => void;
  href: (id: string) => string;
  onOpen: (id: string) => void;
}) {
  const rows = fleet.nodes.filter(
    (n) =>
      n.cluster_id === filter &&
      n.name.toLowerCase().includes(query.toLowerCase()),
  );
  const current = Math.min(
    Math.max(1, page),
    Math.max(1, Math.ceil(rows.length / 10)),
  );
  return (
    <Section layout="stack" title="Nodes">
      <SearchField
        aria-label="Find nodes"
        placeholder="Find nodes…"
        value={query}
        onChange={onQuery}
      />
      <DataTable
        aria-label="HAProxy nodes"
        rows={rows.slice((current - 1) * 10, current * 10)}
        rowLabel={(n) => n.name}
        onRowAction={(id) => onOpen(String(id))}
        columns={[
          {
            id: "name",
            header: "Node",
            isRowHeader: true,
            cell: (n) => <AriaLink href={href(n.id)}>{n.name}</AriaLink>,
          },
          {
            id: "status",
            header: "Observations",
            cell: (n) => {
              const state = nodeStatus(n);
              return <Badge tone={state.tone}>{state.label}</Badge>;
            },
          },
          {
            id: "agent",
            header: "Agent version",
            cell: (n) => n.agent?.version ?? "Not reported",
          },
        ]}
        emptyState={
          <EmptyState
            title={query ? "No matching nodes" : "No enrolled nodes"}
            description="Enroll an agent beside an existing HAProxy instance."
          />
        }
      />
      <TablePagination
        page={current}
        pageSize={10}
        totalItems={rows.length}
        itemLabel="nodes"
        onPageChange={onPage}
      />
    </Section>
  );
}
