import type { HAProxyNode, HAProxySnapshot, MetricSeries } from "./haproxy-api";

export function liveObservationStatus(
  snapshot: HAProxySnapshot | null,
  connected: boolean,
  now: number,
): { label: string; fresh: boolean; reconnecting: boolean } {
  const age = snapshot?.observed_at
    ? now - Date.parse(snapshot.observed_at)
    : Infinity;
  const fresh = Boolean(
    snapshot?.fresh && Number.isFinite(age) && age >= -60_000 && age <= 15_000,
  );
  const reconnecting = !connected && !fresh;
  return {
    fresh,
    reconnecting,
    label: fresh
      ? snapshot?.scrape_ok
        ? "Reporting"
        : "HAProxy unavailable"
      : reconnecting
        ? "Reconnecting to live observations"
        : snapshot?.observed_at
          ? "Telemetry stale"
          : "Waiting for first observation",
  };
}

export function nodeStatus(
  node: Pick<
    HAProxyNode,
    "revoked_at" | "metrics_available" | "telemetry_fresh" | "scrape_ok"
  >,
): { label: string; tone: "neutral" | "success" | "warning" | "error" } {
  if (node.revoked_at) return { label: "Revoked", tone: "neutral" };
  if (!node.metrics_available)
    return { label: "Observations unavailable", tone: "warning" };
  if (!node.telemetry_fresh)
    return { label: "Telemetry stale", tone: "warning" };
  if (!node.scrape_ok) return { label: "HAProxy unavailable", tone: "error" };
  return { label: "Reporting", tone: "success" };
}

export function metricValue(series: MetricSeries): number | null {
  if (typeof series.value?.[1] !== "string" || !series.value[1].trim())
    return null;
  const value = Number(series.value?.[1]);
  return Number.isFinite(value) ? value : null;
}

function matching(
  snapshot: HAProxySnapshot,
  name: string,
  label?: [string, string],
): MetricSeries[] {
  return snapshot.data.result.filter(
    (series) =>
      series.metric.__name__ === name &&
      (!snapshot.traffic_frontend ||
        !name.startsWith("haproxy_frontend_") ||
        series.metric.proxy === snapshot.traffic_frontend) &&
      (!label || series.metric[label[0]] === label[1]),
  );
}

function seriesKey(series: MetricSeries): string {
  return JSON.stringify(
    Object.entries(series.metric).sort(([a], [b]) => a.localeCompare(b)),
  );
}

export function counterRate(
  current: HAProxySnapshot | null,
  previous: HAProxySnapshot | null,
  name: string,
  label?: [string, string],
): number | null {
  if (
    !current?.fresh ||
    !previous?.fresh ||
    !current.scrape_ok ||
    !previous.scrape_ok ||
    current.node_id !== previous.node_id ||
    !current.observed_at ||
    !previous.observed_at
  )
    return null;
  const seconds =
    (Date.parse(current.observed_at) - Date.parse(previous.observed_at)) / 1000;
  if (!Number.isFinite(seconds) || seconds <= 0 || seconds > 60) return null;
  const now = matching(current, name, label),
    before = matching(previous, name, label);
  const uptime = matching(current, "haproxy_process_uptime_seconds")[0];
  const previousUptime = matching(
    previous,
    "haproxy_process_uptime_seconds",
  )[0];
  if (uptime && previousUptime) {
    const age = metricValue(uptime),
      priorAge = metricValue(previousUptime);
    if (age !== null && priorAge !== null && age < priorAge) return null;
  }
  if (!now.length || now.length !== before.length) return null;
  const old = new Map(
    before.map((series) => [seriesKey(series), metricValue(series)]),
  );
  let delta = 0;
  for (const series of now) {
    const value = metricValue(series),
      prior = old.get(seriesKey(series));
    if (
      value === null ||
      prior == null ||
      value < 0 ||
      prior < 0 ||
      value < prior
    )
      return null;
    delta += value - prior;
  }
  return delta / seconds;
}

export function currentConnections(
  snapshot: HAProxySnapshot | null,
): number | null {
  if (!snapshot?.fresh || !snapshot.scrape_ok) return null;
  const series = matching(
    snapshot,
    snapshot.traffic_frontend
      ? "haproxy_frontend_current_sessions"
      : "haproxy_process_current_connections",
  );
  return series.length === 1 ? metricValue(series[0]!) : null;
}

export interface BackendLatency {
  id: string;
  queue: number | null;
  connect: number | null;
  response: number | null;
  total: number | null;
}

export function backendLatencies(
  snapshot: HAProxySnapshot | null,
): BackendLatency[] {
  if (!snapshot?.fresh || !snapshot.scrape_ok) return [];
  const rows = new Map<string, BackendLatency>();
  for (const [field, metric] of Object.entries({
    queue: "queue_time_average_seconds",
    connect: "connect_time_average_seconds",
    response: "response_time_average_seconds",
    total: "total_time_average_seconds",
  })) {
    for (const series of matching(snapshot, `haproxy_backend_${metric}`)) {
      const name = series.metric.proxy;
      if (!name) continue;
      const row = rows.get(name) ?? {
        id: name,
        queue: null,
        connect: null,
        response: null,
        total: null,
      };
      row[field as "queue" | "connect" | "response" | "total"] =
        metricValue(series);
      rows.set(name, row);
    }
  }
  return [...rows.values()].sort((a, b) => a.id.localeCompare(b.id));
}

export function formatMetric(value: number | null, unit = ""): string {
  if (value === null || !Number.isFinite(value)) return "Unavailable";
  return `${new Intl.NumberFormat("en", { maximumFractionDigits: 1 }).format(value)}${unit ? ` ${unit}` : ""}`;
}

export function formatBandwidth(bytes: number | null): string {
  if (bytes === null) return "Waiting for two observations";
  return formatBits(bytes * 8);
}

export function formatBits(bits: number): string {
  if (bits >= 1e9) return formatMetric(bits / 1e9, "Gbit/s");
  if (bits >= 1e6) return formatMetric(bits / 1e6, "Mbit/s");
  if (bits >= 1e3) return formatMetric(bits / 1e3, "kbit/s");
  return formatMetric(bits, "bit/s");
}
