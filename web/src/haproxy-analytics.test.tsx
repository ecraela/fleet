import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import {
  MetricCard,
  MetricGrid,
  TimeSeriesChart,
  TrafficTopology,
} from "@ecraela/ui";
import { clusterMetricSeries } from "./haproxy-cluster";
import type { HAProxyNode, HAProxyHistory } from "./haproxy-api";

const history = {
  start: 0,
  end: 60,
  step: 10,
  label: "Recorded bandwidth",
  values: [
    [0, 4],
    [10, 8],
    [60, 12],
  ] as const,
};
const node = (id: string): HAProxyNode => ({
  id,
  name: `haproxy-${id}`,
  cluster_id: "pair",
  organization_id: "qa",
  created_at: "2026-11-02T00:00:00Z",
  observed_at: "2026-11-02T00:00:00Z",
  telemetry_fresh: true,
  scrape_ok: true,
  metrics_available: true,
});
const records = (
  id: string,
  values: Array<[number, number]>,
): HAProxyHistory => ({
  node_id: id,
  range: "15m",
  start: 0,
  end: 60,
  step_seconds: 10,
  series: [{ metric: { series: "traffic_out" }, values }],
});

describe("recorded analytics presentation", () => {
  it("distinguishes a measured zero from an unobserved metric", () => {
    const html = renderToStaticMarkup(
      <MetricGrid columns={3}>
        <MetricCard label="Connections" value={0} />
        <MetricCard label="Traffic out" value={null} />
      </MetricGrid>,
    );
    expect(html).toContain('cv-metric-card__value">0<');
    expect(html).toContain('cv-metric-card__value">Not observed<');
  });
  it("breaks sparkline fills at missing buckets and preserves isolated observations", () => {
    const html = renderToStaticMarkup(
      <MetricGrid>
        <MetricCard label="Traffic out" value="12 bit/s" history={history} />
      </MetricGrid>,
    );
    expect(html.match(/<g>/g)).toHaveLength(2);
    expect(html).toContain("<circle");
    expect(html).not.toContain("L298.00");
    expect(html).not.toContain("NaN");
    expect(html).toContain('aria-label="Recorded bandwidth"');
  });
  it("shows the latest recorded bucket rather than an empty end bucket", () => {
    const html = renderToStaticMarkup(
      <TimeSeriesChart
        title="Traffic"
        unit="bit/s"
        start={0}
        end={60}
        step={10}
        variant="panel"
        series={[{ id: "a", label: "haproxy-a", values: [[20, 37]] }]}
      />,
    );
    expect(html).toContain("1970-01-01T00:00:20.000Z");
    expect(html).toContain("Latest observation");
    expect(html).toContain('data-color="blue"');
    expect(html).not.toContain("No observation");
  });
  it("stacks only recorded positive response values", () => {
    const html = renderToStaticMarkup(
      <TimeSeriesChart
        title="Responses"
        unit="requests/s"
        start={0}
        end={60}
        step={10}
        variant="panel"
        plot="bars"
        series={[
          {
            id: "2xx",
            label: "2xx",
            tone: "success",
            values: [
              [10, 8],
              [20, 0],
            ],
          },
          { id: "5xx", label: "5xx", tone: "error", values: [[10, 2]] },
        ]}
      />,
    );
    const bars = [
      ...html.matchAll(
        /<rect x="[^"]+" y="([^"]+)" width="[^"]+" height="([^"]+)" fill="currentColor"/g,
      ),
    ];
    expect(bars).toHaveLength(2);
    expect(Number(bars[0]![1]) + Number(bars[0]![2])).toBeCloseTo(174);
    expect(Number(bars[1]![1]) + Number(bars[1]![2])).toBeCloseTo(
      Number(bars[0]![1]),
    );
    expect(html).toContain('data-tone="success"');
    expect(html).toContain('data-tone="error"');
  });
  it("does not invent an observation time for an empty chart", () => {
    const html = renderToStaticMarkup(
      <TimeSeriesChart
        title="Traffic"
        unit="bit/s"
        start={0}
        end={60}
        step={10}
        variant="panel"
        series={[]}
      />,
    );
    expect(html).toContain("No observations");
    expect(html).not.toContain("Latest observation");
  });
  it("combines a cluster bucket only when every displayed node reported", () => {
    const nodes = [node("a"), node("b")];
    expect(
      clusterMetricSeries(
        nodes,
        {
          a: records("a", [
            [0, 2],
            [10, 4],
            [20, 6],
          ]),
          b: records("b", [
            [0, 3],
            [20, 8],
          ]),
        },
        "traffic_out",
      ),
    ).toEqual([
      [0, 5],
      [20, 14],
    ]);
    expect(
      clusterMetricSeries(nodes, { a: records("a", [[0, 2]]) }, "traffic_out"),
    ).toEqual([]);
  });
});

describe("bounded vertical topology", () => {
  const nodes = Array.from({ length: 64 }, (_, i) => ({
    id: `edge-${i + 1}`,
    label: `haproxy-edge-${i + 1}`,
    address: `edge-${i + 1}:8080`,
    status: "Reporting",
    tone: "success" as const,
    description: "84 kbit/s out",
  }));
  it("keeps small clusters as cards without connector lines", () => {
    const html = renderToStaticMarkup(
      <TrafficTopology
        entrypoint="https://apps.example.test"
        strategy="Round robin"
        backend="store-api:8080"
        nodes={nodes.slice(0, 2)}
        onSelect={() => {}}
      />,
    );
    expect(html).not.toContain("__connector");
    expect(html).toContain("cv-traffic-topology__nodes");
    expect(html).not.toContain('aria-label="Find topology nodes"');
    expect(html.indexOf("Entrypoint")).toBeLessThan(
      html.indexOf("HAProxy nodes"),
    );
    expect(html.indexOf("HAProxy nodes")).toBeLessThan(
      html.indexOf(">Application<"),
    );
  });
  it("bounds a large node group to ten rows with search, status filters and pagination", () => {
    const html = renderToStaticMarkup(
      <TrafficTopology
        entrypoint="https://apps.example.test"
        strategy="Round robin"
        backend="store-api:8080"
        nodes={nodes}
        onSelect={() => {}}
      />,
    );
    expect(html).toContain("Find topology nodes");
    expect(html).toContain("Node status");
    expect(html).toContain('data-key="edge-10"');
    expect(html).not.toContain('data-key="edge-11"');
    expect(html).toContain('aria-label="Traffic-serving nodes pagination"');
    expect(html).toContain("64 nodes in this route");
  });
});
