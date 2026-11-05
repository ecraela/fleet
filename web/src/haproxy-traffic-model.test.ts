import { describe, it, expect } from "vitest";
import {
  validTrafficPolicy,
  trafficPolicyInput,
  validTrafficCounts,
  validTrafficDecision,
  trafficRequestFilters,
  trafficLogFormat,
  type TrafficRuleCounts,
} from "./haproxy-traffic-model";

const id = "00000000-0000-0000-0000-000000000001";
const row = {
  id,
  name: "Country block",
  action: "block" as const,
  path: "/shop/items",
  cidrs: "192.0.2.0/24, 2001:db8::/32",
  countries: "ru, jp",
  asns: "AS15169, 13335",
};
describe("application traffic policy", () => {
  it("generates the complete cv5 profile and canonical single-address rules", () => {
    expect(trafficLogFormat.startsWith('log-format "cv5 ')).toBe(true);
    expect(
      trafficLogFormat.endsWith('%[var(txn.cv_rule)] %[var(txn.cv_decision)]"'),
    ).toBe(true);
    expect(
      trafficPolicyInput(
        "observe",
        "allow",
        [{ ...row, cidrs: "192.0.2.1, 2001:db8::1" }],
        "/shop",
      ).rules[0]?.cidrs,
    ).toEqual(["192.0.2.1/32", "2001:db8::1/128"]);
  });
  it("retains only typed traffic investigation filters in request links", () => {
    expect(
      trafficRequestFilters(
        `?traffic_rule=${id}&traffic_decision=would_block&spec_digest=${"b".repeat(64)}`,
      ),
    ).toEqual({
      traffic_rule: id,
      traffic_decision: "would_block",
      spec_digest: "b".repeat(64),
    });
    expect(
      trafficRequestFilters(
        "?traffic_rule=address&traffic_decision=deny&spec_digest=raw",
      ),
    ).toEqual({});
  });
  it("normalizes typed values while preserving order and explicit fallback", () => {
    const p = trafficPolicyInput("observe", "allow", [row], "/shop");
    expect(p.version).toBe(1);
    expect(p.rules[0]?.countries).toEqual(["RU", "JP"]);
    expect(p.rules[0]?.asns).toEqual([15169, 13335]);
    expect(validTrafficPolicy(p, "/shop")).toBe(true);
    for (const change of [
      { cidrs: "192.0.2.1/24" },
      { cidrs: "192.0.2.0/24,192.0.2.0/24" },
      { cidrs: "::ffff:192.0.2.0/120" },
      { countries: "RU } http-request allow" },
      { asns: "1e3" },
      { asns: "4294967296" },
      { asns: "0" },
      { path: "/admin" },
      { name: "bad\nname" },
    ])
      expect(() =>
        trafficPolicyInput(
          "enforce",
          "block",
          [{ ...row, ...change }],
          "/shop",
        ),
      ).toThrow();
    expect(() => trafficPolicyInput("observe", "allow", [], "/shop")).toThrow();
    expect(() =>
      trafficPolicyInput(
        "observe",
        "allow",
        [...Array(9)].map((_, i) => ({ ...row, id: id.slice(0, -1) + i })),
        "/shop",
      ),
    ).toThrow();
    expect(
      validTrafficPolicy(
        { version: 1, mode: "observe", unknown_action: "allow", rules: [null] },
        "/shop",
      ),
    ).toBe(false);
  });
  it("validates scoped decision counts and collection gaps", () => {
    const c: TrafficRuleCounts = {
      organization_id: id,
      cluster_id: id,
      application_id: id,
      spec_digest: "a".repeat(64),
      start_ms: 1000,
      end_ms: 2000,
      collection_complete: false,
      reporting_nodes: 1,
      expected_nodes: 2,
      matches: [{ rule_id: id, decision: "would_block", requests: 2 }],
    };
    expect(validTrafficCounts(c, id, id, id)).toBe(true);
    expect(
      validTrafficCounts({ ...c, collection_complete: true }, id, id, id),
    ).toBe(false);
    expect(
      validTrafficCounts({ ...c, organization_id: "foreign" }, id, id, id),
    ).toBe(false);
    expect(
      validTrafficCounts(
        { ...c, matches: [{ ...c.matches[0]!, decision: "raw" }] },
        id,
        id,
        id,
      ),
    ).toBe(false);
    expect(validTrafficCounts({ ...c, matches: [null] }, id, id, id)).toBe(
      false,
    );
    expect(
      validTrafficDecision({ rule_id: "192.0.2.1", decision: "block" }),
    ).toBe(false);
    expect(
      validTrafficDecision({ rule_id: "unknown", decision: "block" }),
    ).toBe(true);
  });
});
