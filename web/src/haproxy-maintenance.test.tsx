import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { HAProxyClient, HAProxyMaintenanceView } from "./haproxy-api";
import {
  HAProxyMaintenancePanel,
  maintenanceReason,
  maintenanceStatus,
  maintenanceConfirmation,
} from "./haproxy-maintenance";
import { configurationControlReason } from "./haproxy-configuration";
import { runtimeControlReason } from "./haproxy-runtime";
vi.mock("./permissions", () => ({
  usePermissionAction: () => ({ runAction: vi.fn() }),
}));
function fixture(): HAProxyMaintenanceView {
  const control = {
    node_id: "a",
    connected_at: new Date().toISOString(),
    last_seen: new Date().toISOString(),
    discovery: {
      observed_at_ms: Date.now(),
      control_allowed: true,
      configuration: { control_allowed: true, observed_at_ms: Date.now() },
      proxies: [{ kind: "frontend", name: "public", active_connections: 0 }],
    },
  };
  return {
    node_id: "a",
    cluster_id: "pair",
    frontend: "public",
    reason_code: "",
    controller_name: "pair-entrypoint",
    controller_node_id: "entrypoint",
    target: control,
    controller: { ...control, node_id: "entrypoint" },
    maintenance: {
      id: "maintenance-a",
      status: "maintenance",
      frontend: "public",
      connections: 0,
      updated_at: new Date().toISOString(),
      error_code: "",
    },
  } as HAProxyMaintenanceView;
}
describe("whole-node maintenance", () => {
  it("offers explicit rejoin with permission checks and no approvals", () => {
    const view = fixture();
    const client = {
      cachedMaintenance: () => view,
    } as unknown as HAProxyClient;
    const html = renderToStaticMarkup(
      <HAProxyMaintenancePanel nodeId="a" client={client} />,
    );
    expect(html).toContain("In maintenance");
    expect(html).toContain("Rejoin traffic");
    expect(html).toContain("never forcibly closed");
    expect(html).toContain("5 seconds");
    expect(html).not.toMatch(/Approve|Review changes|Awaiting approval/);
    const denied = renderToStaticMarkup(
      <HAProxyMaintenancePanel
        nodeId="a"
        client={client}
        permissionReason="Your role cannot manage HAProxy."
      />,
    );
    expect(denied).toContain("Your role cannot manage HAProxy.");
    expect(denied).toContain("disabled");
  });
  it("does not confuse an unknown connection count or an unmanaged entrypoint with a drained node", () => {
    const view = fixture();
    view.reason_code = "entrypoint_not_managed";
    expect(maintenanceReason(view)).toContain("Bind an enrolled entrypoint");
    view.reason_code = "";
    delete view.target.discovery!.proxies[0]!.active_connections;
    expect(maintenanceReason(view)).toContain("Upgrade");
    expect(maintenanceStatus("draining")).toBe("Draining connections");
    expect(maintenanceStatus("checking_rejoin")).toBe("Checking rejoin health");
    expect(maintenanceStatus("attention")).toBe("Needs attention");
  });
  it("fences ordinary runtime and saved edits while preserving edits on a held target", () => {
    const control = { ...fixture().target, maintenance_blocked: true };
    expect(
      runtimeControlReason({ control } as Parameters<
        typeof runtimeControlReason
      >[0]),
    ).toContain("Whole-node maintenance");
    expect(
      configurationControlReason({ control } as Parameters<
        typeof configurationControlReason
      >[0]),
    ).toContain("Whole-node maintenance");
  });
});

it("names the target and traffic consequence before maintenance is confirmed", () => {
  const withdraw = maintenanceConfirmation(
    "withdraw",
    "edge-eu-1",
    "shared-edge",
    60,
  );
  expect(withdraw.title).toContain("maintenance");
  expect(withdraw.description).toContain("edge-eu-1");
  expect(withdraw.description).toContain("shared-edge");
  expect(withdraw.description).toContain("60 seconds");
  expect(withdraw.description).toContain("not forcibly closed");
  const rejoin = maintenanceConfirmation(
    "rejoin",
    "edge-eu-1",
    "shared-edge",
    60,
  );
  expect(rejoin.confirmLabel).toBe("Check health and rejoin");
  expect(rejoin.description).toContain("before and after rejoining");
  expect(rejoin.description).toContain("withdraws the node again");
});
