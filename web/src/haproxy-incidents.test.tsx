import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import {
  HAProxyIncidentTable,
  incidentExplanation,
  incidentTitle,
  incidentEvaluationFresh,
  mergeIncidentTimeline,
} from "./haproxy-incidents";
import {
  validIncidentPage,
  type HAProxyIncident,
  type HAProxyIncidentPage,
} from "./haproxy-incidents-api";

const org = "00000000-0000-0000-0000-000000000001";
const cluster = "00000000-0000-0000-0000-000000000002";
const incident: HAProxyIncident = {
  id: "00000000-0000-0000-0000-000000000003",
  organization_id: org,
  cluster_id: cluster,
  resource_id: "00000000-0000-0000-0000-000000000004",
  resource_name: "Store",
  rule: "application_health",
  scope: "entrypoint",
  scheme: "https",
  status: "active",
  severity: "critical",
  evidence: "failing",
  code: "request_failed",
  node_ids: ["00000000-0000-0000-0000-000000000005"],
  opened_at_ms: 1000,
  updated_at_ms: 1000,
  observed_at_ms: 1000,
  occurrences: 1,
};
const page = (): HAProxyIncidentPage => ({
  organization_id: org,
  cluster_id: cluster,
  incidents: [structuredClone(incident)],
  events: [
    {
      sequence: 1,
      kind: "opened",
      at_ms: 1000,
      incident: structuredClone(incident),
    },
  ],
  evaluated_at_ms: 1000,
  notifications: { configured: false, pending: 0, failed: 0 },
});
describe("HAProxy incident console", () => {
  it("keeps delayed or future evaluation from implying health", () => {
    expect(incidentEvaluationFresh(1000, 21000)).toBe(true);
    for (const at of [0, 999, 23000])
      expect(incidentEvaluationFresh(at, 21000)).toBe(false);
  });
  it("deduplicates timeline pages and resets its cursor window after a hidden-tab gap", () => {
    const event = page().events[0]!;
    const events = Array.from({ length: 50 }, (_, n) => ({
      ...event,
      sequence: 150 - n,
    }));
    const merged = mergeIncidentTimeline([{ ...event, sequence: 10 }], events);
    expect(merged.gap).toBe(true);
    expect(merged.events).toEqual(events);
    const older = mergeIncidentTimeline(events, [
      { ...event, sequence: 101 },
      { ...event, sequence: 100 },
    ]);
    expect(older.gap).toBe(false);
    expect(older.events).toHaveLength(51);
  });
  it("binds active state and timeline snapshots to the tenant and cluster", () => {
    expect(validIncidentPage(page(), org, cluster)).toBe(true);
    for (const mutate of [
      (p: HAProxyIncidentPage) => {
        p.organization_id = "foreign";
      },
      (p: HAProxyIncidentPage) => {
        p.incidents[0]!.cluster_id = "foreign";
      },
      (p: HAProxyIncidentPage) => {
        p.events[0]!.incident.organization_id = "foreign";
      },
      (p: HAProxyIncidentPage) => {
        p.incidents[0]!.status = "pending" as "active";
      },
      (p: HAProxyIncidentPage) => {
        p.incidents[0]!.code = "secret raw error";
      },
      (p: HAProxyIncidentPage) => {
        p.events.push(p.events[0]!);
      },
      (p: HAProxyIncidentPage) => {
        p.next_cursor = 100;
      },
      (p: HAProxyIncidentPage) => {
        p.notifications.pending = -1;
      },
      (p: HAProxyIncidentPage) => {
        p.incidents[0]!.node_ids = Array(13).fill(incident.node_ids[0]);
      },
      (p: HAProxyIncidentPage) => {
        p.events[0]!.at_ms = NaN;
      },
    ]) {
      const p = page();
      mutate(p);
      expect(validIncidentPage(p, org, cluster)).toBe(false);
    }
  });
  it("shows the affected app and actual check location while evidence is missing", () => {
    const html = renderToStaticMarkup(
      <HAProxyIncidentTable
        incidents={[{ ...incident, evidence: "unknown" }]}
        onSelect={() => {}}
      />,
    );
    expect(html).toContain("Store");
    expect(html).toContain("HTTPS check failed");
    expect(html).toContain("Entrypoint");
    expect(html).toContain("Critical");
    expect(html).toContain("Waiting for fresh evidence");
    expect(incidentTitle({ ...incident, scope: "node", scheme: "http" })).toBe(
      "HTTP check failed",
    );
  });
  it("does not label policy changes or failed rollbacks as serving recovery", () => {
    expect(
      incidentExplanation({ ...incident, code: "rollback_failed" }),
    ).toContain("needs attention");
    expect(
      incidentExplanation({ ...incident, code: "application_inactive" }),
    ).toContain("disabled or deleted");
    expect(
      incidentExplanation({ ...incident, code: "automatic_tls_disabled" }),
    ).toContain("does not verify");
    expect(
      incidentExplanation({ ...incident, code: "rollback_verified" }),
    ).toContain("verified");
  });
  it("accepts honest recovered history and rejects impossible state", () => {
    const p = page();
    p.incidents[0] = {
      ...incident,
      status: "resolved",
      evidence: "healthy",
      resolved_at_ms: 2000,
      updated_at_ms: 2000,
    };
    p.events[0] = {
      sequence: 2,
      kind: "resolved",
      at_ms: 2000,
      incident: p.incidents[0],
    };
    expect(validIncidentPage(p, org, cluster)).toBe(true);
    p.incidents[0].resolved_at_ms = 1;
    expect(validIncidentPage(p, org, cluster)).toBe(false);
  });
});
