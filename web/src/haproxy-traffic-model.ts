import { canonicalPrefix, geoLogFormat } from "./haproxy-geo-setup-model";
import type { HAProxyRequestFilters } from "./haproxy-requests-api";

export interface TrafficRule {
  id: string;
  name: string;
  action: "allow" | "block";
  path_prefix?: string;
  cidrs?: string[];
  countries?: string[];
  asns?: number[];
}
export interface TrafficPolicy {
  version: 1;
  mode: "observe" | "enforce";
  unknown_action: "allow" | "block";
  rules: TrafficRule[];
}
export interface TrafficCapabilities {
  profile: string;
  client_source: "peer" | "xff" | "proxy_protocol";
  country_ready: boolean;
  asn_ready: boolean;
}
export interface TrafficDecision {
  rule_id: string;
  decision: "allow" | "block" | "would_allow" | "would_block";
}
export interface TrafficRuleCounts {
  collection_complete: boolean;
  reporting_nodes: number;
  expected_nodes: number;
  organization_id: string;
  cluster_id: string;
  application_id: string;
  spec_digest: string;
  start_ms: number;
  end_ms: number;
  matches: Array<TrafficDecision & { requests: number }>;
}
// Derive the versioned format from the same capture profile shown by Geo setup.
export const trafficLogFormat =
  geoLogFormat.replace('"cv4 ', '"cv5 ').slice(0, -1) +
  ' %[var(txn.cv_rule)] %[var(txn.cv_decision)]"';

const uuid = /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/;
const path = (s: string) =>
  /^\/[A-Za-z0-9/._~-]{0,127}$/.test(s) &&
  !s.includes("..") &&
  !s.includes("//");
const cidr = (s: string) =>
  s === "0.0.0.0/0" || s === "::/0" || canonicalPrefix(s) === s;
const values = <T>(v: unknown, test: (item: T) => boolean): v is T[] =>
  v === undefined ||
  (Array.isArray(v) &&
    v.length <= 16 &&
    new Set(v).size === v.length &&
    v.every(test));
