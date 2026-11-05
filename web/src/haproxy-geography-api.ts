import {
  validRequestStats,
  type HAProxyRequestStats,
  type HAProxyRequestFilters,
  type HAProxyRequestPipeline,
} from "./haproxy-requests-api";
import {
  validCountry,
  validCountryFilter,
  validGeoCollection,
} from "./haproxy-geography-model";
const uuid = /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/;
const count = (value: unknown): value is number =>
  Number.isSafeInteger(value) && Number(value) >= 0;
export interface HAProxyGeographyTraffic {
  organization_id: string;
  cluster_id: string;
  application_id: string;
  range: HAProxyRequestFilters["range"];
  start_ms: number;
  end_ms: number;
  summary: HAProxyRequestStats;
  client_source_filter?: boolean;
  country_known: number;
  asn_known: number;
  unresolved: number;
  database_unavailable: number;
  countries: Array<HAProxyRequestStats & { country: string }>;
  asns: Array<HAProxyRequestStats & { asn: number }>;
  more_countries: boolean;
  more_asns: boolean;
  collection: Array<{
    node_id: string;
    name: string;
    fresh: boolean;
    pipeline: HAProxyRequestPipeline | null;
  }>;
}
export function validGeoPipeline(p: HAProxyRequestPipeline): boolean {
  return (
    uuid.test(p.boot_id) &&
    count(p.at_ms) &&
    typeof p.listening === "boolean" &&
    [p.received, p.ignored, p.rejected, p.dropped, p.unconfirmed].every(
      count,
    ) &&
    (p.geo === undefined || validGeoCollection(p.geo))
  );
}
export function validGeographyTraffic(
  value: unknown,
  org: string,
  cluster: string,
  app: string,
): value is HAProxyGeographyTraffic {
  if (!value || typeof value !== "object") return false;
  const g = value as HAProxyGeographyTraffic;
  if (
    g.organization_id !== org ||
    g.cluster_id !== cluster ||
    g.application_id !== app ||
    !["15m", "1h", "6h", "24h"].includes(g.range) ||
    !count(g.start_ms) ||
    !count(g.end_ms) ||
    g.end_ms <= g.start_ms ||
    g.end_ms - g.start_ms > 86400000 ||
    (g.client_source_filter !== undefined &&
      typeof g.client_source_filter !== "boolean") ||
    !validRequestStats(g.summary)
  )
    return false;
  return (
    [g.country_known, g.asn_known, g.unresolved, g.database_unavailable].every(
      (n) => count(n) && n <= g.summary.requests,
    ) &&
    typeof g.more_countries === "boolean" &&
    typeof g.more_asns === "boolean" &&
    Array.isArray(g.countries) &&
    g.countries.length <= 10 &&
    new Set(g.countries.map((c) => c.country)).size === g.countries.length &&
    g.countries.every(
      (c) =>
        c &&
        (c.country === "" || validCountry(c.country)) &&
        validRequestStats(c) &&
        c.requests > 0,
    ) &&
    g.countries.reduce((n, c) => n + c.requests, 0) <= g.summary.requests &&
    Array.isArray(g.asns) &&
    g.asns.length <= 10 &&
    new Set(g.asns.map((c) => c.asn)).size === g.asns.length &&
    g.asns.every(
      (c) =>
        c &&
        count(c.asn) &&
        c.asn <= 4294967295 &&
        validRequestStats(c) &&
        c.requests > 0,
    ) &&
    g.asns.reduce((n, c) => n + c.requests, 0) <= g.summary.requests &&
    Array.isArray(g.collection) &&
    g.collection.length <= 12 &&
    new Set(g.collection.map((c) => c.node_id)).size === g.collection.length &&
    g.collection.every(
      (c) =>
        c &&
        uuid.test(c.node_id) &&
        typeof c.name === "string" &&
        c.name.length <= 255 &&
        typeof c.fresh === "boolean" &&
        (c.pipeline === null || (!!c.pipeline && validGeoPipeline(c.pipeline))),
    )
  );
}
export function geographyRequestFilters(search: string): HAProxyRequestFilters {
  const q = new URLSearchParams(search),
    filters: HAProxyRequestFilters = { range: "15m" };
  const range = q.get("range"),
    country = q.get("country"),
    asn = q.get("asn"),
    node = q.get("node"),
    scheme = q.get("scheme");
  if (["15m", "1h", "6h", "24h"].includes(range ?? ""))
    filters.range = range as HAProxyRequestFilters["range"];
  if (country && validCountryFilter(country))
    filters.country = country.split(",").sort().join(",");
  if (asn && /^(0|[1-9][0-9]{0,9})$/.test(asn) && Number(asn) <= 4294967295)
    filters.asn = asn;
  if (node && uuid.test(node)) filters.node = node;
  if (scheme === "http" || scheme === "https") filters.scheme = scheme;
  return filters;
}
