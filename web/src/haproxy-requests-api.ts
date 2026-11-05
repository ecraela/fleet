import {
  validGeography,
  validGeoCollection,
  type HAProxyGeography,
  type HAProxyGeoCollection,
} from "./haproxy-geography-model";
import {
  validTrafficDecision,
  type TrafficDecision,
} from "./haproxy-traffic-model";
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const digest = /^[0-9a-f]{64}$/;
const eventId = /^[0-9a-f-]{36}\.[0-9a-f]{16}$/;
const count = (v: unknown): v is number =>
  Number.isSafeInteger(v) && Number(v) >= 0;
const duration = (v: unknown): v is number =>
  typeof v === "number" && Number.isFinite(v) && v >= 0 && v <= 86400000;
export interface HAProxyRequestFilters {
  traffic_rule?: string;
  traffic_decision?: TrafficDecision["decision"];
  range: "15m" | "1h" | "6h" | "24h";
  node?: string;
  status_class?: string;
  scheme?: string;
  method?: string;
  min_ms?: number;
  spec_digest?: string;
  request_id?: string;
  path?: string;
  client_source?: "peer" | "xff" | "proxy_protocol" | "unknown";
  country?: string;
  asn?: string;
  path_order?: "requests" | "errors" | "latency";
}
export interface HAProxyRequestStats {
  requests: number;
  classes: Record<string, number>;
  bytes: number;
  mean_ms: number | null;
  p50_ms: number | null;
  p95_ms: number | null;
  p99_ms: number | null;
}
export interface HAProxyRequestPipeline {
  boot_id: string;
  at_ms: number;
  listening: boolean;
  received: number;
  ignored: number;
  rejected: number;
  dropped: number;
  unconfirmed: number;
  path_mode?: "off" | "sanitized" | "exact";
  path_policy?: string;
  geo?: HAProxyGeoCollection;
}
export interface HAProxyRequestTraffic {
  organization_id: string;
  cluster_id: string;
  application_id: string;
  range: HAProxyRequestFilters["range"];
  start_ms: number;
  end_ms: number;
  step_seconds: number;
  summary: HAProxyRequestStats;
  points: Array<HAProxyRequestStats & { at: number }>;
  collection: Array<{
    node_id: string;
    name: string;
    fresh: boolean;
    pipeline: HAProxyRequestPipeline | null;
  }>;
  paths?: Array<HAProxyRequestStats & { path: string; path_kind: string }>;
  path_order?: "requests" | "errors" | "latency";
  more_paths?: boolean;
}
export interface HAProxyRequest {
  traffic?: TrafficDecision;
  geo?: HAProxyGeography;
  id: string;
  at_ms: number;
  application_id: string;
  node_id: string;
  spec_digest: string;
  matching_versions: number[];
  server: string;
  method: string;
  scheme: "http" | "https";
  status: number;
  bytes: number;
  duration_ms: number;
  response_ms: number;
  connect_ms: number;
  queue_ms: number;
  termination: string;
  path?: string;
  path_kind?: string;
}

