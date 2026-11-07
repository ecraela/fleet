import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router-dom";
import {
  haproxySections,
  haproxyPath,
  readHAProxyRoute,
  historyRange,
  clusterTabs,
} from "./haproxy-navigation";
import { HAProxyWorkspace } from "./haproxy-workspace";
import { HAProxyClient } from "./haproxy-api";

vi.mock("./mfa", () => ({
  useMfaGate: () => ({ runSensitive: vi.fn() }),
  isMfaFlowCancelled: () => false,
}));

const org = "11111111-1111-4111-8111-111111111111";
const cluster = "22222222-2222-4222-8222-222222222222";
const node = "33333333-3333-4333-8333-333333333333";
describe("HAProxy workspace routes", () => {
  for (const base of ["", `/organizations/${org}/haproxy`]) {
    it(`keeps the same section and detail model at ${base || "standalone root"}`, () => {
      for (const section of haproxySections)
        expect(readHAProxyRoute(haproxyPath(base, [section.id]), base)).toEqual(
          { section: section.id, kind: "list" },
        );
      expect(
        readHAProxyRoute(
          haproxyPath(base, ["applications", cluster, "app", "traffic"]),
          base,
        ),
      ).toMatchObject({
        section: "applications",
        kind: "application",
        clusterId: cluster,
        id: "app",
        tab: "traffic",
      });
      expect(
        readHAProxyRoute(
          haproxyPath(base, ["infrastructure", "nodes", node, "configuration"]),
          base,
        ),
      ).toMatchObject({ kind: "node", id: node, tab: "configuration" });
      expect(
        readHAProxyRoute(
          haproxyPath(base, ["infrastructure", "clusters", cluster, "tls"]),
          base,
        ),
      ).toMatchObject({ kind: "cluster", id: cluster, tab: "tls" });
      expect(readHAProxyRoute(base + "/", base)).toEqual({
        kind: "list",
        section: "overview",
      });
    });
  }
  it("rejects malformed or unsupported links without switching tenant or interpreting encoded separators", () => {
    for (const path of [
      "/applications/c/a/unknown",
      "/applications/%zz/a",
      "/infrastructure/nodes/a%2Fb",
      "/settings/extra",
      "/elsewhere",
    ])
      expect(readHAProxyRoute(path).kind).toBe("not-found");
    expect(
      readHAProxyRoute(
        "/organizations/other/haproxy",
        "/organizations/local/haproxy",
      ).kind,
    ).toBe("not-found");
    expect(readHAProxyRoute("/login")).toEqual({
      kind: "list",
      section: "overview",
    });
    expect(historyRange("unbounded")).toBe("15m");
    expect(historyRange("6h")).toBe("6h");
  });
  it("renders real resource links while keeping cluster/time filters and putting nodes on their own page", () => {
    const client = new HAProxyClient(org);
    try {
      const fleet = {
        clusters: [
          {
            id: cluster,
            organization_id: org,
            name: "Production",
            created_at: "2026-10-28T00:00:00Z",
          },
        ],
        nodes: [
          {
            id: node,
            organization_id: org,
            cluster_id: cluster,
            name: "proxy-a",
            created_at: "2026-10-28T00:00:00Z",
            observed_at: null,
            revoked_at: null,
            telemetry_fresh: false,
            scrape_ok: false,
            metrics_available: false,
            agent_fresh: false,
          },
        ],
        next_cursor: null,
        partial: false,
      };
      const render = (path: string) =>
        renderToStaticMarkup(
          <MemoryRouter initialEntries={[path]}>
            <HAProxyWorkspace
              basePath=""
              fleet={fleet}
              client={client}
              loading={false}
              status={null}
              onRefresh={() => {}}
              onCreateCluster={() => {}}
              onEnroll={() => {}}
              onRevoke={() => {}}
              onLoadMore={async () => {}}
            />
          </MemoryRouter>,
        );
      const search = `?cluster=${cluster}&range=6h&node_query=proxy-a`;
      const html = render("/infrastructure" + search);
      expect(html).toContain('aria-label="Cluster navigation"');
      expect(html).toContain(
        `/infrastructure/nodes/${node}/metrics?cluster=${cluster}&amp;range=6h&amp;node_query=proxy-a`,
      );
      expect(html).toContain(
        `/applications?cluster=${cluster}&amp;range=6h&amp;node_query=proxy-a`,
      );
      expect(html).not.toContain("Backend latency averages");
      expect(html).not.toContain("Application deployments");
      const sections = (markup: string) =>
        markup.match(
          /<nav aria-label="Infrastructure sections"[\s\S]*?<\/nav>/,
        )?.[0];
      expect(sections(html)).toMatch(
        /aria-current="page"[^>]*><span>Nodes<\/span>/,
      );
      expect(html).toContain('aria-label="Find nodes"');
      expect(html).not.toContain('class="cv-field__label"');
      expect(html).not.toContain('aria-label="Cluster configuration"');
      for (const tab of clusterTabs) {
        const view = render(
          `/infrastructure/clusters/${cluster}/${tab}${search}`,
        );
        const tabs = sections(view);
        const label =
          tab === "tls"
            ? "Automatic TLS"
            : tab[0]!.toUpperCase() + tab.slice(1);
        expect(view).toContain(
          '<h1 class="cv-page-header__title">Infrastructure</h1>',
        );
        expect(tabs).toContain(
          `/infrastructure/clusters/${cluster}/nodes?cluster=${cluster}&amp;range=6h&amp;node_query=proxy-a`,
        );
        expect(tabs).toMatch(
          new RegExp(`aria-current="page"[^>]*><span>${label}</span>`),
        );
        for (const name of [
          "Nodes",
          "Topology",
          "Listeners",
          "Automatic TLS",
          "Rollouts",
        ])
          expect(tabs).toContain(`<span>${name}</span>`);
      }
      expect(
        sections(render(`/infrastructure/nodes/${node}${search}`)),
      ).toContain("<span>Nodes</span>");
      const breadcrumb = (markup: string) =>
        markup.match(/<nav aria-label="Breadcrumb"[\s\S]*?<\/nav>/)?.[0] ?? "";
      for (const section of haproxySections) {
        const trail = breadcrumb(render(`/${section.id}${search}`));
        expect(trail).toContain(`title="${section.label}"`);
        expect(trail).toContain('title="Production"');
      }
      for (const tab of clusterTabs) {
        const path = `/infrastructure/clusters/${cluster}/${tab}${search}`;
        const trail = breadcrumb(render(path));
        const label =
          tab === "tls"
            ? "Automatic TLS"
            : tab[0]!.toUpperCase() + tab.slice(1);
        expect(trail).toContain('title="Infrastructure"');
        expect(trail).toContain(`aria-current="page" title="${label}"`);
      }
      const nodes = render(
        `/infrastructure/clusters/${cluster}/nodes${search}`,
      );
      expect(nodes).toContain('aria-label="Find nodes"');
      expect(nodes).toContain("Enroll node");
      const nodeTrail = breadcrumb(
        render(`/infrastructure/nodes/${node}/runtime${search}`),
      );
      for (const label of ["Infrastructure", "Nodes", "proxy-a", "Runtime"])
        expect(nodeTrail).toContain(`title="${label}"`);
      vi.spyOn(client, "cachedApplications").mockReturnValue([
        {
          id: "revision",
          organization_id: org,
          cluster_id: cluster,
          application_id: "app",
          number: 1,
          request_id: "request",
          rollout_id: "rollout",
          created_at: "2026-10-28T00:00:00Z",
          status: "succeeded",
          spec: {
            id: "app",
            name: "Admin demo",
            hostname: "apps.demo.test",
            path_prefix: "/",
            listener: "public",
            algorithm: "roundrobin",
            health_path: "/healthz",
            servers: [],
          },
        },
      ]);
      const application = render(
        `/applications/${cluster}/app/traffic${search}`,
      );
      const appTrail = breadcrumb(application);
      for (const label of ["Applications", "Admin demo", "Traffic"])
        expect(appTrail).toContain(`title="${label}"`);
      expect(application).toContain(
        `/applications/${cluster}/app/overview?cluster=`,
      );
    } finally {
      client.dispose();
    }
  });
});

