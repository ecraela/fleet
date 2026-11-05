import { useEffect, useState } from "react";
import {
  Badge,
  DataTable,
  EmptyState,
  InlineTip,
  Link,
  LoadingStatus,
  SearchField,
  Section,
  Select,
  Sparkline,
  TablePagination,
  Toolbar,
  type MetricHistory,
} from "@ecraela/ui";
import type {
  HAProxyClient,
  HAProxyCluster,
  HAProxyFleet,
  HAProxyHistory,
  HAProxyNode,
} from "./haproxy-api";
import { clusterMetricSeries } from "./haproxy-cluster";
import { formatBits, formatMetric } from "./haproxy-metrics";

function reporting(node: HAProxyNode, now = Date.now()) {
  const age = node.observed_at ? now - Date.parse(node.observed_at) : Infinity;
  return (
    node.telemetry_fresh &&
    node.scrape_ok &&
    Number.isFinite(age) &&
    age >= -60_000 &&
    // Allow the 30-second inventory interval plus one request budget.
    age <= 45_000
  );
}

export function clusterHealth(nodes: HAProxyNode[], incomplete: boolean) {
  const active = nodes.filter((node) => !node.revoked_at);
  if (active.some((node) => !reporting(node)))
    return {
      id: "attention",
      label: "Needs attention",
      tone: "warning",
    } as const;
  if (incomplete)
    return {
      id: "incomplete",
      label: "Incomplete observations",
      tone: "warning",
    } as const;
  if (!active.length)
    return {
      id: "empty",
      label: "No enrolled nodes",
      tone: "neutral",
    } as const;
  return { id: "reporting", label: "Reporting", tone: "success" } as const;
}

/** Table history is bounded by the visible page and the existing per-cluster limit. */
export function clusterHistoryNodes(
  clusters: HAProxyCluster[],
  nodes: HAProxyNode[],
) {
  return clusters.flatMap((cluster) =>
    nodes
      .filter((node) => node.cluster_id === cluster.id && !node.revoked_at)
      .slice(0, 12),
  );
}

export async function loadClusterHistories(
  client: Pick<HAProxyClient, "history">,
  ids: string[],
  signal: AbortSignal,
) {
  const histories: Record<string, HAProxyHistory> = {};
  let failed = false;
  for (let index = 0; index < ids.length && !signal.aborted; index += 4) {
    await Promise.all(
      ids.slice(index, index + 4).map(async (id) => {
        try {
          const data = await client.history(id, "15m", signal);
          if (!signal.aborted) histories[id] = data;
        } catch {
          if (!signal.aborted) failed = true;
        }
      }),
    );
  }
  return { histories, failed };
}

