import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router-dom";
import { HAProxyApplicationDetails } from "./haproxy-application-details";
import type { HAProxyApplicationRevision } from "./haproxy-api";

describe("application detail pages", () => {
  it("bounds long deployment history and keeps permissions visible on the resource", () => {
    const revisions: HAProxyApplicationRevision[] = Array.from(
      { length: 34 },
      (_, index) => ({
        id: `revision-${index + 1}`,
        organization_id: "tenant",
        cluster_id: "cluster",
        application_id: "store",
        number: index + 1,
        request_id: "request",
        rollout_id: "rollout",
        created_at: "2026-10-28T00:00:00Z",
        status: "succeeded",
        spec: {
          id: "store",
          name: "Store",
          hostname: "shop.example.test",
          path_prefix: "/shop",
          listener: "public",
          algorithm: "roundrobin",
          health_path: "/healthz",
          servers: [
            {
              name: "web",
              address: "backend.internal",
              port: 8080,
              weight: 100,
              maintenance: false,
            },
          ],
        },
      }),
    );
    const html = renderToStaticMarkup(
      <MemoryRouter>
        <HAProxyApplicationDetails
          revision={revisions.at(-1)!}
          revisions={revisions}
          now={Date.now()}
          tab="deployments"
          links={[]}
          permissionReason="Your role is read-only."
          onClose={() => {}}
          onEdit={() => {}}
          onHistory={() => {}}
          onRestoreVersion={() => {}}
          onLifecycle={() => {}}
        />
      </MemoryRouter>,
    );
    expect(html).toContain("Version 34");
    expect(html).toContain("Version 25");
    expect(html).not.toContain("Version 24");
    expect(html).toContain("34 versions");
    expect(html).toContain("Your role is read-only.");
    expect(html).toContain("Loaded revisions");
    expect(html).toContain('aria-label="Actions for Store"');
    expect(html).not.toContain("Application operations");
    expect(html).not.toContain("Version history and rollback");
  });
});
