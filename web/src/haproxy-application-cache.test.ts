import { afterEach, describe, expect, it, vi } from "vitest";
import { HAProxyClient, type HAProxyApplicationRevision } from "./haproxy-api";
const org = "11111111-1111-4111-8111-111111111111",
  cluster = "22222222-2222-4222-8222-222222222222",
  app = "33333333-3333-4333-8333-333333333333";
const revision: HAProxyApplicationRevision = {
  id: "revision",
  organization_id: org,
  cluster_id: cluster,
  application_id: app,
  number: 1,
  request_id: "request",
  rollout_id: "rollout",
  created_at: "2026-10-28T00:00:00Z",
  status: "succeeded",
  spec: {
    id: app,
    name: "Store",
    listener: "public",
    hostname: "shop.example.test",
    path_prefix: "/shop",
    health_path: "/healthz",
    algorithm: "roundrobin",
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
};
afterEach(() => vi.unstubAllGlobals());
describe("application navigation cache", () => {
  it("keeps validated records between pages and clears them with the client session", async () => {
    const fetch = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ revisions: [revision] }), {
        status: 200,
      }),
    );
    vi.stubGlobal("fetch", fetch);
    const client = new HAProxyClient(org),
      other = new HAProxyClient("another-organization");
    try {
      await client.applications(cluster);
      expect(client.cachedApplications(cluster)).toEqual([revision]);
      expect(other.cachedApplications(cluster)).toBeUndefined();
      const options = fetch.mock.calls[0]![1] as RequestInit;
      expect(new Headers(options.headers).get("X-Organization-ID")).toBe(org);
      client.dispose();
      expect(client.cachedApplications(cluster)).toBeUndefined();
    } finally {
      client.dispose();
      other.dispose();
    }
  });
  it("does not replace cached data with a response from another tenant or cluster", async () => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ revisions: [revision] }), {
          status: 200,
        }),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            revisions: [{ ...revision, organization_id: "other" }],
          }),
          { status: 200 },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ revisions: [{ ...revision, cluster_id: "other" }] }),
          { status: 200 },
        ),
      );
    vi.stubGlobal("fetch", fetch);
    const client = new HAProxyClient(org);
    try {
      await client.applications(cluster);
      await expect(client.applications(cluster)).rejects.toThrow(
        "invalid application revision",
      );
      await expect(client.applications(cluster)).rejects.toThrow(
        "invalid application revision",
      );
      expect(client.cachedApplications(cluster)).toEqual([revision]);
    } finally {
      client.dispose();
    }
  });
});
