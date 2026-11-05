import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type {
  HAProxyClient,
  HAProxyConfigurationDocument,
  HAProxyConfigurationView,
} from "./haproxy-api";
import {
  HAProxyConfiguration,
  configurationControlReason,
  configurationDraft,
  documentFromEdit,
  rolloutReadinessReason,
} from "./haproxy-configuration";

vi.mock("./permissions", () => ({
  usePermissionAction: () => ({ runAction: vi.fn() }),
  isActionCancelled: () => false,
}));
const now = Date.now();
const document: HAProxyConfigurationDocument = {
  backends: [
    {
      name: "application",
      algorithm: "roundrobin",
      servers: [
        {
          name: "app-a",
          address: "app.internal",
          port: 8080,
          weight: 100,
          maintenance: false,
        },
        {
          name: "app-b",
          address: "127.0.0.2",
          port: 8080,
          weight: 100,
          maintenance: false,
        },
      ],
    },
  ],
};
function view(): HAProxyConfigurationView {
  return {
    control: {
      node_id: "node-a",
      current_revision_id: "revision-a",
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
        proxies: [],
        servers: [],
        config_files: 1,
        configuration: {
          version: 1,
          artifact_id: "c".repeat(64),
          observed_at_ms: now,
          control_allowed: true,
          error_code: "",
          document,
        },
      },
    },
    connected: true,
    fresh: true,
    configuration_enabled: true,
    versions: [
      {
        id: "revision-a",
        organization_id: "org-a",
        node_id: "node-a",
        number: 1,
        document,
        artifact_id: "c".repeat(64),
        source: "discovered",
        status: "observed",
        error_code: "",
        created_at: new Date(now).toISOString(),
      },
    ],
  };
}
describe("persistent HAProxy configuration", () => {
  it("requires completed checks and saved runtime weights before a cluster update", () => {
    const data = view();
    const discovery = data.control.discovery!;
    discovery.proxies = [
      {
        name: "web",
        kind: "frontend",
        mode: "http",
        algorithm: "",
        status: "OPEN",
      },
    ];
    discovery.servers = document.backends[0]!.servers.map((server) => ({
      backend: "application",
      name: server.name,
      address: server.address,
      state: "ready",
      weight: server.weight,
      initial_weight: server.weight,
      active_connections: 0,
      health: "UP",
      check_status: "L7OK",
      admin_flags: 0,
    }));
    expect(rolloutReadinessReason(data)).toBeUndefined();
    discovery.servers[0]!.weight = 50;
    expect(rolloutReadinessReason(data)).toContain("saved weight");
    discovery.servers[0]!.weight = 100;
    discovery.servers[0]!.check_status = "INI";
    expect(rolloutReadinessReason(data)).toContain("health check");
    discovery.servers[0]!.check_status = "L7OK";
    discovery.proxies[0]!.status = "STOP";
    expect(rolloutReadinessReason(data)).toContain("open HAProxy frontend");
  });
  it("keeps editing behind permissions, local policy, fresh discovery and serialized changes", () => {
    const data = view();
    data.control.active_rollout_id = "rollout-a";
    expect(configurationControlReason(data, undefined, now)).toContain(
      "cluster update",
    );
    delete data.control.active_rollout_id;
    expect(configurationControlReason(data, undefined, now)).toBeUndefined();
    expect(
      configurationControlReason(data, "haproxy.manage required", now),
    ).toBe("haproxy.manage required");
    expect(
      configurationControlReason({ ...data, connected: false }, undefined, now),
    ).toContain("control connection");
    expect(configurationControlReason(data, undefined, now + 7000)).toContain(
      "control connection",
    );
    expect(
      configurationControlReason({ ...data, fresh: false }, undefined, now),
    ).toContain("fresh saved configuration");
    expect(
      configurationControlReason(
        { ...data, configuration_enabled: false },
        undefined,
        now,
      ),
    ).toContain("enabled on this node");
    data.versions[0]!.status = "running";
    expect(configurationControlReason(data, undefined, now)).toContain(
      "current configuration change",
    );
  });
  it("edits a backend without mutating cached settings or another pool", () => {
    const original = structuredClone(document);
    original.backends.push({
      ...structuredClone(original.backends[0]!),
      name: "other",
    });
    const servers = configurationDraft(original.backends[0]!);
    servers[0]!.weight = "200";
    servers[0]!.address = "2001:db8::1";
    const changed = documentFromEdit({
      document: original,
      backend: "application",
      algorithm: "leastconn",
      servers,
    });
    expect(changed.backends[0]).toMatchObject({
      algorithm: "leastconn",
      servers: [{ weight: 200, address: "2001:db8::1" }, { weight: 100 }],
    });
    expect(original.backends[0]!.servers[0]!.weight).toBe(100);
    expect(changed.backends[1]).toEqual(original.backends[1]);
    expect(changed.backends[1]).not.toBe(original.backends[1]);
  });
  it("rejects invalid numbers, duplicate names, CLI injection and removing every available server", () => {
    for (const patch of [
      { port: "0" },
      { port: "65536" },
      { weight: "257" },
      { weight: "1.5" },
      { name: "app-a\nshutdown" },
      { address: "backend; reload" },
      { name: "app-b" },
    ]) {
      const servers = configurationDraft(document.backends[0]!);
      Object.assign(servers[0]!, patch);
      expect(() =>
        documentFromEdit({
          document,
          backend: "application",
          algorithm: "roundrobin",
          servers,
        }),
      ).toThrow();
    }
    const servers = configurationDraft(document.backends[0]!).map((s) => ({
      ...s,
      maintenance: true,
    }));
    expect(() =>
      documentFromEdit({
        document,
        backend: "application",
        algorithm: "roundrobin",
        servers,
      }),
    ).toThrow("at least one");
  });
  it("renders persistent settings and recoverable version history without review or approval controls", () => {
    const client = {
      cachedConfiguration: () => view(),
    } as unknown as HAProxyClient;
    const html = renderToStaticMarkup(
      <HAProxyConfiguration nodeId="node-a" client={client} />,
    );
    expect(html).toContain("Persistent configuration");
    expect(html).toContain("Version history");
    expect(html).toContain("Version 1");
    expect(html).toContain("Current");
    expect(html).toContain("Editing enabled");
    expect(html).not.toMatch(/Approve|Review configuration|Adopt/);
  });
  it("shows live settings and explains when automatic history reaches its limit", () => {
    const data = view();
    data.control.configuration_history_limited = true;
    const client = {
      cachedConfiguration: () => data,
    } as unknown as HAProxyClient;
    const html = renderToStaticMarkup(
      <HAProxyConfiguration nodeId="node-a" client={client} />,
    );
    expect(html).toContain("Automatic configuration history is full");
    expect(html).toContain("Latest observation");
    expect(html).toContain("Editing enabled");
    expect(html).toContain("Further external changes will not be added");
  });
});
