import { ConnectionIssue } from "./connection-health";
import { useEffect, useState } from "react";
import {
  Badge,
  MetricCard,
  MetricGrid,
  type MetricHistory,
  DataTable,
  InlineTip,
  Section,
  SegmentedControl,
  TimeSeriesChart,
  TablePagination,
  TrafficTopology,
  type TimeSeries,
} from "@ecraela/ui";
import {
  HAProxyClient,
  type HAProxyCluster,
  type HAProxyMaintenance,
  type HAProxyNode,
  type HAProxyHistory,
  type HAProxyHistoryRange,
  type HAProxyRollout,
} from "./haproxy-api";
import { formatBits, formatMetric, nodeStatus } from "./haproxy-metrics";
import { latestHistoryValue } from "./haproxy-history";
import { liveHistory, type HAProxyClusterLive } from "./haproxy-live";
import { maintenanceStatus, maintenanceTone } from "./haproxy-maintenance";

type Histories = Record<string, HAProxyHistory>;

export function rolloutMaintenances(
  rollouts: HAProxyRollout[],
): HAProxyMaintenance[] {
  return rollouts.flatMap((rollout) =>
    rollout.nodes.flatMap((node) =>
      [node.maintenance, node.rollback_maintenance].filter(
        (item): item is HAProxyMaintenance => !!item,
      ),
    ),
  );
}

export function clusterMaintenanceHistory(
  standalone: HAProxyMaintenance[],
  rollouts: HAProxyRollout[],
): HAProxyMaintenance[] {
  // After handoff the standalone record owns subsequent recovery progress.
  const unique = new Map(
    rolloutMaintenances(rollouts).map((item) => [item.id, item]),
  );
  for (const item of standalone) unique.set(item.id, item);
  return [...unique.values()]
    .sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at))
    .slice(0, 20);
}

// Keep each proxy's series separate: adding them together would hide a failed
// node, and missing observations must never become zero traffic.
export function nodeTrafficSeries(
  nodes: HAProxyNode[],
  histories: Histories,
  metric: string,
): TimeSeries[] {
  return nodes.map((node, i) => {
    const lines =
      histories[node.id]?.series.filter(
        (line) => line.metric.series === metric,
      ) ?? [];
    const points = new Map<number, number>();
    for (const line of lines)
      for (const [at, value] of line.values)
        points.set(at, (points.get(at) ?? 0) + value);
    return {
      id: node.id,
      label: node.name,
      color: (["blue", "teal", "violet", "amber", "rose", "cyan"] as const)[
        i % 6
      ]!,
      values: [...points].sort(([a], [b]) => a - b),
    };
  });
}

// A cluster sparkline only includes buckets observed on every displayed node.
export function clusterMetricSeries(
  nodes: HAProxyNode[],
  histories: Histories,
  metric: string,
): Array<readonly [number, number]> {
  const lines = nodeTrafficSeries(nodes, histories, metric).map(
    (line) => new Map(line.values),
  );
  if (!lines.length) return [];
  return [...lines[0]!.keys()]
    .filter((at) => lines.every((line) => line.has(at)))
    .map(
      (at) =>
        [at, lines.reduce((total, line) => total + line.get(at)!, 0)] as const,
    )
    .sort((a, b) => a[0] - b[0]);
}

export function clusterTotals(
  nodes: HAProxyNode[],
  histories: Histories,
  kind: string,
): { value: number | null; reporting: number } {
  const values = nodes
    .filter((n) => n.telemetry_fresh && n.scrape_ok && !n.revoked_at)
    .map((n) => latestHistoryValue(histories[n.id], kind))
    .filter((n): n is number => n !== null);
  return {
    value: values.length ? values.reduce((a, b) => a + b, 0) : null,
    reporting: values.length,
  };
}