export function validRequestPath(path: unknown): path is string {
  return (
    typeof path === "string" &&
    path.length > 0 &&
    path.length <= 512 &&
    path.startsWith("/") &&
    !path.includes("//") &&
    !/[\x00-\x20\x7f-\uffff?#%\\"'<>|]/.test(path) &&
    !path.split("/").some((p) => p === "." || p === "..")
  );
}

function validPathEvidence(path: unknown, kind: unknown): boolean {
  if (kind === "template" || kind === "exact") return validRequestPath(path);
  return (
    [undefined, "", "unavailable", "withheld", "unmatched", "invalid"].includes(
      kind as string | undefined,
    ) &&
    (path === undefined || path === "")
  );
}
export interface HAProxyRequestPage {
  organization_id: string;
  cluster_id: string;
  application_id: string;
  start_ms: number;
  end_ms: number;
  requests: HAProxyRequest[];
  next_cursor: string;
}
function scope(
  v: unknown,
  org: string,
  cluster: string,
  app: string,
): v is HAProxyRequestPage {
  if (!v || typeof v !== "object") return false;
  const r = v as HAProxyRequestPage;
  return (
    r.organization_id === org &&
    r.cluster_id === cluster &&
    r.application_id === app &&
    count(r.start_ms) &&
    count(r.end_ms) &&
    r.end_ms > r.start_ms &&
    r.end_ms - r.start_ms <= 86400000
  );
}
export function validRequestStats(v: unknown): v is HAProxyRequestStats {
  if (!v || typeof v !== "object") return false;
  const s = v as HAProxyRequestStats;
  if (
    !count(s.requests) ||
    !count(s.bytes) ||
    !s.classes ||
    typeof s.classes !== "object" ||
    Array.isArray(s.classes)
  )
    return false;
  const classes = Object.entries(s.classes);
  return (
    classes.every(
      ([k, n]) =>
        ["1xx", "2xx", "3xx", "4xx", "5xx", "other"].includes(k) && count(n),
    ) &&
    classes.reduce((sum, [, n]) => sum + n, 0) === s.requests &&
    [s.mean_ms, s.p50_ms, s.p95_ms, s.p99_ms].every((n) =>
      s.requests ? duration(n) : n === null,
    )
  );
}
export function validRequestTraffic(
  v: unknown,
  org: string,
  cluster: string,
  app: string,
): v is HAProxyRequestTraffic {
  if (!scope(v, org, cluster, app)) return false;
  const t = v as unknown as HAProxyRequestTraffic;
  return (
    ["15m", "1h", "6h", "24h"].includes(t.range) &&
    [5, 20, 120, 480].includes(t.step_seconds) &&
    validRequestStats(t.summary) &&
    (t.paths === undefined ||
      (Array.isArray(t.paths) &&
        t.paths.length <= 10 &&
        ["requests", "errors", "latency"].includes(t.path_order ?? "") &&
        typeof t.more_paths === "boolean" &&
        new Set(t.paths.map((p) => `${p.path_kind}:${p.path}`)).size ===
          t.paths.length &&
        t.paths.every(
          (p) =>
            p &&
            validPathEvidence(p.path, p.path_kind) &&
            validRequestStats(p) &&
            p.requests > 0,
        ))) &&
    Array.isArray(t.points) &&
    t.points.length <= 362 &&
    t.points.every(
      (p, i) =>
        validRequestStats(p) &&
        count(p.at) &&
        p.at * 1000 >= t.start_ms - t.step_seconds * 1000 &&
        p.at * 1000 < t.end_ms &&
        (!i || p.at > t.points[i - 1]!.at),
    ) &&
    Array.isArray(t.collection) &&
    t.collection.length <= 12 &&
    new Set(t.collection.map((c) => c.node_id)).size === t.collection.length &&
    t.collection.every((c) => {
      if (
        !c ||
        !uuid.test(c.node_id) ||
        typeof c.name !== "string" ||
        c.name.length > 255 ||
        typeof c.fresh !== "boolean"
      )
        return false;
      const p = c.pipeline;
      return (
        p === null ||
        (!!p &&
          uuid.test(p.boot_id) &&
          count(p.at_ms) &&
          typeof p.listening === "boolean" &&
          (p.geo === undefined || validGeoCollection(p.geo)) &&
          (p.path_mode === undefined ||
            ["off", "sanitized", "exact"].includes(p.path_mode)) &&
          (p.path_policy === undefined ||
            p.path_policy === "" ||
            digest.test(p.path_policy)) &&
          [p.received, p.ignored, p.rejected, p.dropped, p.unconfirmed].every(
            count,
          ))
      );
    })
  );
}
export function validRequestPage(
  v: unknown,
  org: string,
  cluster: string,
  app: string,
): v is HAProxyRequestPage {
  if (!scope(v, org, cluster, app)) return false;
  const p = v as HAProxyRequestPage;
  return (
    typeof p.next_cursor === "string" &&
    /^[A-Za-z0-9_-]{0,256}$/.test(p.next_cursor) &&
    Array.isArray(p.requests) &&
    p.requests.length <= 10 &&
    new Set(p.requests.map((e) => e.id)).size === p.requests.length &&
    p.requests.every(
      (e) =>
        e &&
        eventId.test(e.id) &&
        uuid.test(e.id.slice(0, 36)) &&
        e.application_id === app &&
        (e.traffic === undefined || validTrafficDecision(e.traffic)) &&
        (e.geo === undefined || validGeography(e.geo)) &&
        validPathEvidence(e.path, e.path_kind) &&
        uuid.test(e.node_id) &&
        digest.test(e.spec_digest) &&
        count(e.at_ms) &&
        e.at_ms >= p.start_ms &&
        e.at_ms < p.end_ms &&
        Array.isArray(e.matching_versions) &&
        e.matching_versions.length <= 256 &&
        e.matching_versions.every((n) => count(n) && n > 0) &&
        /^[-A-Za-z0-9_.:<>]{1,128}$/.test(e.server) &&
        [
          "GET",
          "HEAD",
          "POST",
          "PUT",
          "PATCH",
          "DELETE",
          "OPTIONS",
          "CONNECT",
          "TRACE",
          "OTHER",
        ].includes(e.method) &&
        ["http", "https"].includes(e.scheme) &&
        (e.status === 0 ||
          (count(e.status) && e.status >= 100 && e.status <= 599)) &&
        count(e.bytes) &&
        e.bytes <= 2 ** 40 &&
        duration(e.duration_ms) &&
        [e.response_ms, e.connect_ms, e.queue_ms].every(
          (n) => n === -1 || duration(n),
        ) &&
        /^[A-Za-z-]{2}$/.test(e.termination),
    )
  );
}
export function requestQuery(
  filters: HAProxyRequestFilters,
  end?: number,
  before?: string,
): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(filters))
    if (v !== undefined && v !== "") q.set(k, String(v));
  if (end !== undefined) q.set("end", String(end));
  if (before) q.set("before", before);
  return q.toString();
}
