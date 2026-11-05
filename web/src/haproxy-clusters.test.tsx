import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import {
  HAProxyClient,
  type HAProxyFleet,
  type HAProxyHistory,
  type HAProxyNode,
} from "./haproxy-api";
import {
  clusterHealth,
  clusterHistoryNodes,
  HAProxyClusters,
  loadClusterHistories,
} from "./haproxy-clusters";
import { readHAProxyRoute, selectedHAProxyCluster } from "./haproxy-navigation";
import { HAProxyWorkspace } from "./haproxy-workspace";

const node = (id: string, cluster = "production"): HAProxyNode => ({
  id,
  name: id,
  cluster_id: cluster,
  organization_id: "qa",
  created_at: "2026-11-02T00:00:00Z",
  telemetry_fresh: true,
  scrape_ok: true,
  metrics_available: true,
  observed_at: new Date().toISOString(),
});
const fleet: HAProxyFleet = {
  clusters: ["production", "staging"].map((id) => ({
    id,
    name: id,
    organization_id: "qa",
    created_at: "2026-11-02T00:00:00Z",
  })),
  nodes: [node("proxy-a"), node("proxy-b", "staging")],
  next_cursor: null,
  partial: false,
};
const record = (id: string): HAProxyHistory => ({
  node_id: id,
  range: "15m",
  start: 0,
  end: 60,
  step_seconds: 15,
  series: [],
});

