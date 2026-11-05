import { useEffect, useState, type ReactNode } from "react";
import {
  EmptyState,
  MetricCard,
  MetricGrid,
  PaginatedDataTable,
  DescriptionItem,
  DescriptionList,
  InlineMeta,
  InlineTip,
  Section,
} from "@ecraela/ui";
import {
  type HAProxyClient,
  type HAProxyNode,
  type HAProxySnapshot,
  type HAProxyHistory,
  type HAProxyHistoryRange,
} from "./haproxy-api";
import {
  backendLatencies,
  counterRate,
  currentConnections,
  formatBandwidth,
  formatMetric,
  formatBits,
  liveObservationStatus,
} from "./haproxy-metrics";
import {
  HAProxyHistoryCharts,
  latestHistoryValue,
  useHAProxyHistory,
} from "./haproxy-history";
import {
  HAProxyConfiguration,
  type HAProxyConfigurationScope,
} from "./haproxy-configuration";
import { HAProxyMaintenancePanel } from "./haproxy-maintenance";
import { HAProxyRuntime } from "./haproxy-runtime";
import type { NodeTab } from "./haproxy-navigation";
function observedTime(value: string | null) {
  return value && Number.isFinite(Date.parse(value))
    ? new Intl.DateTimeFormat(undefined, {
        dateStyle: "medium",
        timeStyle: "medium",
      }).format(new Date(value))
    : "Not yet observed";
}
export function HAProxyNodeDetail({
  node,
  tab,
  cluster,
  client,
  revokeReason,
  historyRange,
  onHistoryRangeChange,
}: {
  node: HAProxyNode;
  tab: NodeTab;
  cluster: HAProxyConfigurationScope;
  client: HAProxyClient;
  revokeReason: string | undefined;
  historyRange: HAProxyHistoryRange;
  onHistoryRangeChange: (range: HAProxyHistoryRange) => void;
}) {
  const [samples, setSamples] = useState(() =>
    client.cachedObservations(node.id),
  );
  const [available, setAvailable] = useState(true);
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    if (node.revoked_at) return;
    const controller = new AbortController();
    const accept = (snapshot: HAProxySnapshot) => {
      if (controller.signal.aborted) return;
      setAvailable(true);
      if (snapshot.node_id !== node.id) return;
      setSamples(client.cachedObservations(node.id));
    };
    if (!client.cachedObservations(node.id).current)
      void client
        .snapshot(node.id, controller.signal)
        .then(accept)
        .catch(() => {
          if (!controller.signal.aborted) setAvailable(false);
        });
    void client.stream(node.id, controller.signal, accept, () => {
      if (!controller.signal.aborted) setAvailable(false);
    });
    const timer = setInterval(() => setNow(Date.now()), 5000);
    return () => {
      controller.abort();
      clearInterval(timer);
    };
  }, [client, node.id, node.revoked_at]);
  const current = samples.current;
  const status = liveObservationStatus(current, available, now);
  const snapshot = current;
  return (
    <Section
      layout="stack"
      title={
        {
          metrics: "Live metrics",
          configuration: "Configuration",
          runtime: "Runtime",
          maintenance: "Maintenance",
        }[tab]
      }
    >
      <InlineMeta
        items={[
          [
            "Observation",
            observedTime(current?.observed_at ?? node.observed_at),
          ],
          ["Status", node.revoked_at ? "Revoked" : status.label],
        ]}
      />
      {!node.revoked_at && status.reconnecting && (
        <InlineTip
          variant="warning"
          label="Reconnecting to live observations"
          role="status"
        >
          The connection will retry automatically. Last received observations
          remain visible with their collection time.
        </InlineTip>
      )}
      {!node.revoked_at && current && !status.fresh && !status.reconnecting && (
        <InlineTip
          variant="warning"
          title="Showing last received metrics"
          role="status"
        >
          New observations have not arrived yet. Values below retain their
          collection time and will update automatically.
        </InlineTip>
      )}
      {node.revoked_at ? (
        <EmptyState
          size="sm"
          title="Node access revoked"
          description="This identity can no longer send telemetry or connect to the manager."
        />
      ) : (
        <>
          {tab === "maintenance" && (
            <HAProxyMaintenancePanel
              nodeId={node.id}
              nodeName={node.name}
              client={client}
              {...(revokeReason ? { permissionReason: revokeReason } : {})}
            />
          )}
          {tab === "configuration" && (
            <HAProxyConfiguration
              cluster={cluster}
              nodeId={node.id}
              client={client}
              {...(revokeReason ? { permissionReason: revokeReason } : {})}
            />
          )}
          {tab === "runtime" && (
            <HAProxyRuntime
              nodeId={node.id}
              client={client}
              {...(revokeReason ? { permissionReason: revokeReason } : {})}
            />
          )}
          {tab === "metrics" && (
            <HAProxyNodeObservations
              nodeId={node.id}
              client={client}
              snapshot={snapshot}
              previous={samples.previous}
              range={historyRange}
              onRangeChange={onHistoryRangeChange}
            />
          )}
        </>
      )}
    </Section>
  );
}

