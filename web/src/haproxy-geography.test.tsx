import { describe, it, expect } from "vitest";
import {
  validGeographyTraffic,
  geographyRequestFilters,
  type HAProxyGeographyTraffic,
} from "./haproxy-geography-api";
import { validGeography, validGeoCollection } from "./haproxy-geography-model";
import {
  geographyRequestHref,
  geographyStatus,
  countryLabel,
  networkLabel,
} from "./haproxy-geography";

const org = "00000000-0000-0000-0000-000000000001",
  cluster = "00000000-0000-0000-0000-000000000002",
  app = "00000000-0000-0000-0000-000000000003",
  profile = "a".repeat(64);
const stats = {
  requests: 2,
  classes: { "2xx": 1, "5xx": 1 },
  bytes: 10,
  mean_ms: 20,
  p50_ms: 10,
  p95_ms: 30,
  p99_ms: 30,
};
function view(): HAProxyGeographyTraffic {
  return {
    organization_id: org,
    cluster_id: cluster,
    application_id: app,
    range: "15m",
    start_ms: 1000000,
    end_ms: 1900000,
    summary: stats,
    country_known: 2,
    asn_known: 2,
    unresolved: 0,
    database_unavailable: 0,
    countries: [{ ...stats, country: "GB" }],
    asns: [{ ...stats, asn: 15169 }],
    more_countries: false,
    more_asns: false,
    collection: [],
  };
}
describe("node-local geography contracts", () => {
  it("rejects foreign resources and malformed/overcounted aggregates", () => {
    expect(validGeographyTraffic(view(), org, cluster, app)).toBe(true);
    for (const mutate of [
      (v: HAProxyGeographyTraffic) => {
        v.organization_id = app;
      },
      (v: HAProxyGeographyTraffic) => {
        v.country_known = 3;
      },
      (v: HAProxyGeographyTraffic) => {
        v.countries[0]!.country = "client-ip";
      },
      (v: HAProxyGeographyTraffic) => {
        v.asns[0]!.asn = 4294967296;
      },
      (v: HAProxyGeographyTraffic) => {
        v.asns.push(v.asns[0]!);
      },
      (v: HAProxyGeographyTraffic) => {
        v.countries[0]!.requests = 3;
        v.countries[0]!.classes = { "2xx": 2, "5xx": 1 };
      },
    ]) {
      const v = structuredClone(view());
      mutate(v);
      expect(validGeographyTraffic(v, org, cluster, app)).toBe(false);
    }
  });
  it("keeps unknowns explicit and refuses address-shaped extras", () => {
    expect(
      validGeography({
        status: "partial",
        source: "xff",
        country: "GB",
        profile,
      }),
    ).toBe(true);
    expect(
      validGeography({ status: "unresolved", source: "unknown", profile }),
    ).toBe(true);
    expect(validGeography({ status: "disabled", source: "unknown" })).toBe(
      true,
    );
    expect(
      validGeography({
        status: "located",
        source: "xff",
        country: "GB",
        profile,
      }),
    ).toBe(false);
    expect(
      validGeography({
        status: "unresolved",
        source: "unknown",
        country: "GB",
        profile,
      }),
    ).toBe(false);
    expect(
      validGeography({
        status: "partial",
        source: "xff",
        country: "GB",
        profile,
        client_ip: "81.2.69.160",
      }),
    ).toBe(false);
    expect(validGeoCollection({ mode: "xff", status: "ready", profile })).toBe(
      false,
    );
    expect(
      validGeoCollection({
        mode: "xff",
        status: "database_unavailable",
        profile,
      }),
    ).toBe(true);
    expect(geographyStatus()).toBe("Not collected");
    expect(networkLabel()).toBe("Unknown network");
    expect(countryLabel()).toBe("Unknown country");
  });
  it("drills into the same app with typed country/ASN filters", () => {
    const href = geographyRequestHref(
      `/applications/${cluster}/${app}/traffic?cluster=${cluster}`,
      { range: "1h", country: "GB", asn: "15169" },
    );
    const url = new URL(href, "http://localhost");
    expect(url.pathname).toContain(app);
    expect(url.searchParams.get("cluster")).toBe(cluster);
    expect(geographyRequestFilters(url.search)).toEqual({
      range: "1h",
      country: "GB",
      asn: "15169",
    });
    expect(
      geographyRequestFilters(
        "?country=GB%7Cstats&asn=-1&node=foreign&range=all",
      ),
    ).toEqual({ range: "15m" });
    expect(geographyRequestFilters("?country=unknown&asn=0")).toEqual({
      range: "15m",
      country: "unknown",
      asn: "0",
    });
    expect(geographyRequestFilters("?country=unknown,US,GB")).toEqual({
      range: "15m",
      country: "GB,US,unknown",
    });
    expect(geographyRequestFilters("?country=GB,GB")).toEqual({ range: "15m" });
  });
});