describe("cluster-first workspace", () => {
  it("requires an explicit selection and makes resource ownership win over stale queries", () => {
    expect(
      selectedHAProxyCluster(readHAProxyRoute("/"), null, fleet.nodes),
    ).toBeNull();
    expect(
      selectedHAProxyCluster(readHAProxyRoute("/activity"), "all", fleet.nodes),
    ).toBeNull();
    expect(
      selectedHAProxyCluster(
        readHAProxyRoute("/activity"),
        "staging",
        fleet.nodes,
      ),
    ).toBe("staging");
    expect(
      selectedHAProxyCluster(
        readHAProxyRoute("/applications/production/shop/traffic"),
        "staging",
        fleet.nodes,
      ),
    ).toBe("production");
    expect(
      selectedHAProxyCluster(
        readHAProxyRoute("/infrastructure/nodes/proxy-a"),
        "staging",
        fleet.nodes,
      ),
    ).toBe("production");
    expect(
      selectedHAProxyCluster(
        readHAProxyRoute("/infrastructure/nodes/not-loaded"),
        "staging",
        fleet.nodes,
      ),
    ).toBeNull();
    expect(
      selectedHAProxyCluster(
        readHAProxyRoute("/infrastructure/clusters/production"),
        "staging",
        fleet.nodes,
      ),
    ).toBe("production");
  });
  it("opens the cluster table first, scopes subsequent sections, and keeps installation settings independent", () => {
    const client = new HAProxyClient("qa");
    const render = (path: string) =>
      renderToStaticMarkup(
        <MemoryRouter initialEntries={[path]}>
          <HAProxyWorkspace
            basePath=""
            fleet={fleet}
            client={client}
            loading={false}
            status={null}
            settingsContent={<p>Local sign-in settings</p>}
            onRefresh={() => {}}
            onCreateCluster={() => {}}
            onEnroll={() => {}}
            onRevoke={() => {}}
            onLoadMore={async () => {}}
          />
        </MemoryRouter>,
      );
    try {
      const index = render("/");
      expect(index).toContain('aria-label="HAProxy clusters"');
      expect(index).toContain("/overview?cluster=production");
      expect(index).not.toContain('aria-label="Cluster navigation"');
      expect(index).not.toContain(">All clusters<");
      const infrastructure = render(
        "/infrastructure?cluster=production&range=6h",
      );
      expect(infrastructure).toContain(
        "/applications?cluster=production&amp;range=6h",
      );
      expect(infrastructure).toContain("proxy-a");
      expect(infrastructure).not.toContain("proxy-b");
      expect(infrastructure).not.toContain('aria-label="HAProxy clusters"');
      const unknown = render("/infrastructure?cluster=deleted");
      expect(unknown).toContain("Cluster not found");
      expect(unknown).not.toContain("proxy-a");
      const settings = render("/settings");
      expect(settings).toContain("Installation settings");
      expect(settings).toContain("Local sign-in settings");
      expect(settings).not.toContain('aria-label="Cluster navigation"');
    } finally {
      client.dispose();
    }
  });
  it("does not report complete healthy inventory when nodes are still unloaded", () => {
    expect(clusterHealth(fleet.nodes, false).id).toBe("reporting");
    expect(
      clusterHealth(
        [
          {
            ...node("between-refreshes"),
            observed_at: new Date(Date.now() - 31_000).toISOString(),
          },
        ],
        false,
      ).id,
    ).toBe("reporting");
    expect(
      clusterHealth(
        [
          {
            ...node("refresh-missed"),
            observed_at: new Date(Date.now() - 46_000).toISOString(),
          },
        ],
        false,
      ).id,
    ).toBe("attention");
    expect(clusterHealth(fleet.nodes, true).id).toBe("incomplete");
    expect(clusterHealth([], true).id).toBe("incomplete");
    expect(clusterHealth([], false).id).toBe("empty");
    expect(
      clusterHealth(
        [{ ...node("stale"), observed_at: "2026-10-01T00:00:00Z" }],
        false,
      ).id,
    ).toBe("attention");
    expect(
      clusterHealth([{ ...node("proxy"), scrape_ok: false }], false).id,
    ).toBe("attention");
    expect(
      clusterHealth(
        [{ ...node("proxy"), revoked_at: "2026-11-02T00:00:00Z" }],
        false,
      ).id,
    ).toBe("empty");
  });
  it("bounds charts to ten visible clusters and twelve active nodes per cluster", () => {
    const clusters = Array.from({ length: 25 }, (_, id) => ({
      ...fleet.clusters[0]!,
      id: `cluster-${id}`,
      name: `Cluster ${id}`,
    }));
    const nodes = clusters.flatMap((cluster) =>
      Array.from({ length: 30 }, (_, id) =>
        node(`${cluster.id}-proxy-${id}`, cluster.id),
      ),
    );
    expect(clusterHistoryNodes(clusters.slice(0, 10), nodes)).toHaveLength(120);
    expect(
      clusterHistoryNodes(clusters.slice(10, 20), nodes).some(
        (node) => node.cluster_id === "cluster-0",
      ),
    ).toBe(false);
    const client = new HAProxyClient("qa");
    try {
      const html = renderToStaticMarkup(
        <HAProxyClusters
          fleet={{ ...fleet, clusters, nodes }}
          client={client}
          href={(id) => "/overview?cluster=" + id}
          onOpen={() => {}}
          query=""
          onQuery={() => {}}
          health="all"
          onHealth={() => {}}
          page={2}
          onPage={() => {}}
        />,
      );
      expect(html).toContain("Cluster 10");
      expect(html).toContain("Cluster 19");
      expect(html).not.toContain("Cluster 20");
      expect(html).toContain("12 loaded nodes, partial");
    } finally {
      client.dispose();
    }
  });
  it("loads four histories at a time, retains successes after one failure and stops on cancellation", async () => {
    let active = 0,
      maximum = 0;
    const history = vi.fn(async (id: string) => {
      active++;
      maximum = Math.max(maximum, active);
      await Promise.resolve();
      active--;
      if (id === "bad") throw new Error("unavailable");
      return record(id);
    });
    const result = await loadClusterHistories(
      { history },
      ["a", "b", "bad", "d", "e", "f", "g", "h", "i"],
      new AbortController().signal,
    );
    expect(maximum).toBe(4);
    expect(result.failed).toBe(true);
    expect(Object.keys(result.histories)).toHaveLength(8);
    const controller = new AbortController();
    const stopped = vi.fn(async (id: string) => {
      controller.abort();
      return record(id);
    });
    const cancelled = await loadClusterHistories(
      { history: stopped },
      ["a", "b", "c", "d", "e"],
      controller.signal,
    );
    expect(stopped).toHaveBeenCalledTimes(4);
    expect(cancelled.histories).toEqual({});
    expect(cancelled.failed).toBe(false);
  });
});
