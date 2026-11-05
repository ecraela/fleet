export interface HAProxyIncident {
  id: string;
  organization_id: string;
  cluster_id: string;
  rule:
    | "application_health"
    | "tls_expiry"
    | "tls_renewal"
    | "agent_offline"
    | "metrics_missing"
    | "telemetry_unavailable"
    | "deployment_failed";
  resource_id: string;
  resource_name: string;
  scope?: string;
  scheme?: "http" | "https";
  status: "active" | "resolved";
  severity: "warning" | "critical";
  evidence: "failing" | "healthy" | "unknown" | "retired";
  code: string;
  node_ids: string[];
  opened_at_ms: number;
  updated_at_ms: number;
  resolved_at_ms?: number;
  observed_at_ms: number;
  occurrences: number;
}
export interface HAProxyIncidentEvent {
  sequence: number;
  kind: "opened" | "reopened" | "escalated" | "resolved" | "recovered";
  at_ms: number;
  incident: HAProxyIncident;
}
export interface HAProxyIncidentPage {
  notifications: { configured: boolean; pending: number; failed: number };
  organization_id: string;
  cluster_id: string;
  incidents: HAProxyIncident[];
  events: HAProxyIncidentEvent[];
  next_cursor?: number;
  evaluated_at_ms: number;
}
const uuid = (v: unknown): v is string =>
  typeof v === "string" &&
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(v);
const integer = (v: unknown): v is number =>
  typeof v === "number" && Number.isSafeInteger(v) && v >= 0;
const record = (v: unknown): v is Record<string, unknown> =>
  typeof v === "object" && v !== null && !Array.isArray(v);
const member = (value: unknown, allowed: string[]): value is string =>
  typeof value === "string" && allowed.includes(value);
const rules = [
  "application_health",
  "tls_expiry",
  "tls_renewal",
  "agent_offline",
  "metrics_missing",
  "telemetry_unavailable",
  "deployment_failed",
];
const codes = [
  "request_failed",
  "checks_unavailable",
  "application_inactive",
  "certificate_expiring",
  "certificate_expired",
  "renewal_failed",
  "automatic_tls_disabled",
  "agent_offline",
  "metrics_missing",
  "haproxy_scrape_failed",
  "telemetry_unavailable",
  "rollout_failed",
  "rollback_failed",
  "rollback_verified",
  "superseded_by_success",
  "node_removed",
  "listener_removed",
  "maintenance_recovered",
  "no_active_nodes",
];
export function validIncident(
  v: unknown,
  organization: string,
  cluster: string,
): v is HAProxyIncident {
  if (
    !record(v) ||
    !uuid(v.id) ||
    v.organization_id !== organization ||
    v.cluster_id !== cluster ||
    !uuid(v.resource_id) ||
    !member(v.rule, rules) ||
    typeof v.resource_name !== "string" ||
    v.resource_name.length > 255 ||
    !member(v.status, ["active", "resolved"]) ||
    !member(v.severity, ["warning", "critical"]) ||
    !member(v.evidence, ["failing", "healthy", "unknown", "retired"]) ||
    !member(v.code, codes) ||
    !Array.isArray(v.node_ids) ||
    v.node_ids.length > 12 ||
    !v.node_ids.every(uuid) ||
    new Set(v.node_ids).size !== v.node_ids.length ||
    !integer(v.opened_at_ms) ||
    !integer(v.updated_at_ms) ||
    !integer(v.observed_at_ms) ||
    !integer(v.occurrences) ||
    v.occurrences < 1 ||
    v.opened_at_ms < 1 ||
    v.updated_at_ms < v.opened_at_ms ||
    (v.resolved_at_ms !== undefined && !integer(v.resolved_at_ms))
  )
    return false;
  if (
    v.status === "resolved" &&
    (!integer(v.resolved_at_ms) || v.resolved_at_ms < v.opened_at_ms)
  )
    return false;
  if (v.rule === "application_health")
    return (
      member(v.scope, ["node", "entrypoint"]) &&
      member(v.scheme, ["http", "https"])
    );
  if (v.scheme !== undefined) return false;
  return v.rule === "tls_expiry"
    ? typeof v.scope === "string" && /^[A-Za-z0-9_.:-]{1,128}$/.test(v.scope)
    : v.scope === undefined;
}
export function validIncidentPage(
  v: unknown,
  org: string,
  cluster: string,
): v is HAProxyIncidentPage {
  if (
    !record(v) ||
    !record(v.notifications) ||
    typeof v.notifications.configured !== "boolean" ||
    !integer(v.notifications.pending) ||
    !integer(v.notifications.failed)
  )
    return false;
  if (
    !record(v) ||
    v.organization_id !== org ||
    v.cluster_id !== cluster ||
    !integer(v.evaluated_at_ms) ||
    !Array.isArray(v.incidents) ||
    v.incidents.length > 100 ||
    !v.incidents.every((i) => validIncident(i, org, cluster)) ||
    new Set(v.incidents.map((i) => (i as HAProxyIncident).id)).size !==
      v.incidents.length ||
    !Array.isArray(v.events) ||
    v.events.length > 50
  )
    return false;
  let previous = Number.MAX_SAFE_INTEGER;
  for (const e of v.events) {
    if (
      !record(e) ||
      !integer(e.sequence) ||
      e.sequence < 1 ||
      e.sequence >= previous ||
      !integer(e.at_ms) ||
      !member(e.kind, [
        "opened",
        "reopened",
        "escalated",
        "resolved",
        "recovered",
      ]) ||
      !validIncident(e.incident, org, cluster)
    )
      return false;
    previous = e.sequence;
  }
  return (
    v.next_cursor === undefined ||
    (integer(v.next_cursor) &&
      v.events.length === 50 &&
      v.next_cursor === previous)
  );
}
