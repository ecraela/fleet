import { afterEach, describe, expect, it, vi } from "vitest";
import { HAProxyClient } from "./haproxy-api";
const org = "11111111-1111-4111-8111-111111111111",
  cluster = "22222222-2222-4222-8222-222222222222",
  app = "33333333-3333-4333-8333-333333333333";
const filters = { range: "15m" as const };
const page = {
  organization_id: org,
  cluster_id: cluster,
  application_id: app,
  start_ms: 1000,
  end_ms: 2000,
  requests: [],
  next_cursor: "",
};
afterEach(() => vi.unstubAllGlobals());
describe("request navigation cache", () => {
  it("does not repopulate a disposed session from a late response", async () => {
    let finish: (response: Response) => void = () => {};
    const fetch = vi.fn().mockImplementation(
      () =>
        new Promise<Response>((resolve) => {
          finish = resolve;
        }),
    );
    vi.stubGlobal("fetch", fetch);
    const client = new HAProxyClient(org);
    const pending = client.applicationRequests(cluster, app, filters);
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
    client.dispose();
    finish(new Response(JSON.stringify(page)));
    await pending.catch(() => {});
    expect(client.cachedRequests(cluster, app, filters)).toBeUndefined();
  });
  it("retains validated results by full resource/filter/cursor and clears them on logout", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockImplementation(() =>
          Promise.resolve(new Response(JSON.stringify(page))),
        ),
    );
    const client = new HAProxyClient(org),
      other = new HAProxyClient("another-organization");
    try {
      await client.applicationRequests(cluster, app, filters);
      expect(client.cachedRequests(cluster, app, filters)).toEqual(page);
      expect(other.cachedRequests(cluster, app, filters)).toBeUndefined();
      expect(
        client.cachedRequests(cluster, "different-app", filters),
      ).toBeUndefined();
      expect(
        client.cachedRequests(cluster, app, { ...filters, path: "/private" }),
      ).toBeUndefined();
      expect(
        client.cachedRequests(cluster, app, filters, 2000, "older"),
      ).toBeUndefined();
      for (let i = 0; i < 13; i++)
        await client.applicationRequests(cluster, app, {
          ...filters,
          path: `/shop/${i}`,
        });
      expect(client.cachedRequests(cluster, app, filters)).toBeUndefined();
      expect(
        client.cachedRequests(cluster, app, { ...filters, path: "/shop/12" }),
      ).toEqual(page);
      client.dispose();
      expect(
        client.cachedRequests(cluster, app, { ...filters, path: "/shop/12" }),
      ).toBeUndefined();
    } finally {
      client.dispose();
      other.dispose();
    }
  });
  it("keeps last loaded data when a refresh fails and rejects responses from a different tenant", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce(new Response(JSON.stringify(page)))
        .mockResolvedValueOnce(
          new Response(JSON.stringify({ ...page, organization_id: "other" })),
        )
        .mockResolvedValueOnce(new Response("", { status: 503 })),
    );
    const client = new HAProxyClient(org);
    try {
      await client.applicationRequests(cluster, app, filters);
      await expect(
        client.applicationRequests(cluster, app, filters),
      ).rejects.toThrow("invalid request search");
      await expect(
        client.applicationRequests(cluster, app, filters),
      ).rejects.toThrow();
      expect(client.cachedRequests(cluster, app, filters)).toEqual(page);
    } finally {
      client.dispose();
    }
  });
});