it("keeps settings tabs linkable and renders only the selected section", () => {
  const client = new HAProxyClient(org);
  try {
    const render = (search: string) =>
      renderToStaticMarkup(
        <MemoryRouter initialEntries={["/settings" + search]}>
          <HAProxyWorkspace
            basePath=""
            fleet={{
              clusters: [],
              nodes: [],
              next_cursor: null,
              partial: false,
            }}
            client={client}
            loading={false}
            status={null}
            settingsContent={<p>Access settings panel</p>}
            onRefresh={() => {}}
            onCreateCluster={() => {}}
            onEnroll={() => {}}
            onRevoke={() => {}}
            onLoadMore={async () => {}}
          />
        </MemoryRouter>,
      );
    const access = render("");
    expect(access).toContain('aria-label="Settings navigation"');
    expect(access).toContain('href="/settings?settings_tab=software"');
    expect(access).toContain("Access settings panel");
    expect(access).not.toContain("No agent versions yet");
    expect(access).not.toContain("Installation and recovery guide");
    const software = render("?settings_tab=software");
    expect(software).toContain("No agent versions yet");
    expect(software).not.toContain("Access settings panel");
    expect(software).not.toContain("Installation and recovery guide");
    const recovery = render("?settings_tab=recovery");
    expect(recovery).toContain("Installation and recovery guide");
    expect(recovery).toContain(
      "https://github.com/ecraela/fleet/blob/main/docs/self-hosted-installation.md#encrypted-and-authenticated-backup",
    );
    expect(recovery).not.toContain("github.com/ecraela/haproxy-manager");
    expect(recovery).not.toContain("Access settings panel");
    expect(recovery).not.toContain("No agent versions yet");
    expect(render("?settings_tab=unknown")).toContain("Access settings panel");
  } finally {
    client.dispose();
  }
});
