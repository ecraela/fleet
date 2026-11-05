import { useEffect, useState } from "react";
import {
  InlineTip,
  LoadingStatus,
  Section,
  SegmentedControl,
  TimeSeriesChart,
  type TimeSeries,
} from "@ecraela/ui";
import {
  HAProxyClient,
  type HAProxyHistory,
  type HAProxyHistoryRange,
} from "./haproxy-api";
import { formatBits, formatMetric } from "./haproxy-metrics";

export function useHAProxyHistory(
  client: HAProxyClient,
  nodeId: string,
  range: HAProxyHistoryRange,
) {
  const [state, setState] = useState<{
    range: HAProxyHistoryRange;
    nodeId: string;
    data: HAProxyHistory | null;
    loading: boolean;
    error: boolean;
  }>(() => ({
    range,
    nodeId,
    data: client.cachedHistory(nodeId, range)?.data ?? null,
    loading: true,
    error: false,
  }));
  useEffect(() => {
    const controller = new AbortController();
    let pending = false;
    const cached = client.cachedHistory(nodeId, range);
    setState((previous) => ({
      range,
      nodeId,
      data: cached?.data ?? (previous.nodeId === nodeId ? previous.data : null),
      loading: !cached,
      error: false,
    }));
    const update = async () => {
      if (
        pending ||
        controller.signal.aborted ||
        globalThis.document?.visibilityState === "hidden"
      )
        return;
      pending = true;
      try {
        const data = await client.history(nodeId, range, controller.signal);
        if (!controller.signal.aborted)
          setState({ range, nodeId, data, loading: false, error: false });
      } catch {
        if (!controller.signal.aborted)
          setState((current) => ({ ...current, loading: false, error: true }));
      } finally {
        pending = false;
      }
    };
    const unsubscribe = client.subscribeHistory(nodeId, () => {
      const data = client.cachedHistory(nodeId, range)?.data;
      if (data && !controller.signal.aborted)
        setState((current) => ({ ...current, range, data, loading: false }));
    });
    const resume = () => {
      if (document.visibilityState !== "hidden") void update();
    };
    document.addEventListener("visibilitychange", resume);
    if (!cached || Date.now() - cached.loadedAt >= 30_000) void update();
    const timer = setInterval(() => void update(), 30_000);
    return () => {
      controller.abort();
      clearInterval(timer);
      unsubscribe();
      document.removeEventListener("visibilitychange", resume);
    };
  }, [client, nodeId, range]);
  return state.range === range && state.nodeId === nodeId
    ? state
    : {
        range,
        nodeId,
        data:
          client.cachedHistory(nodeId, range)?.data ??
          (state.nodeId === nodeId ? state.data : null),
        loading: true,
        error: false,
      };
}

export function latestHistoryValue(
  history: HAProxyHistory | null | undefined,
  series: string,
  now = Date.now(),
): number | null {
  const point = history?.series
    .find((line) => line.metric.series === series)
    ?.values.at(-1);
  return point &&
    now - point[0] * 1000 <= 30_000 &&
    now >= point[0] * 1000 - 60_000
    ? point[1]
    : null;
}

export function HAProxyHistoryCharts({
  history,
  range,
  loading,
  error,
  onRangeChange,
  backendLabels = {},
}: {
  history: HAProxyHistory | null;
  range: HAProxyHistoryRange;
  loading: boolean;
  error: boolean;
  onRangeChange: (range: HAProxyHistoryRange) => void;
  backendLabels?: Readonly<Record<string, string>>;
}) {
  const select = (
    kind: string,
    label: (metric: HAProxyHistory["series"][number]["metric"]) => string,
    tone?: (
      metric: HAProxyHistory["series"][number]["metric"],
    ) => TimeSeries["tone"],
  ): TimeSeries[] =>
    (history?.series ?? [])
      .filter((line) => line.metric.series === kind)
      .map((line) => ({
        id: JSON.stringify(line.metric),
        label: label(line.metric),
        values: line.values,
        tone: tone?.(line.metric) ?? "neutral",
      }));
  const traffic = [
    ...select("traffic_in", () => "In"),
    ...select(
      "traffic_out",
      () => "Out",
      () => "muted",
    ),
  ].map((line, index) => ({
    ...line,
    color: (["blue", "teal"] as const)[index % 2]!,
  }));
  const responses = select(
    "http_responses",
    (metric) => metric.code ?? "Other",
    (metric) =>
      metric.code === "5xx"
        ? "error"
        : metric.code === "4xx"
          ? "warning"
          : metric.code === "2xx"
            ? "success"
            : metric.code === "3xx"
              ? "info"
              : "muted",
  ).sort((a, b) => a.label.localeCompare(b.label));
  return (
    <Section
      layout="stack"
      size="sm"
      title="Traffic history"
      description="Stored HAProxy observations. Gaps indicate missing telemetry. Bandwidth and response rates use a rolling window."
      actions={
        <SegmentedControl
          aria-label="History period"
          size="sm"
          selectedKey={range}
          onSelectionChange={(key) =>
            onRangeChange(String(key) as HAProxyHistoryRange)
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
      {error && (
        <InlineTip
          variant="warning"
          title="History could not refresh"
          role="status"
        >
          The query will retry automatically.{" "}
          {history
            ? "Last loaded history remains visible."
            : "Live observations are shown separately."}
        </InlineTip>
      )}
      {loading && history && history.range !== range && (
        <LoadingStatus delayMs={1000}>
          Updating the time range; previous history remains visible.
        </LoadingStatus>
      )}
      {loading && !history ? (
        <LoadingStatus delayMs={1000}>
          Loading stored observations…
        </LoadingStatus>
      ) : history ? (
        <div className="cv-haproxy-charts">
          <TimeSeriesChart
            variant="panel"
            title="Bandwidth"
            unit="bit/s"
            start={history.start}
            end={history.end}
            step={history.step_seconds}
            series={traffic}
            formatValue={formatBits}
          />
          <TimeSeriesChart
            variant="panel"
            title="HTTP responses"
            unit="responses/s"
            start={history.start}
            end={history.end}
            step={history.step_seconds}
            series={responses}
            formatValue={(value) => formatMetric(value, "responses/s")}
          />
          <TimeSeriesChart
            variant="panel"
            title="Active connections"
            unit="connections"
            start={history.start}
            end={history.end}
            step={history.step_seconds}
            series={select("connections", () => "Connections")}
            formatValue={(value) => formatMetric(value)}
          />
          <TimeSeriesChart
            variant="panel"
            title="Backend response time"
            unit="ms (rolling average)"
            start={history.start}
            end={history.end}
            step={history.step_seconds}
            series={select(
              "latency",
              (metric) =>
                backendLabels[metric.proxy ?? ""] ?? metric.proxy ?? "Backend",
            )}
            formatValue={(value) => formatMetric(value, "ms")}
          />
        </div>
      ) : null}
    </Section>
  );
}
