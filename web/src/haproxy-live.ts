import {
  backendLatencies,
  counterRate,
  currentConnections,
} from "./haproxy-metrics";
import type { HAProxySnapshot, HAProxyHistory } from "./haproxy-api";

export interface HAProxyLiveNode {
  node_id: string;
  observed_at: string | null;
  fresh: boolean;
  scrape_ok: boolean;
  revoked: boolean;
  series: Array<{
    metric: HAProxyHistory["series"][number]["metric"];
    value: number;
  }>;
}
export interface HAProxyClusterLive {
  cluster_id: string;
  nodes: HAProxyLiveNode[];
}

function metricKey(metric: HAProxyHistory["series"][number]["metric"]): string {
  return JSON.stringify([metric.series, metric.code ?? "", metric.proxy ?? ""]);
}

/** Merge actual collection times into bounded chart buckets; never fill a gap. */
export function mergeLiveHistory(
  history: HAProxyHistory,
  frames: readonly HAProxyLiveNode[],
): HAProxyHistory {
  const samples = frames
    .filter(
      (frame) =>
        frame.node_id === history.node_id &&
        frame.fresh &&
        !frame.revoked &&
        frame.observed_at &&
        Number.isFinite(Date.parse(frame.observed_at)),
    )
    .sort((a, b) => Date.parse(a.observed_at!) - Date.parse(b.observed_at!));
  if (!samples.length) return history;
  const bucket = (frame: HAProxyLiveNode) =>
    Math.floor(Date.parse(frame.observed_at!) / 1000 / history.step_seconds) *
    history.step_seconds;
  const end = Math.max(history.end, ...samples.map(bucket));
  const start = end - (history.end - history.start);
  const lines = new Map(
    history.series.map((line) => [
      metricKey(line.metric),
      {
        metric: line.metric,
        values: new Map(line.values.filter(([at]) => at >= start && at <= end)),
      },
    ]),
  );
  for (const frame of samples) {
    const at = bucket(frame);
    if (at < start || at > end) continue;
    // The newest sample owns its bucket. Failed scrapes remove the bucket,
    // including a successful observation earlier in the same bucket.
    for (const line of lines.values()) line.values.delete(at);
    if (!frame.scrape_ok) continue;
    for (const point of frame.series) {
      if (!Number.isFinite(point.value) || point.value < 0) continue;
      const key = metricKey(point.metric);
      if (!lines.has(key) && lines.size >= 64) continue;
      const line = lines.get(key) ?? {
        metric: point.metric,
        values: new Map<number, number>(),
      };
      line.values.set(at, point.value);
      lines.set(key, line);
    }
  }
  return {
    ...history,
    start,
    end,
    series: [...lines.values()].map((line) => ({
      metric: line.metric,
      values: [...line.values].sort(([a], [b]) => a - b),
    })),
  };
}

export function liveHistory(node: HAProxyLiveNode): HAProxyHistory {
  const end = node.observed_at
    ? Math.floor(Date.parse(node.observed_at) / 1000)
    : Math.floor(Date.now() / 1000);
  return {
    node_id: node.node_id,
    range: "15m",
    start: end - 900,
    end,
    step_seconds: 1,
    series:
      node.fresh && node.scrape_ok && !node.revoked
        ? node.series.map((point) => ({
            metric: point.metric,
            values: [[end, point.value]],
          }))
        : [],
  };
}

/** Node inspection also stays live when no cluster overview owns a stream. */
export function snapshotLive(
  current: HAProxySnapshot,
  previous: HAProxySnapshot | null,
): HAProxyLiveNode {
  const series: HAProxyLiveNode["series"] = [];
  const add = (
    kind: string,
    value: number | null,
    labels: { code?: string; proxy?: string } = {},
  ) => {
    if (value !== null && Number.isFinite(value) && value >= 0)
      series.push({ metric: { series: kind, ...labels }, value });
  };
  for (const [kind, metric] of [
    ["traffic_in", "haproxy_frontend_bytes_in_total"],
    ["traffic_out", "haproxy_frontend_bytes_out_total"],
  ] as const) {
    const value = counterRate(current, previous, metric);
    add(kind, value === null ? null : value * 8);
  }
  add("connections", currentConnections(current));
  for (const code of ["1xx", "2xx", "3xx", "4xx", "5xx", "other"])
    add(
      "http_responses",
      counterRate(current, previous, "haproxy_frontend_http_responses_total", [
        "code",
        code,
      ]),
      { code },
    );
  for (const row of backendLatencies(current))
    add("latency", row.response === null ? null : row.response * 1000, {
      proxy: row.id,
    });
  return {
    node_id: current.node_id,
    observed_at: current.observed_at,
    fresh: current.fresh,
    scrape_ok: current.scrape_ok,
    revoked: false,
    series: current.fresh && current.scrape_ok ? series : [],
  };
}
