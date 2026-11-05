import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type {
  HAProxyClient,
  HAProxyRollout,
  HAProxyMaintenance,
} from "./haproxy-api";
import {
  HAProxyRollouts,
  rolloutStatus,
  rolloutError,
  rolloutRequestRows,
  rolloutRequestResult,
} from "./haproxy-rollouts";
import {
  clusterMaintenanceHistory,
  rolloutMaintenances,
} from "./haproxy-cluster";

export function rolloutFixture(
  status: HAProxyRollout["status"] = "rolling_back",
): HAProxyRollout {
  return {
    id: "rollout-a",
    organization_id: "org-a",
    cluster_id: "cluster-a",
    request_id: "request-a",
    document: {
      backends: [
        {
          name: "application",
          algorithm: "roundrobin",
          servers: [
            {
              name: "app-a",
              address: "backend.internal",
              port: 8080,
              weight: 200,
              maintenance: false,
            },
          ],
        },
      ],
    },
    status,
    error_code: status === "rolling_back" ? "health_check_failed" : "",
    index: 0,
    created_at: "2026-10-25T01:00:00Z",
    updated_at: "2026-10-25T01:00:05Z",
    nodes: ["node-a", "node-b"].map((id, i) => ({
      node_id: id,
      name: `haproxy-${i ? "b" : "a"}`,
      expected_generation: "a".repeat(64),
      expected_artifact_id: "b".repeat(64),
      expected_version: 1,
      baseline_revision_id: `baseline-${i}`,
      status: i ? "skipped" : "rolling_back",
      error_code: i ? "" : "health_check_failed",
      health_since_ms: 0,
      last_health_ms: 0,
    })),
  };
}
describe("cluster configuration rollout progress", () => {
  it("distinguishes entrypoint failure from passing direct node checks", () => {
    const rollout = rolloutFixture("rolled_back");
    rollout.error_code = "application_entrypoint_https_failed";
    const check = {
      name: "store-https-node",
      scope: "node" as const,
      application_id: "app-a",
      scheme: "https" as const,
      path: "/shop/items",
      expected_status: 200,
      status_code: 200,
      tls_verified: true,
      observed_at_ms: 2000,
      passes: 2,
      error_code: "",
    };
    rollout.nodes[0]!.application_verification = {
      phase: "after_rejoin",
      status: "failed",
      started_at_ms: 1000,
      error_code: rollout.error_code,
      checks: [
        check,
        {
          ...check,
          name: "store-https-entrypoint",
          scope: "entrypoint",
          passes: 0,
          tls_verified: false,
          status_code: 0,
          error_code: "tls_failed",
        },
      ],
    };
    expect(
      rolloutRequestRows(rollout).map((r) => [r.location, r.result]),
    ).toEqual([
      ["Direct node", "Passed"],
      ["Entrypoint", "TLS verification failed"],
    ]);
    const html = renderToStaticMarkup(
      <HAProxyRollouts
        clusterId="cluster-a"
        client={{ cachedRollouts: () => [rollout] } as unknown as HAProxyClient}
      />,
    );
    expect(html).toContain("Direct node");
    expect(html).toContain("Entrypoint");
    expect(html).toContain("shared entrypoint failed after rejoin");
    expect(
      rolloutError("application_entrypoint_probe_policy_changed"),
    ).toContain("local entrypoint check policy");
    expect(rolloutError("rollback_entrypoint_verification_failed")).toContain(
      "Restored nodes remain in traffic",
    );
    const gate = rollout.nodes[0]!.application_verification;
    expect(rolloutRequestResult({ ...check, passes: 1 }, gate)).toBe(
      "HTTP 200 (1 / 2 passes)",
    );
    expect(
      rolloutRequestResult(
        { ...gate.checks[1]!, error_code: "" },
        { ...gate, error_code: "application_entrypoint_verification_timeout" },
      ),
    ).toBe("Timed out");
  });
  it("keeps failed deployment checks alongside the verified rollback", () => {
    const rollout = rolloutFixture("rolled_back");
    const failed = {
      phase: "before_rejoin" as const,
      status: "failed" as const,
      started_at_ms: 1000,
      error_code: "application_https_failed",
      checks: [
        {
          name: "store-https",
          application_id: "app-a",
          scheme: "https" as const,
          path: "/shop/items",
          expected_status: 200,
          status_code: 0,
          tls_verified: false,
          observed_at_ms: 2000,
          passes: 0,
          error_code: "tls_failed",
        },
      ],
    };
    rollout.nodes[0]!.application_verification = failed;
    rollout.nodes[0]!.rollback_application_verification = {
      ...failed,
      phase: "after_rejoin",
      status: "passed",
      error_code: "",
      checks: failed.checks.map((c) => ({
        ...c,
        passes: 2,
        tls_verified: true,
        status_code: 200,
        error_code: "",
      })),
    };
    expect(
      rolloutRequestRows(rollout).map((r) => [r.direction, r.phase, r.result]),
    ).toEqual([
      ["Deployment", "Before rejoin", "TLS verification failed"],
      ["Rollback", "After rejoin", "Passed"],
    ]);
    const client = {
      cachedRollouts: () => [rollout],
    } as unknown as HAProxyClient;
    const html = renderToStaticMarkup(
      <HAProxyRollouts clusterId="cluster-a" client={client} />,
    );
    expect(html).toContain("Application request verification");
    expect(html).toContain("TLS verification failed");
    expect(html).toContain("After rejoin");
    expect(html).not.toMatch(/Approve|Awaiting approval/);
    expect(
      rolloutRequestResult({ ...failed.checks[0]!, error_code: "" }, failed),
    ).toBe("Not completed");
    expect(
      rolloutRequestResult(
        { ...failed.checks[0]!, error_code: "", passes: 1 },
        { ...failed, error_code: "application_verification_timeout" },
      ),
    ).toBe("Timed out");
  });
  it("shows automatic recovery per node without review or approval controls", () => {
    const client = {
      cachedRollouts: () => [rolloutFixture()],
    } as unknown as HAProxyClient;
    const html = renderToStaticMarkup(
      <HAProxyRollouts clusterId="cluster-a" client={client} />,
    );
    expect(html).toContain("Rolling configuration");
    expect(html).toContain("Rolling back");
    expect(html).toContain("haproxy-a");
    expect(html).toContain("haproxy-b");
    expect(html).toContain("reverse order");
    expect(html).toContain("Not changed");
    expect(html).not.toMatch(/Approve|Review changes|Awaiting approval/);
  });
  it("keeps recovery failure distinct from completion", () => {
    expect(rolloutStatus("rollback_failed")).toBe("Recovery needs attention");
    expect(rolloutStatus("rolled_back")).toBe("Rolled back");
    expect(rolloutStatus("checking")).toBe("Checking health");
    const value = rolloutFixture("rollback_failed");
    value.error_code = "node_unavailable";
    const client = {
      cachedRollouts: () => [value],
    } as unknown as HAProxyClient;
    const html = renderToStaticMarkup(
      <HAProxyRollouts clusterId="cluster-a" client={client} />,
    );
    expect(html).toContain("Check failed nodes and their version history");
  });
  it("shows drain counts, rejoin verification and recovery reservations", () => {
    const value = rolloutFixture("running");
    value.nodes[0]!.status = "draining";
    value.nodes[0]!.maintenance = {
      id: "maintenance-a",
      node_id: "node-a",
      status: "draining",
      connections: 3,
      health_since_ms: 0,
      last_health_ms: 0,
    } as HAProxyMaintenance;
    const client = {
      cachedRollouts: () => [value],
    } as unknown as HAProxyClient;
    let html = renderToStaticMarkup(
      <HAProxyRollouts clusterId="cluster-a" client={client} />,
    );
    expect(html).toContain("Draining connections");
    expect(html).toContain("Connections to drain");
    expect(html).toContain(">3<");
    expect(rolloutMaintenances([value])).toEqual([value.nodes[0]!.maintenance]);
    const recovered = {
      ...value.nodes[0]!.maintenance,
      status: "completed" as const,
    };
    expect(clusterMaintenanceHistory([recovered], [value])).toEqual([
      recovered,
    ]);
    value.nodes[0]!.maintenance.status = "checking_rejoin";
    value.nodes[0]!.maintenance.health_since_ms = 1000;
    value.nodes[0]!.maintenance.last_health_ms = 4000;
    html = renderToStaticMarkup(
      <HAProxyRollouts clusterId="cluster-a" client={client} />,
    );
    expect(html).toContain("Before rejoin: 3 / 5 seconds");
    value.nodes[0]!.recovery_maintenance_id = "maintenance-a";
    value.status = "rollback_failed";
    html = renderToStaticMarkup(
      <HAProxyRollouts clusterId="cluster-a" client={client} />,
    );
    expect(html).toContain("reserved for maintenance");
    expect(html).toContain("Its peer continues serving traffic");
    expect(html).not.toMatch(/Approve|Review changes|Awaiting approval/);

    value.nodes[0]!.status = "rollback_failed";
    value.nodes[0]!.error_code = "maintenance_deadline";
    value.error_code = "maintenance_deadline";
    value.nodes[0]!.maintenance.status = "completed";
    html = renderToStaticMarkup(
      <HAProxyRollouts clusterId="cluster-a" client={client} />,
    );
    expect(html).toContain("Stopped (maintenance recovered)");
    expect(html).toContain("Rejoined after recovery");
    expect(html).toContain("Recovery later completed");
    expect(html).toContain("may keep their applied version");
    expect(html).not.toContain("reserved for maintenance");
    expect(html).not.toContain("Recovery needs attention");

    value.nodes[0]!.recovery_maintenance_id = "a-different-reservation";
    html = renderToStaticMarkup(
      <HAProxyRollouts clusterId="cluster-a" client={client} />,
    );
    expect(html).toContain("Recovery needs attention");
    expect(html).not.toContain("Stopped (maintenance recovered)");
  });
});