function HAProxyNodeObservations({
  nodeId,
  client,
  snapshot,
  previous,
  range,
  onRangeChange,
}: {
  nodeId: string;
  client: HAProxyClient;
  snapshot: HAProxySnapshot | null;
  previous: HAProxySnapshot | null;
  range: HAProxyHistoryRange;
  onRangeChange: (range: HAProxyHistoryRange) => void;
}) {
  const history = useHAProxyHistory(client, nodeId, range);
  const labels = (applications: readonly { id: string; name: string }[]) =>
    Object.fromEntries(
      applications.map((app) => [
        `cv_app_${app.id.replaceAll("-", "")}`,
        app.name,
      ]),
    );
  const [backendLabels, setBackendLabels] = useState(() =>
    labels(
      client.cachedConfiguration(nodeId)?.control.discovery?.configuration
        ?.document?.applications ?? [],
    ),
  );
  useEffect(() => {
    const controller = new AbortController();
    void client
      .configuration(nodeId, controller.signal)
      .then((view) => {
        if (!controller.signal.aborted)
          setBackendLabels(
            labels(
              view.control.discovery?.configuration?.document?.applications ??
                [],
            ),
          );
      })
      .catch(() => {});
    return () => controller.abort();
  }, [client, nodeId]);
  return (
    <HAProxyObservation
      snapshot={snapshot}
      previous={previous}
      history={history.data}
    >
      <HAProxyHistoryCharts
        history={history.data}
        backendLabels={backendLabels}
        range={range}
        loading={history.loading}
        error={history.error}
        onRangeChange={onRangeChange}
      />
    </HAProxyObservation>
  );
}

export function HAProxyObservation({
  snapshot,
  previous,
  history,
  children,
}: {
  snapshot: HAProxySnapshot | null;
  previous: HAProxySnapshot | null;
  history?: HAProxyHistory | null;
  children?: ReactNode;
}) {
  const rates = ["1xx", "2xx", "3xx", "4xx", "5xx", "other"].map((code) => ({
    id: code,
    rate: counterRate(
      snapshot,
      previous,
      "haproxy_frontend_http_responses_total",
      ["code", code],
    ),
  }));
  const latencies = backendLatencies(snapshot);
  const bandwidth = (name: string, metric: string) => {
    const rate = counterRate(snapshot, previous, metric);
    const stored =
      snapshot?.fresh && snapshot.scrape_ok
        ? latestHistoryValue(history, name)
        : null;
    return rate !== null
      ? formatBandwidth(rate)
      : stored !== null
        ? formatBits(stored)
        : formatBandwidth(null);
  };
  return (
    <div className="cv-haproxy-observation">
      <MetricGrid columns={3}>
        <MetricCard
          label="Traffic in"
          value={bandwidth("traffic_in", "haproxy_frontend_bytes_in_total")}
          description="Latest received observation"
        />
        <MetricCard
          label="Traffic out"
          value={bandwidth("traffic_out", "haproxy_frontend_bytes_out_total")}
          description="Latest received observation"
          color="teal"
        />
        <MetricCard
          label="Active connections"
          value={
            currentConnections(snapshot) === null
              ? null
              : formatMetric(currentConnections(snapshot))
          }
          description="Latest received observation"
          color="violet"
        />
      </MetricGrid>
      {children ?? (
        <Section
          layout="stack"
          size="sm"
          title="HTTP responses"
          description="Response classes per second across HAProxy frontends. Rates require two observations."
        >
          <DescriptionList columns={3}>
            {rates.map((row) => (
              <DescriptionItem key={row.id} label={row.id}>
                {formatMetric(row.rate, "responses/s")}
              </DescriptionItem>
            ))}
          </DescriptionList>
        </Section>
      )}
      <Section
        layout="stack"
        size="sm"
        title="Backend latency averages"
        description="HAProxy-reported rolling averages, in milliseconds. These values are not p95 or p99 percentiles."
      >
        <PaginatedDataTable
          aria-label="Backend latency averages"
          itemLabel="backends"
          rows={latencies}
          columns={[
            {
              id: "name",
              header: "Backend",
              isRowHeader: true,
              cell: (row) => row.id,
            },
            ...(["queue", "connect", "response", "total"] as const).map(
              (field) => ({
                id: field,
                header: `${field[0]!.toUpperCase()}${field.slice(1)}`,
                align: "end" as const,
                cell: (row: (typeof latencies)[number]) =>
                  formatMetric(
                    row[field] === null ? null : row[field]! * 1000,
                    "ms",
                  ),
              }),
            ),
          ]}
          emptyState={
            <EmptyState
              size="sm"
              title="No latency observations"
              description="Latency averages appear when this node exports compatible backend timing metrics."
            />
          }
        />
      </Section>
    </div>
  );
}
