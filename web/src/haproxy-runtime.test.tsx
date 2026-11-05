import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { HAProxyClient, HAProxyNodeDiscovery } from "./haproxy-api";
import {
  HAProxyRuntime,
  hasOtherCapacity,
  runtimeControlReason,
} from "./haproxy-runtime";

vi.mock("./permissions", () => ({
  usePermissionAction: () => ({ runAction: vi.fn() }),
  isActionCancelled: () => false,
}));
const now = Date.now();
const inventory = (): HAProxyNodeDiscovery => ({
  control: {
    node_id: "node-a",
    connected_at: new Date(now).toISOString(),
    last_seen: new Date(now).toISOString(),
    discovery: {
      version: "3.4.7",
      process_id: "8",
      started_at: 1792800000,
      config_fingerprint: "a".repeat(64),
      generation: "b".repeat(64),
      control_allowed: true,
      observed_at_ms: now,
      error_code: "",
      config_files: 1,
      proxies: [
        {
          name: "public",
          kind: "frontend",
          mode: "http",
          algorithm: "",
          status: "OPEN",
        },
      ],
      servers: ["app-a", "app-b"].map((name) => ({
        backend: "application",
        name,
        address: "127.0.0.1:8080",
        state: "ready" as const,
        weight: 100,
        initial_weight: 100,
        health: "UP",
        active_connections: 0,
        admin_flags: 0,
      })),
    },
  },
  connected: true,
  fresh: true,
  management_enabled: true,
  operations: [],
});

describe("direct HAProxy runtime controls", () => {
  it("requires permission, local policy, fresh discovery and one operation at a time without adoption", () => {
    const value = inventory();
    value.control.active_rollout_id = "rollout-a";
    expect(runtimeControlReason(value, undefined, now)).toContain(
      "cluster update",
    );
    delete value.control.active_rollout_id;
    expect(runtimeControlReason(value, undefined, now)).toBeUndefined();
    expect(runtimeControlReason(value, "You need haproxy.manage", now)).toBe(
      "You need haproxy.manage",
    );
    expect(
      runtimeControlReason({ ...value, connected: false }, undefined, now),
    ).toContain("control connection");
    expect(runtimeControlReason(value, undefined, now + 7000)).toContain(
      "control connection",
    );
    value.control.discovery!.observed_at_ms = now - 7000;
    expect(runtimeControlReason(value, undefined, now)).toContain(
      "fresh configuration",
    );
    value.control.discovery!.observed_at_ms = now;
    value.control.discovery!.control_allowed = false;
    expect(runtimeControlReason(value, undefined, now)).toContain(
      "allow runtime control",
    );
    value.control.discovery!.control_allowed = true;
    value.operations = [
      { status: "running" },
    ] as HAProxyNodeDiscovery["operations"];
    expect(runtimeControlReason(value, undefined, now)).toContain(
      "current HAProxy change",
    );
  });
  it("requires a healthy ready positive-weight peer in the same pool before removing normal selection", () => {
    const discovery = inventory().control.discovery!;
    const target = discovery.servers[0]!;
    expect(hasOtherCapacity(discovery, target)).toBe(true);
    for (const peer of [
      { ...target, name: "app-b", health: "DOWN" },
      { ...target, name: "app-b", state: "drain" as const },
      { ...target, name: "app-b", weight: 0 },
      { ...target, name: "app-b", backend: "other" },
    ]) {
      expect(
        hasOtherCapacity({ ...discovery, servers: [target, peer] }, target),
      ).toBe(false);
    }
  });
  it("shows inventory and history without an adoption or approval action", () => {
    const client = {
      cachedDiscovery: () => inventory(),
    } as unknown as HAProxyClient;
    const html = renderToStaticMarkup(
      <HAProxyRuntime nodeId="node-a" client={client} />,
    );
    expect(html).toContain("Runtime controls");
    expect(html).toContain("Runtime controls enabled");
    expect(html).toContain("app-a");
    expect(html).toContain("app-b");
    expect(html).not.toMatch(
      /Enable runtime controls|Review configuration|Adopt|Approve/,
    );
  });
});
