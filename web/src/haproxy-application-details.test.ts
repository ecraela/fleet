import { describe, expect, it } from "vitest";
import {
  applicationHealthLabel,
  probeStatus,
  currentApplicationHealth,
} from "./haproxy-application-details";
import {
  validApplicationHealth,
  type HAProxyApplicationProbe,
} from "./haproxy-api";

const probe: HAProxyApplicationProbe = {
  application_id: "00000000-0000-0000-0000-000000000001",
  spec_digest: "a".repeat(64),
  name: "store-https",
  scheme: "https",
  path: "/shop/items",
  expected_status: 200,
  status_code: 200,
  observed_at_ms: 10000,
  duration_ms: 12,
  error_code: "",
  tls_verified: true,
  certificate_sha256: "b".repeat(64),
  certificate_not_after_ms: 100000,
};
const health = {
  application_id: probe.application_id,
  configuration: "loaded" as const,
  reachability: "reachable" as const,
  nodes: [
    {
      node_id: "00000000-0000-0000-0000-000000000002",
      name: "haproxy-a",
      fresh: true,
      observed_at_ms: 11000,
      loaded: true,
      revision: 1,
      spec_digest: probe.spec_digest,
      servers: [],
      probes: [probe],
    },
  ],
};
describe("Application delivery evidence", () => {
  it("never presents missing, stale, future or disconnected checks as a current success", () => {
    expect(probeStatus(probe, true, 11000)).toBe("Passed");
    expect(probeStatus(probe, false, 11000)).toBe("Unknown");
    expect(probeStatus(probe, true, 26000)).toBe("Unknown");
    expect(probeStatus(probe, true, 8000)).toBe("Unknown");
    expect(probeStatus({ ...probe, tls_verified: false }, true, 11000)).toBe(
      "Unknown",
    );
    expect(
      probeStatus({ ...probe, error_code: "tls_failed" }, true, 11000),
    ).toBe("TLS verification failed");
    expect(
      probeStatus(
        { ...probe, error_code: "response_status", status_code: 503 },
        true,
        11000,
      ),
    ).toContain("503");
    expect(applicationHealthLabel()).toBe("Unknown");
    expect(currentApplicationHealth(health, 11000)?.reachability).toBe(
      "reachable",
    );
    expect(currentApplicationHealth(health, 18000)?.reachability).toBe(
      "unknown",
    );
    expect(currentApplicationHealth(health, 18000)?.configuration).toBe(
      "unknown",
    );
    expect(currentApplicationHealth(health, 18000)?.nodes[0]?.probes).toEqual([
      probe,
    ]);
  });
  it("rejects unbounded and cross-application health evidence", () => {
    expect(validApplicationHealth([health])).toBe(true);
    expect(validApplicationHealth([health, health])).toBe(false);
    for (const invalid of [
      null,
      {},
      [{ ...health, nodes: Array(13).fill(health.nodes[0]) }],
      [
        {
          ...health,
          nodes: [
            {
              ...health.nodes[0],
              probes: [{ ...probe, application_id: "foreign" }],
            },
          ],
        },
      ],
      [
        {
          ...health,
          nodes: [
            { ...health.nodes[0], probes: [{ ...probe, duration_ms: 100000 }] },
          ],
        },
      ],
    ])
      expect(validApplicationHealth(invalid)).toBe(false);
  });
  it("expires disabled/deleted observations without presenting them as failed request checks", () => {
    for (const state of ["disabled", "deleted"] as const) {
      const inactive = {
        ...health,
        reachability: state,
        nodes: health.nodes.map((n) => ({ ...n, probes: [] })),
      };
      expect(validApplicationHealth([inactive])).toBe(true);
      expect(currentApplicationHealth(inactive, 11000)?.reachability).toBe(
        state,
      );
      expect(currentApplicationHealth(inactive, 18000)?.reachability).toBe(
        "unknown",
      );
      expect(applicationHealthLabel(state)).toBe(
        state === "disabled" ? "Disabled" : "Deleted",
      );
    }
  });
});