export function validTrafficPolicy(
  value: unknown,
  prefix: string,
): value is TrafficPolicy | undefined {
  if (value === undefined) return true;
  if (!value || typeof value !== "object") return false;
  const p = value as TrafficPolicy;
  return (
    p.version === 1 &&
    ["observe", "enforce"].includes(p.mode) &&
    ["allow", "block"].includes(p.unknown_action) &&
    Array.isArray(p.rules) &&
    p.rules.length > 0 &&
    p.rules.length <= 8 &&
    new Set(p.rules.map((r) => r?.id)).size === p.rules.length &&
    p.rules.every(
      (r) =>
        r &&
        uuid.test(r.id) &&
        typeof r.name === "string" &&
        r.name.length > 0 &&
        r.name.length <= 64 &&
        r.name.trim() === r.name &&
        !/[\u0000-\u001f\u007f]/.test(r.name) &&
        ["allow", "block"].includes(r.action) &&
        (r.path_prefix === undefined ||
          r.path_prefix === "" ||
          (typeof r.path_prefix === "string" &&
            path(r.path_prefix) &&
            r.path_prefix.startsWith(prefix))) &&
        values<string>(r.cidrs, (s) => typeof s === "string" && cidr(s)) &&
        values<string>(
          r.countries,
          (s) => typeof s === "string" && /^[A-Z]{2}$/.test(s) && s !== "ZZ",
        ) &&
        values<number>(
          r.asns,
          (n) => Number.isInteger(n) && n > 0 && n <= 4294967295,
        ) &&
        (r.cidrs?.length ?? 0) +
          (r.countries?.length ?? 0) +
          (r.asns?.length ?? 0) >
          0,
    )
  );
}
export function validTrafficCapabilities(
  value: unknown,
): value is TrafficCapabilities {
  if (!value || typeof value !== "object") return false;
  const c = value as TrafficCapabilities;
  return (
    typeof c.profile === "string" &&
    /^[a-f0-9]{64}$/.test(c.profile) &&
    ["peer", "xff", "proxy_protocol"].includes(c.client_source) &&
    typeof c.country_ready === "boolean" &&
    typeof c.asn_ready === "boolean"
  );
}
export function validTrafficDecision(value: unknown): value is TrafficDecision {
  if (!value || typeof value !== "object") return false;
  const d = value as TrafficDecision;
  return (
    typeof d.rule_id === "string" &&
    (d.rule_id === "unknown" || uuid.test(d.rule_id)) &&
    ["allow", "block", "would_allow", "would_block"].includes(d.decision)
  );
}
export function trafficRequestFilters(
  search: string,
): Partial<HAProxyRequestFilters> {
  const q = new URLSearchParams(search),
    filters: Partial<HAProxyRequestFilters> = {};
  const rule = q.get("traffic_rule"),
    decision = q.get("traffic_decision"),
    digest = q.get("spec_digest");
  if (rule === "unknown" || (rule && uuid.test(rule)))
    filters.traffic_rule = rule;
  if (
    decision &&
    ["allow", "block", "would_allow", "would_block"].includes(decision)
  )
    filters.traffic_decision = decision as TrafficDecision["decision"];
  if (digest && /^[a-f0-9]{64}$/.test(digest)) filters.spec_digest = digest;
  return filters;
}
export function validTrafficCounts(
  value: unknown,
  org: string,
  cluster: string,
  app: string,
): value is TrafficRuleCounts {
  if (!value || typeof value !== "object") return false;
  const c = value as TrafficRuleCounts;
  return (
    typeof c.collection_complete === "boolean" &&
    Number.isSafeInteger(c.reporting_nodes) &&
    Number.isSafeInteger(c.expected_nodes) &&
    c.reporting_nodes >= 0 &&
    c.expected_nodes >= 0 &&
    c.expected_nodes <= 12 &&
    c.reporting_nodes <= c.expected_nodes &&
    (!c.collection_complete ||
      (c.expected_nodes > 0 && c.reporting_nodes === c.expected_nodes)) &&
    c.organization_id === org &&
    c.cluster_id === cluster &&
    c.application_id === app &&
    typeof c.spec_digest === "string" &&
    /^[a-f0-9]{64}$/.test(c.spec_digest) &&
    Number.isSafeInteger(c.start_ms) &&
    Number.isSafeInteger(c.end_ms) &&
    c.start_ms > 0 &&
    c.end_ms > c.start_ms &&
    c.end_ms - c.start_ms <= 86400000 &&
    Array.isArray(c.matches) &&
    c.matches.length <= 36 &&
    new Set(c.matches.map((r) => r?.rule_id + (r?.decision ?? ""))).size ===
      c.matches.length &&
    c.matches.every(
      (r) =>
        validTrafficDecision(r) &&
        Number.isSafeInteger(r.requests) &&
        r.requests > 0,
    )
  );
}
export type TrafficRuleDraft = Pick<TrafficRule, "id" | "name" | "action"> & {
  path: string;
  cidrs: string;
  countries: string;
  asns: string;
};
export const trafficDraft = (r: TrafficRule): TrafficRuleDraft => ({
  id: r.id,
  name: r.name,
  action: r.action,
  path: r.path_prefix ?? "",
  cidrs: (r.cidrs ?? []).join(", "),
  countries: (r.countries ?? []).join(", "),
  asns: (r.asns ?? []).join(", "),
});
export function trafficPolicyInput(
  mode: TrafficPolicy["mode"],
  unknown: TrafficPolicy["unknown_action"],
  rows: TrafficRuleDraft[],
  prefix: string,
): TrafficPolicy {
  const split = (s: string) => (s.trim() ? s.trim().split(/[\s,]+/) : []);
  const p: TrafficPolicy = {
    version: 1,
    mode,
    unknown_action: unknown,
    rules: rows.map((r) => ({
      id: r.id,
      name: r.name.trim(),
      action: r.action,
      ...(r.path ? { path_prefix: r.path.trim() } : {}),
      cidrs: split(r.cidrs).map((s) =>
        s.includes("/") ? s : s + (s.includes(":") ? "/128" : "/32"),
      ),
      countries: split(r.countries).map((s) => s.toUpperCase()),
      asns: split(r.asns).map((s) =>
        /^[1-9][0-9]*$/.test(s.replace(/^AS/i, ""))
          ? Number(s.replace(/^AS/i, ""))
          : NaN,
      ),
    })),
  };
  if (!validTrafficPolicy(p, prefix))
    throw new Error(
      "Give each rule a name and at least one valid IP/CIDR, country code or ASN. Use distinct canonical networks, at most 16 values per condition and 8 rules. Paths must stay inside this application’s prefix.",
    );
  return p;
}