export function HAProxyClusters({
  fleet,
  client,
  href,
  onOpen,
  query,
  onQuery,
  health,
  onHealth,
  page,
  onPage,
}: {
  fleet: HAProxyFleet;
  client: HAProxyClient;
  href: (id: string) => string;
  onOpen: (id: string) => void;
  query: string;
  onQuery: (value: string) => void;
  health: string;
  onHealth: (value: string) => void;
  page: number;
  onPage: (value: number) => void;
}) {
  const incomplete = Boolean(fleet.next_cursor || fleet.partial);
  const nodesFor = (id: string) =>
    fleet.nodes.filter((node) => node.cluster_id === id && !node.revoked_at);
  const rows = fleet.clusters.filter(
    (cluster) =>
      `${cluster.name} ${cluster.topology?.entrypoint ?? ""}`
        .toLowerCase()
        .includes(query.trim().toLowerCase()) &&
      (health === "all" ||
        clusterHealth(nodesFor(cluster.id), incomplete).id === health),
  );
  const currentPage = Math.min(
    Math.max(1, page),
    Math.max(1, Math.ceil(rows.length / 10)),
  );
  const visible = rows.slice((currentPage - 1) * 10, currentPage * 10);
  const monitored = clusterHistoryNodes(visible, fleet.nodes);
  const ids = monitored.map((node) => node.id).join(",");
  const [state, setState] = useState<{
    client: HAProxyClient;
    ids: string;
    histories: Record<string, HAProxyHistory>;
    failed: boolean;
    loading: boolean;
  }>({ client, ids: "", histories: {}, failed: false, loading: false });
  const histories = Object.fromEntries(
    monitored.flatMap((node) => {
      const data =
        client.cachedHistory(node.id, "15m")?.data ??
        (state.client === client ? state.histories[node.id] : undefined);
      return data ? [[node.id, data]] : [];
    }),
  );
  useEffect(() => {
    const controller = new AbortController();
    const nodeIds = ids ? ids.split(",") : [];
    let pending = false;
    const update = async () => {
      if (
        pending ||
        controller.signal.aborted ||
        document.visibilityState === "hidden"
      )
        return;
      pending = true;
      setState((previous) => ({
        client,
        ids,
        histories:
          previous.client === client
            ? Object.fromEntries(
                nodeIds.flatMap((id) =>
                  previous.histories[id] ? [[id, previous.histories[id]]] : [],
                ),
              )
            : {},
        failed: false,
        loading: true,
      }));
      const result = await loadClusterHistories(
        client,
        nodeIds,
        controller.signal,
      );
      if (!controller.signal.aborted)
        setState((previous) => ({
          client,
          ids,
          histories: { ...previous.histories, ...result.histories },
          failed: result.failed,
          loading: false,
        }));
      pending = false;
    };
    const resume = () => {
      if (document.visibilityState !== "hidden") void update();
    };
    void update();
    const timer = setInterval(() => void update(), 30_000);
    document.addEventListener("visibilitychange", resume);
    return () => {
      controller.abort();
      clearInterval(timer);
      document.removeEventListener("visibilitychange", resume);
    };
  }, [client, ids]);
  const now = Math.floor(Date.now() / 1000);
  const traffic = (cluster: HAProxyCluster, metric: string, label: string) => {
    const nodes = monitored.filter((node) => node.cluster_id === cluster.id);
    const values = clusterMetricSeries(nodes, histories, metric).filter(
      ([at]) => at >= now - 900 && at <= now + 60,
    );
    const latest = values.at(-1);
    const current = latest && now - latest[0] <= 60 ? latest[1] : null;
    const limited = nodes.length < nodesFor(cluster.id).length || incomplete;
    const history: MetricHistory = {
      start: now - 900,
      end: now,
      step: Math.max(
        15,
        ...nodes.map((node) => histories[node.id]?.step_seconds ?? 15),
      ),
      values,
      label: `${cluster.name}: ${label} over the last 15 minutes${limited ? ", partial node coverage" : ""}`,
    };
    return (
      <Sparkline
        compact
        history={history}
        color={metric === "traffic_out" ? "teal" : "blue"}
        summary={
          current === null
            ? "Not observed"
            : metric === "traffic_out"
              ? formatBits(current)
              : formatMetric(current, "responses/s")
        }
        description={
          limited
            ? `15m history – ${nodes.length} loaded nodes, partial`
            : undefined
        }
      />
    );
  };
  return (
    <Section
      layout="stack"
      title="Clusters"
      description="Choose a cluster to view its applications, nodes and activity. Charts show recorded traffic from enrolled nodes."
    >
      <Toolbar aria-label="Cluster filters">
        <SearchField label="Find clusters" value={query} onChange={onQuery} />
        <Select
          label="Health"
          value={health}
          onChange={(value) => onHealth(String(value ?? "all"))}
          options={[
            { id: "all", label: "All statuses" },
            { id: "reporting", label: "Reporting" },
            { id: "attention", label: "Needs attention" },
            { id: "incomplete", label: "Incomplete observations" },
            { id: "empty", label: "No enrolled nodes" },
          ]}
        />
      </Toolbar>
      {fleet.next_cursor && (
        <InlineTip variant="warning">
          Node counts cover the loaded inventory. Load the remaining fleet nodes
          for complete cluster health.
        </InlineTip>
      )}
      {state.ids === ids && state.failed && (
        <InlineTip variant="warning">
          Some cluster charts could not refresh. Previously recorded traffic
          remains visible; gaps are not zero traffic.
        </InlineTip>
      )}
      {state.ids === ids &&
        state.loading &&
        !Object.keys(histories).length &&
        !!ids && (
          <LoadingStatus delayMs={1000}>Loading cluster traffic…</LoadingStatus>
        )}
      <DataTable
        aria-label="HAProxy clusters"
        density="compact"
        rowHighlight="none"
        rows={visible}
        rowLabel={(cluster) => cluster.name}
        onRowAction={(id) => onOpen(String(id))}
        columns={[
          {
            id: "name",
            header: "Cluster",
            isRowHeader: true,
            cell: (cluster) => (
              <Link href={href(cluster.id)}>{cluster.name}</Link>
            ),
          },
          {
            id: "health",
            header: "Health",
            cell: (cluster) => {
              const status = clusterHealth(nodesFor(cluster.id), incomplete);
              return <Badge tone={status.tone}>{status.label}</Badge>;
            },
          },
          {
            id: "nodes",
            header: "Reporting nodes",
            cell: (cluster) => {
              const nodes = nodesFor(cluster.id);
              return `${nodes.filter((node) => reporting(node)).length} / ${nodes.length}${fleet.next_cursor ? " loaded" : ""}`;
            },
          },
          {
            id: "traffic",
            header: "Traffic out (15m)",
            cell: (cluster) => traffic(cluster, "traffic_out", "Traffic out"),
          },
          {
            id: "responses",
            header: "HTTP responses (15m)",
            cell: (cluster) =>
              traffic(cluster, "http_responses", "HTTP responses"),
          },
          {
            id: "entrypoint",
            header: "Entrypoint",
            cell: (cluster) => cluster.topology?.entrypoint ?? "Not declared",
          },
        ]}
        emptyState={
          <EmptyState
            title="No matching clusters"
            description="Try a different name or health filter."
          />
        }
      />
      {!!rows.length && (
        <TablePagination
          aria-label="Cluster pagination"
          page={currentPage}
          pageSize={10}
          totalItems={rows.length}
          itemLabel="clusters"
          onPageChange={onPage}
        />
      )}
    </Section>
  );
}
