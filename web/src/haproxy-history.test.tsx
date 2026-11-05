import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { TimeSeriesChart } from "@ecraela/ui";
import type { HAProxyHistory } from "./haproxy-api";
import { HAProxyHistoryCharts, latestHistoryValue } from "./haproxy-history";

const history: HAProxyHistory = {
  node_id: "node-a",
  range: "1h",
  start: 1000,
  end: 4600,
  step_seconds: 15,
  series: [
    {
      metric: { series: "traffic_in" },
      values: [
        [4585, 800],
        [4600, 1200],
      ],
    },
    { metric: { series: "http_responses", code: "5xx" }, values: [[4600, 2]] },
    {
      metric: { series: "latency", proxy: "application" },
      values: [[4600, 12]],
    },
  ],
};

describe("HAProxy history", () => {
  it("keeps previous charts and explains a pending time-range change", () => {
    const html = renderToStaticMarkup(
      <HAProxyHistoryCharts
        history={history}
        range="24h"
        loading
        error={false}
        onRangeChange={() => {}}
      />,
    );
    expect(html).toContain("Bandwidth");
    expect(html).toContain("previous history remains visible");
    expect(html).not.toContain("Loading stored observations");
  });
  it("only uses recent stored rates while the next live observation arrives", () => {
    expect(latestHistoryValue(history, "traffic_in", 4_605_000)).toBe(1200);
    expect(latestHistoryValue(history, "traffic_in", 4_640_000)).toBeNull();
    expect(latestHistoryValue(history, "connections", 4_605_000)).toBeNull();
    expect(latestHistoryValue(history, "traffic_in", 4_000_000)).toBeNull();
  });

  it("shows stored time ranges and keeps charts visible when a refresh fails", () => {
    const html = renderToStaticMarkup(
      <HAProxyHistoryCharts
        history={history}
        range="1h"
        loading={false}
        error
        onRangeChange={() => {}}
      />,
    );
    for (const label of [
      "15m",
      "1h",
      "6h",
      "24h",
      "Bandwidth",
      "HTTP responses",
      "Active connections",
      "Backend response time",
      "Last loaded history remains visible",
      "5xx",
      "application",
      "Inspect bandwidth",
    ])
      expect(html).toContain(label);
    expect(html).not.toContain("p99");
  });

  it("breaks chart paths over collection gaps and supplies keyboard inspection", () => {
    const html = renderToStaticMarkup(
      <TimeSeriesChart
        title="Connections"
        unit="connections"
        start={100}
        end={150}
        step={5}
        series={[
          {
            id: "connections",
            label: "Connections",
            values: [
              [100, 1],
              [105, 2],
              [145, 3],
              [150, 4],
            ],
          },
        ]}
      />,
    );
    const path = html.match(/d="(M[^"]+)" fill="none"/u)?.[1];
    expect(path?.match(/M/gu)).toHaveLength(2);
    expect(path?.match(/L/gu)).toHaveLength(2);
    expect(html).toContain('type="range"');
    expect(html).toContain("Gaps mean observations are missing");
    expect(html).not.toContain("NaN");
  });
});
