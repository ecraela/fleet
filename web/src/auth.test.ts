import { afterEach, describe, expect, it, vi } from "vitest";
import { localRequest, LocalAuthError } from "./auth";
import { HAProxyClient, HAProxyError } from "./haproxy-api";
const org = "00000000-0000-0000-0000-000000000001";
const session = {
  user_id: "00000000-0000-0000-0000-000000000002",
  username: "local-admin",
  organization_id: org,
  role: "administrator" as const,
  capabilities: ["haproxy.read", "haproxy.manage"],
  csrf_token: "synthetic-csrf",
  expires_at: new Date(Date.now() + 3600000).toISOString(),
};
afterEach(() => vi.unstubAllGlobals());
describe("standalone authentication", () => {
  it("uses same-origin cookies and a local session, without a Converge handoff", async () => {
    const fetcher = vi
      .fn()
      .mockImplementation(
        async () => new Response(JSON.stringify(session), { status: 200 }),
      );
    vi.stubGlobal("fetch", fetcher);
    const client = new HAProxyClient(org);
    try {
      await client.connect();
      await client.refresh();
      await client.login(true);
    } finally {
      client.dispose();
    }
    expect(fetcher).toHaveBeenCalledTimes(3);
    for (const [url, init] of fetcher.mock.calls) {
      expect(url).toBe("/api/haproxy/v1/local/session");
      expect(init.credentials).toBe("same-origin");
      expect(new Headers(init.headers).get("X-HAProxy-Session")).toBe("local");
      expect(init.body).toBeUndefined();
    }
  });
  it("rejects a different tenant and read-only management", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockImplementation(
          async () =>
            new Response(
              JSON.stringify({ ...session, organization_id: "other" }),
            ),
        ),
    );
    const mismatch = new HAProxyClient(org);
    try {
      await expect(mismatch.connect()).rejects.toBeInstanceOf(HAProxyError);
    } finally {
      mismatch.dispose();
    }
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockImplementation(
          async () =>
            new Response(
              JSON.stringify({ ...session, capabilities: ["haproxy.read"] }),
            ),
        ),
    );
    const reader = new HAProxyClient(org);
    try {
      await reader.connect();
      await expect(reader.login(true)).rejects.toMatchObject({ status: 403 });
    } finally {
      reader.dispose();
    }
  });
  it("protects password changes and logout with the current in-memory CSRF token", async () => {
    const fetcher = vi
      .fn()
      .mockImplementation(async () => new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetcher);
    await localRequest(
      "password",
      {
        method: "POST",
        body: JSON.stringify({
          current_password: "synthetic password",
          new_password: "replacement password",
        }),
      },
      session,
    );
    const [url, init] = fetcher.mock.calls[0]!;
    expect(url).toBe("/api/haproxy/v1/local/password");
    expect(new Headers(init.headers).get("X-CSRF-Token")).toBe(
      session.csrf_token,
    );
    expect(init.credentials).toBe("same-origin");
    expect(init.redirect).toBe("error");
  });
  it("reports authentication failures without replaying a mutation", async () => {
    const fetcher = vi
      .fn()
      .mockImplementation(
        async () =>
          new Response(
            JSON.stringify({ error: "username or password is incorrect" }),
            { status: 401 },
          ),
      );
    vi.stubGlobal("fetch", fetcher);
    await expect(
      localRequest("session", { method: "POST", body: "{}" }),
    ).rejects.toBeInstanceOf(LocalAuthError);
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
});