export function HAProxyClusterOverview({
  cluster,
  nodes,
  client,
  selectedId,
  onSelect,
  range,
  onRangeChange,
  view = "overview",
}: {
  view?: "overview" | "topology" | "activity";
  cluster: HAProxyCluster;
  nodes: HAProxyNode[];
  client: HAProxyClient;
  selectedId: string | null;
  onSelect: (id: string) => void;
  range: HAProxyHistoryRange;
  onRangeChange: (range: HAProxyHistoryRange) => void;
}) {
  const [maintenance, setMaintenance] = useState<HAProxyMaintenance[]>(() =>
    clusterMaintenanceHistory(
      client.cachedClusterMaintenance(cluster.id) ?? [],
      client.cachedRollouts(cluster.id) ?? [],
    ),
  );
  const [maintenanceError, setMaintenanceError] = useState(false);
  const [maintenancePage, setMaintenancePage] = useState(1);
  const visibleMaintenancePage = Math.min(
    maintenancePage,
    Math.max(1, Math.ceil(maintenance.length / 10)),
  );
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
        const [rows, rollouts] = await Promise.all([
          client.maintenanceHistory(cluster.id, controller.signal),
          client.rollouts(cluster.id, controller.signal),
        ]);
        if (!controller.signal.aborted) {
          setMaintenance(clusterMaintenanceHistory(rows, rollouts));
          setMaintenanceError(false);
        }
      } catch {
        if (!controller.signal.aborted) setMaintenanceError(true);
      } finally {
        pending = false;
      }
    };
    const resume = () => void update();
    void update();
    const timer = setInterval(resume, 1000);
    document.addEventListener("visibilitychange", resume);
    return () => {
      controller.abort();
      clearInterval(timer);
      document.removeEventListener("visibilitychange", resume);
    };
  }, [client, cluster.id]);
  const heldMaintenance = maintenance.filter(
    (m) => m.status !== "completed" && m.status !== "failed",
  );
  const [observation, setObservation] = useState<{
    key: string;
    frame: HAProxyClusterLive | null;
    failed: boolean;
  }>({ key: "", frame: null, failed: false });
  const active = nodes.filter((n) => !n.revoked_at);
  const monitored = active.slice(0, 12);
  const ids = monitored.map((n) => n.id).join(",");
  const streamKey = `${cluster.id}/${ids}`;
  const liveNodes =
    observation.key === streamKey ? (observation.frame?.nodes ?? []) : [];
  const observed = new Map(liveNodes.map((node) => [node.node_id, node]));
  const currentNodes = nodes.map((node) => {
    const latest = observed.get(node.id);
    if (
      !latest ||
      (node.observed_at &&
        latest.observed_at &&
        Date.parse(node.observed_at) > Date.parse(latest.observed_at))
    )
      return node;
    const age = latest.observed_at
      ? Date.now() - Date.parse(latest.observed_at)
      : Infinity;
    return {
      ...node,
      observed_at: latest.observed_at,
      telemetry_fresh: latest.fresh && age >= -60_000 && age <= 15_000,
      scrape_ok: latest.scrape_ok,
      metrics_available: latest.observed_at !== null && !latest.revoked,
    };
  });
  const currentActive = currentNodes.filter((node) => !node.revoked_at);
  const currentMonitored = currentActive.slice(0, 12);
  const [state, setState] = useState<{
    key: string;
    histories: Histories;
    failed: boolean;
  }>({ key: "", histories: {}, failed: false });
  const key = `${streamKey}/${range}`;
  const histories: Histories = Object.fromEntries(
    monitored.flatMap((node) => {
      const data =
        client.cachedHistory(node.id, range)?.data ??
        (state.key === key ? state.histories[node.id] : undefined);
      return data ? [[node.id, data]] : [];
    }),
  );
  const live: Histories = Object.fromEntries(
    liveNodes.map((node) => [node.node_id, liveHistory(node)]),
  );
  useEffect(() => {
    if (!ids || view === "activity") return;
    const controller = new AbortController();
    setObservation({ key: streamKey, frame: null, failed: false });
    void client.clusterStream(
      cluster.id,
      ids.split(","),
      controller.signal,
      (frame) => {
        if (!controller.signal.aborted)
          setObservation({ key: streamKey, frame, failed: false });
      },
      () => {
        if (!controller.signal.aborted)
          setObservation((current) => ({ ...current, failed: true }));
      },
    );
    return () => controller.abort();
  }, [client, cluster.id, ids, streamKey, view]);
  useEffect(() => {
    if (view !== "overview") return;
    const controller = new AbortController();
    let pending = false;
    const nodeIds = ids ? ids.split(",") : [];
    setState({ key, histories: {}, failed: false });
    const update = async () => {
      if (
        pending ||
        controller.signal.aborted ||
        globalThis.document?.visibilityState === "hidden"
      )
        return;
      pending = true;
      const results: Histories = {};
      let failed = false;
      for (let i = 0; i < nodeIds.length; i += 4) {
        await Promise.all(
          nodeIds.slice(i, i + 4).map(async (id) => {
            try {
              results[id] = await client.history(id, range, controller.signal);
            } catch {
              if (!controller.signal.aborted) failed = true;
            }
          }),
        );
        if (controller.signal.aborted) break;
      }
      if (!controller.signal.aborted)
        setState((current) => ({
          key,
          histories: {
            ...(current.key === key ? current.histories : {}),
            ...results,
          },
          failed,
        }));
      pending = false;
    };
    const resume = () => {
      if (globalThis.document?.visibilityState !== "hidden") void update();
    };
    void update();
    const timer = setInterval(() => void update(), 30_000);
    document.addEventListener("visibilitychange", resume);
    return () => {
      controller.abort();
      clearInterval(timer);
      document.removeEventListener("visibilitychange", resume);
    };
  }, [client, ids, range, key, view]);
  const reporting = currentActive.filter(
    (n) => n.telemetry_fresh && n.scrape_ok,
  ).length;
  const incoming = clusterTotals(currentMonitored, live, "traffic_in");
  const outgoing = clusterTotals(currentMonitored, live, "traffic_out");
  const connections = clusterTotals(currentMonitored, live, "connections");
  const history = Object.values(histories)[0];
  const end = history?.end ?? Math.floor(Date.now() / 1000);
  const start = history?.start ?? end - 3600;
  const partial =
    active.length > monitored.length || outgoing.reporting < monitored.length;
  const metricHistory = (
    metric: string,
    label: string,
  ): MetricHistory | undefined =>
    view === "overview"
      ? {
          start,
          end,
          step: history?.step_seconds ?? 15,
          values: clusterMetricSeries(monitored, histories, metric),
          label,
        }
      : undefined;
  const topology = cluster.topology;
  const byId = new Map(currentNodes.map((n) => [n.id, n]));
  return (
    <Section
      layout="stack"
      title={view === "topology" ? "Traffic topology" : cluster.name}
      description="Observe traffic and reporting status across this cluster’s HAProxy nodes."
      actions={
        <Badge
          tone={
            active.length > 0 && reporting === active.length
              ? "success"
              : "warning"
          }
        >
          {reporting}/{active.length} reporting
        </Badge>
      }
    >
      <div className="cv-haproxy-cluster">
        {view !== "activity" && (
          <>
            <MetricGrid>
              <MetricCard
                label="HAProxy nodes"
                value={active.length}
                description={`${reporting}/${active.length} reporting`}
              />
              <MetricCard
                label={partial ? "Observed traffic in" : "Traffic in"}
                value={
                  incoming.value === null ? null : formatBits(incoming.value)
                }
                description="Live rate across reporting nodes"
                history={metricHistory(
                  "traffic_in",
                  "Recorded incoming bandwidth across all displayed nodes",
                )}
              />
              <MetricCard
                label={partial ? "Observed traffic out" : "Traffic out"}
                value={
                  outgoing.value === null ? null : formatBits(outgoing.value)
                }
                description="Live rate across reporting nodes"
                color="teal"
                history={metricHistory(
                  "traffic_out",
                  "Recorded outgoing bandwidth across all displayed nodes",
                )}
              />
              <MetricCard
                label="Observed connections"
                value={
                  connections.value === null
                    ? null
                    : formatMetric(connections.value)
                }
                description="Across reporting nodes"
                color="violet"
                history={metricHistory(
                  "connections",
                  "Recorded connections across all displayed nodes",
                )}
              />
            </MetricGrid>
          </>
        )}
        {view === "topology" && (
          <>
            {topology ? (
              <TrafficTopology
                entrypoint={topology.entrypoint}
                strategy={
                  topology.strategy === "roundrobin"
                    ? "Round robin"
                    : topology.strategy === "leastconn"
                      ? "Least connections"
                      : "Active / passive"
                }
                backend={topology.backend}
                selectedId={selectedId}
                onSelect={onSelect}
                nodes={topology.nodes.map((n) => {
                  const node = byId.get(n.node_id);
                  const status = node
                    ? nodeStatus(node)
                    : { label: "Not loaded", tone: "neutral" as const };
                  const out =
                    node &&
                    node.telemetry_fresh &&
                    node.scrape_ok &&
                    !node.revoked_at
                      ? latestHistoryValue(live[node.id], "traffic_out")
                      : null;
                  return {
                    id: n.node_id,
                    label: node?.name ?? "HAProxy node",
                    address: n.address,
                    status: heldMaintenance.some((m) => m.node_id === n.node_id)
                      ? maintenanceStatus(
                          heldMaintenance.find((m) => m.node_id === n.node_id)!
                            .status,
                        )
                      : status.label,
                    tone: heldMaintenance.some((m) => m.node_id === n.node_id)
                      ? maintenanceTone(
                          heldMaintenance.find((m) => m.node_id === n.node_id)!
                            .status,
                        )
                      : status.tone,
                    description:
                      out === null
                        ? "Traffic observation unavailable"
                        : `${formatBits(out)} out`,
                  };
                })}
              />
            ) : (
              <InlineTip title="Traffic topology not declared">
                Enrolled nodes report independently. Configure a shared
                entrypoint and declare its routing through the manager API to
                show the traffic path here.
              </InlineTip>
            )}
          </>
        )}
        {view === "activity" && (
          <>
            {maintenanceError && (
              <ConnectionIssue message="Cluster observations: refresh interrupted; retrying automatically." />
            )}
            {maintenance.length > 0 && (
              <Section
                layout="stack"
                size="sm"
                title="Node maintenance history"
                description="Select a node to follow its maintenance or rejoin traffic."
              >
                <DataTable
                  aria-label="HAProxy node maintenance history"
                  rows={maintenance.slice(
                    (visibleMaintenancePage - 1) * 10,
                    visibleMaintenancePage * 10,
                  )}
                  rowLabel={(row) =>
                    byId.get(row.node_id)?.name ?? "HAProxy node"
                  }
                  columns={[
                    {
                      id: "node",
                      header: "Node",
                      isRowHeader: true,
                      cell: (row) =>
                        byId.get(row.node_id)?.name ?? "HAProxy node",
                    },
                    {
                      id: "state",
                      header: "Traffic state",
                      cell: (row) => (
                        <Badge tone={maintenanceTone(row.status)}>
                          {maintenanceStatus(row.status)}
                        </Badge>
                      ),
                    },
                    {
                      id: "connections",
                      header: "Traffic connections",
                      cell: (row) => row.connections ?? "Unknown",
                    },
                    {
                      id: "updated",
                      header: "Updated",
                      cell: (row) => new Date(row.updated_at).toLocaleString(),
                    },
                  ]}
                />
                <TablePagination
                  page={visibleMaintenancePage}
                  pageSize={10}
                  totalItems={maintenance.length}
                  itemLabel="maintenance entries"
                  onPageChange={setMaintenancePage}
                  previousLabel="Previous"
                  nextLabel="Next"
                />
              </Section>
            )}
            {!maintenance.length && (
              <InlineTip>
                No node maintenance has been recorded for this cluster.
              </InlineTip>
            )}
          </>
        )}
        {view === "overview" && (
          <>
            {active.length > 12 && (
              <InlineTip title="Traffic overview limited">
                Charts show the first 12 loaded nodes. Select a node to inspect
                its own history.
              </InlineTip>
            )}
            {observation.key === streamKey &&
              observation.failed &&
              liveNodes.every(
                (node) =>
                  !node.observed_at ||
                  Date.now() - Date.parse(node.observed_at) > 5000,
              ) && (
                <ConnectionIssue message="Cluster observations: refresh interrupted; retrying automatically." />
              )}
            {state.key === key && state.failed && (
              <ConnectionIssue message="Cluster observations: refresh interrupted; retrying automatically." />
            )}
            <Section
              layout="stack"
              size="sm"
              title="Cluster traffic"
              description={
                topology
                  ? `Traffic for frontend ${topology.frontend}. Other frontends are excluded. Missing telemetry leaves gaps.`
                  : "Traffic across each node's HAProxy frontends. Missing telemetry leaves gaps."
              }
              actions={
                <SegmentedControl
                  aria-label="Cluster traffic time range"
                  size="sm"
                  selectedKey={range}
                  onSelectionChange={(value) =>
                    onRangeChange(String(value) as HAProxyHistoryRange)
                  }
                  options={[
                    { id: "15m", label: "15m" },
                    { id: "1h", label: "1h" },
                    { id: "6h", label: "6h" },
                    { id: "24h", label: "24h" },
                  ]}
                />
              }
            >
              <div className="cv-haproxy-charts">
                <TimeSeriesChart
                  variant="panel"
                  title="Traffic out by node"
                  unit="bit/s"
                  start={start}
                  end={end}
                  step={history?.step_seconds ?? 15}
                  series={nodeTrafficSeries(
                    monitored,
                    histories,
                    "traffic_out",
                  )}
                  formatValue={formatBits}
                  emptyMessage="Waiting for stored traffic observations"
                />
                <TimeSeriesChart
                  variant="panel"
                  title="HTTP responses by node"
                  unit="responses/s"
                  start={start}
                  end={end}
                  step={history?.step_seconds ?? 15}
                  series={nodeTrafficSeries(
                    monitored,
                    histories,
                    "http_responses",
                  )}
                  formatValue={(value) => formatMetric(value, "responses/s")}
                  emptyMessage="Waiting for stored response observations"
                />
              </div>
            </Section>
          </>
        )}
      </div>
    </Section>
  );
}
