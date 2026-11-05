import { describe, expect, it } from "vitest";
import type { HAProxyHistory } from "./haproxy-api";
import {
  liveHistory,
  mergeLiveHistory,
  type HAProxyLiveNode,
} from "./haproxy-live";
const history: HAProxyHistory = {
  node_id: "node-a",
  range: "15m",
  start: 100,
  end: 1000,
  step_seconds: 5,
  series: [
    {
      metric: { series: "traffic_out" },
      values: [
        [100, 1],
        [990, 2],
        [995, 3],
        [1000, 4],
      ],
    },
  ],
};
const frame = (
  at: number,
  value = 80,
  overrides: Partial<HAProxyLiveNode> = {},
): HAProxyLiveNode => ({
  node_id: "node-a",
  observed_at: new Date(at * 1000).toISOString(),
  fresh: true,
  scrape_ok: true,
  revoked: false,
  series: [{ metric: { series: "traffic_out" }, value }],
  ...overrides,
});
describe("HAProxy live chart merge", () => {
  it("updates a bucket immediately, rolls the window and leaves gaps unfilled", () => {
    const result = mergeLiveHistory(history, [
      frame(1001, 80),
      frame(1004, 90),
      frame(1016, 120),
    ]);
    expect(result.start).toBe(115);
    expect(result.end).toBe(1015);
    expect(result.series[0]?.values).toEqual([
      [990, 2],
      [995, 3],
      [1000, 90],
      [1015, 120],
    ]);
    expect(history.series[0]?.values.at(-1)).toEqual([1000, 4]);
  });
  it("removes failed scrape buckets instead of fabricating zero traffic", () => {
    expect(
      mergeLiveHistory(history, [
        frame(1001, 80),
        frame(1004, 0, { scrape_ok: false }),
      ]).series[0]?.values,
    ).toEqual([
      [100, 1],
      [990, 2],
      [995, 3],
    ]);
    expect(mergeLiveHistory(history, [frame(1004, 0, { fresh: false })])).toBe(
      history,
    );
    expect(mergeLiveHistory(history, [frame(1004, 0, { revoked: true })])).toBe(
      history,
    );
    expect(
      mergeLiveHistory(history, [frame(1004, 0, { node_id: "other-node" })]),
    ).toBe(history);
  });
  it("keeps the newest observation when full history reconciliation arrives", () => {
    const result = mergeLiveHistory(history, [frame(1002, 900)]);
    expect(result.series[0]?.values.at(-1)).toEqual([1000, 900]);
    expect(liveHistory(frame(1002, 900)).series[0]?.values).toEqual([
      [1002, 900],
    ]);
    expect(liveHistory(frame(1002, 900, { scrape_ok: false })).series).toEqual(
      [],
    );
  });
  it("bounds point and series counts across a long running or changing fleet", () => {
    const frames = Array.from({ length: 1000 }, (_, i) =>
      frame(1000 + i, 80, {
        series: [
          { metric: { series: "latency", proxy: `backend-${i}` }, value: i },
        ],
      }),
    );
    const result = mergeLiveHistory(history, frames);
    expect(result.series.length).toBeLessThanOrEqual(64);
    expect(result.series.every((line) => line.values.length <= 181)).toBe(true);
    expect(result.end - result.start).toBe(900);
  });
});
