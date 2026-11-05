import { describe, expect, it } from "vitest";
import {
  geoSetupEvidence,
  geoSetupPolicy,
  trustedProxyRanges,
} from "./haproxy-geo-setup-model";
import type {
  HAProxyRequest,
  HAProxyRequestPipeline,
} from "./haproxy-requests-api";

describe("trusted client address setup", () => {
  it("accepts explicit canonical IPv4/IPv6 networks and rejects unsafe ACL input", () => {
    expect(trustedProxyRanges("10.20.0.2/32, 2001:db8:20::/64")).toEqual([
      "10.20.0.2/32",
      "2001:db8:20::/64",
    ]);
    for (const text of [
      "",
      "0.0.0.0/0",
      "::/0",
      "10.20.0.1/24",
      "10.020.0.0/24",
      "10.20.0.0/33",
      "10.20.0.0/024",
      "10.20.0.0/24,10.20.0.0/24",
      "2001:db8::1/64",
      "2001:DB8::/32",
      "::ffff:c000:200/120",
      "fe80::1%en0/128",
      "10.20.0.0/24 }\nhttp-request allow",
      "example.test/32",
      Array(65).fill("10.20.0.1/32").join(","),
    ])
      expect(trustedProxyRanges(text), text).toBeNull();
  });
  it("generates conditional PROXY acceptance with peer capture first and one private logger", () => {
    const result = geoSetupPolicy(
      "proxy_protocol",
      "10.20.0.2/32",
      "/etc/geo/country.mmdb",
      "",
    )!;
    expect(JSON.parse(result.policy)).toEqual({
      version: 1,
      mode: "proxy_protocol",
      trusted_proxies: ["10.20.0.2/32"],
      country_database: "/etc/geo/country.mmdb",
    });
    expect(result.frontend).toContain(
      "no log\nlog /run/haproxy/access.sock len 2048 format raw local0",
    );
    expect(result.frontend).toContain(
      "set-var(sess.cv_peer) src\ntcp-request connection expect-proxy layer4 if { var(sess.cv_peer) -m ip 10.20.0.2/32 }",
    );
    expect(result.frontend).not.toContain("accept-proxy");
    expect(geoSetupPolicy("xff", "0.0.0.0/0", "/country.mmdb", "")).toBeNull();
    for (const path of [
      "relative.mmdb",
      "/etc/../key",
      "/etc//geo",
      "/etc/geo\nlog stdout",
      "/etc/geo/",
      "/etc/$(id)",
    ])
      expect(geoSetupPolicy("peer", "", path, "")).toBeNull();
    expect(geoSetupPolicy("peer", "", "", "")).toBeNull();
    expect(
      JSON.parse(
        geoSetupPolicy("peer", "ignored", "", "/etc/asn.mmdb")!.policy,
      ),
    ).not.toHaveProperty("trusted_proxies");
  });
  it("requires fresh traffic from the current profile rather than database readiness alone", () => {
    const profile = "a".repeat(64);
    const p: HAProxyRequestPipeline = {
      at_ms: 100000,
      boot_id: "00000000-0000-0000-0000-000000000001",
      listening: true,
      received: 1,
      ignored: 0,
      rejected: 0,
      dropped: 0,
      unconfirmed: 0,
      geo: {
        mode: "proxy_protocol",
        status: "ready",
        profile,
        country_database: { built_at: 1700000000, sha256: "b".repeat(64) },
      },
    };
    const request = {
      id: p.boot_id + ".0000000000000001",
      at_ms: 99000,
      geo: { status: "non_public", source: "proxy_protocol", profile },
    } as HAProxyRequest;
    const check = (
      records: HAProxyRequest[],
      since = 98000,
      now = 101000,
      fresh = true,
    ) => geoSetupEvidence(p, fresh, "proxy_protocol", records, since, now);
    expect(check([])).toContain("new request");
    expect(check([request])).toBe("Address source observed");
    expect(check([{ ...request, id: "old-boot.0000000000000001" }])).toContain(
      "new request",
    );
    expect(check([request], 100000)).toContain("new request");
    expect(
      check([
        { ...request, geo: { ...request.geo!, profile: "c".repeat(64) } },
      ]),
    ).toContain("new request");
    expect(
      check([{ ...request, geo: { ...request.geo!, source: "xff" } }]),
    ).toContain("new request");
    expect(check([request], 98000, 116000)).toContain("fresh");
    expect(check([request], 98000, 101000, false)).toContain("fresh");
    expect(
      geoSetupEvidence(
        { ...p, geo: { ...p.geo!, status: "database_unavailable" } },
        true,
        "proxy_protocol",
        [request],
        98000,
        101000,
      ),
    ).toContain("database");
  });
});
