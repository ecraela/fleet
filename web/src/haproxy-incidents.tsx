import { useEffect, useRef, useState } from "react";
import {
  Badge,
  Button,
  DataTable,
  DescriptionItem,
  DescriptionList,
  Drawer,
  DrawerBody,
  DrawerDialog,
  DrawerFooter,
  EmptyState,
  InlineTip,
  LoadingStatus,
  Section,
  Link,
  Select,
  TablePagination,
  Toolbar,
} from "@ecraela/ui";
import {
  haproxyErrorMessage,
  type HAProxyClient,
  type HAProxyIncident,
  type HAProxyIncidentEvent,
  type HAProxyIncidentPage,
} from "./haproxy-api";

export function incidentTitle(i: HAProxyIncident): string {
  return {
    application_health:
      (i.scheme?.toUpperCase() ?? "Request") + " check failed",
    tls_expiry: "Certificate expiry",
    tls_renewal: "Automatic TLS needs attention",
    agent_offline: "Agent disconnected",
    metrics_missing: "HAProxy metrics missing",
    telemetry_unavailable: "Telemetry service unavailable",
    deployment_failed: "Configuration update failed",
  }[i.rule];
}
export function incidentExplanation(i: HAProxyIncident): string {
  return (
    (
      {
        request_failed:
          "A locally configured request check failed. Inspect the affected node and application path.",
        checks_unavailable:
          "Fresh request evidence is unavailable. The previous incident remains open until recovery is verified.",
        application_inactive:
          "Monitoring stopped because the application was disabled or deleted.",
        certificate_expiring:
          "A certificate referenced by a loaded listener is approaching expiry. Check automatic renewal or replace it on the node.",
        certificate_expired:
          "A certificate referenced by a loaded listener expired. Restore a valid certificate before relying on HTTPS.",
        renewal_failed:
          "Automatic issuance or activation failed. Inspect Automatic TLS and its node jobs.",
        automatic_tls_disabled:
          "Automatic TLS was disabled. This does not verify certificate health.",
        agent_offline:
          "Neither fresh agent metrics nor a live control heartbeat is reaching the manager. Check the agent and its outbound connections.",
        metrics_missing:
          "No recent HAProxy scrape reached telemetry storage. Check ingestion and the agent’s metrics endpoint.",
        haproxy_scrape_failed:
          "The agent is reporting failed HAProxy scrapes. Check the local metrics endpoint.",
        telemetry_unavailable:
          "The manager could not read its telemetry service. Existing node incidents cannot clear without fresh evidence.",
        rollout_failed:
          "A configuration update failed and recovery is in progress.",
        rollback_failed:
          "Automatic recovery needs attention. Inspect the rollout and node maintenance. A subsequent successful update clears this incident.",
        rollback_verified:
          "The update failed and automatic rollback verified the restored configuration.",
        superseded_by_success:
          "A later configuration update completed successfully.",
        node_removed:
          "The node was revoked. Monitoring ended without asserting that it recovered.",
        listener_removed:
          "The listener was removed from the verified loaded configuration.",
        maintenance_recovered:
          "Recovery completed through node maintenance. The original configuration update remains failed; application checks report serving health separately.",
        no_active_nodes:
          "Monitoring ended because this cluster has no active enrolled nodes.",
      } as Record<string, string>
    )[i.code] ?? "Inspect the application, node and rollout observations."
  );
}
export const incidentEventLabel = (kind: string) =>
  (
    ({
      opened: "Opened",
      reopened: "Reopened",
      escalated: "Escalated",
      resolved: "Resolved",
      recovered: "Recovery recorded",
    }) as Record<string, string>
  )[kind] ?? "Updated";
const date = (at: number) =>
  at ? new Date(at).toLocaleString() : "Not observed";
const tone = (i: HAProxyIncident) =>
  i.status === "resolved"
    ? ("success" as const)
    : i.severity === "critical"
      ? ("error" as const)
      : ("warning" as const);
export const incidentEvaluationFresh = (at: number, now = Date.now()) =>
  at > 0 && now - at >= -1000 && now - at <= 20000;

export function mergeIncidentTimeline(
  current: HAProxyIncidentEvent[],
  incoming: HAProxyIncidentEvent[],
) {
  const gap =
    incoming.length === 50 &&
    (current.length === 0 ||
      incoming[incoming.length - 1]!.sequence > current[0]!.sequence);
  return {
    gap,
    events: gap
      ? incoming
      : [
          ...new Map(
            [...current, ...incoming].map((e) => [e.sequence, e]),
          ).values(),
        ]
          .sort((a, b) => b.sequence - a.sequence)
          .slice(0, 200),
  };
}

export function HAProxyIncidentTable({
  incidents,
  onSelect,
}: {
  incidents: HAProxyIncident[];
  onSelect: (i: HAProxyIncident) => void;
}) {
  return (
    <DataTable
      aria-label="Active HAProxy incidents"
      rows={incidents}
      columns={[
        {
          id: "problem",
          header: "Incident",
          isRowHeader: true,
          cell: (i) => (
            <Link variant="hover" onPress={() => onSelect(i)}>
              {incidentTitle(i)}
            </Link>
          ),
        },
        { id: "resource", header: "Resource", cell: (i) => i.resource_name },
        {
          id: "path",
          header: "Check location",
          cell: (i) =>
            i.rule === "application_health"
              ? i.scope === "node"
                ? "Direct node"
                : "Entrypoint"
              : "—",
        },
        {
          id: "severity",
          header: "Severity",
          cell: (i) => (
            <Badge tone={tone(i)}>
              {i.status === "resolved"
                ? "Resolved"
                : i.severity === "critical"
                  ? "Critical"
                  : "Warning"}
            </Badge>
          ),
        },
        { id: "since", header: "Opened", cell: (i) => date(i.opened_at_ms) },
        {
          id: "evidence",
          header: "Evidence",
          cell: (i) =>
            i.evidence === "unknown"
              ? "Waiting for fresh evidence"
              : i.evidence === "healthy"
                ? "Verifying recovery"
                : "Observed failure",
        },
      ]}
    />
  );
}

export function HAProxyIncidents({
  clusterId,
  client,
  nodeNames,
  onInspectApplication,
  compact = false,
  activityHref,
}: {
  compact?: boolean;
  activityHref?: string;
  clusterId: string;
  client: HAProxyClient;
  nodeNames: Record<string, string>;
  onInspectApplication?: ((id: string) => void) | undefined;
}) {
  const [view, setView] = useState<HAProxyIncidentPage | null>(null);
  const [events, setEvents] = useState<HAProxyIncidentEvent[]>([]);
  const [cursor, setCursor] = useState<number | undefined>();
  const [mode, setMode] = useState("active");
  const [page, setPage] = useState(1);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [selected, setSelected] = useState<HAProxyIncident | null>(null);
  const [now, setNow] = useState(Date.now());
  const busyRef = useRef(false);
  const eventsRef = useRef<HAProxyIncidentEvent[]>([]);
  const historyInitialized = useRef(false);
  useEffect(() => {
    const abort = new AbortController();
    let pending = false;
    const update = async () => {
      setNow(Date.now());
      if (
        pending ||
        busyRef.current ||
        abort.signal.aborted ||
        document.visibilityState === "hidden"
      )
        return;
      pending = true;
      try {
        const next = await client.incidents(clusterId, undefined, abort.signal);
        if (!abort.signal.aborted) {
          setView(next);
          setNow(Date.now());
          setError(null);
          const merged = mergeIncidentTimeline(eventsRef.current, next.events);
          eventsRef.current = merged.events;
          setEvents(merged.events);
          if (!historyInitialized.current || merged.gap) {
            setCursor(next.next_cursor);
            historyInitialized.current = true;
            if (merged.gap) setPage(1);
          }
          setSelected((current) =>
            current
              ? (next.incidents.find((i) => i.id === current.id) ?? current)
              : null,
          );
        }
      } catch (cause) {
        if (!abort.signal.aborted) setError(haproxyErrorMessage(cause));
      } finally {
        pending = false;
      }
    };
    const resume = () => void update();
    void update();
    const timer = setInterval(resume, 5000);
    document.addEventListener("visibilitychange", resume);
    return () => {
      abort.abort();
      clearInterval(timer);
      document.removeEventListener("visibilitychange", resume);
    };
  }, [client, clusterId]);
  const active = view?.incidents.filter((i) => i.status === "active") ?? [];
  const stale = !!view && !incidentEvaluationFresh(view.evaluated_at_ms, now);
  const displayPage = Math.min(
    page,
    Math.max(
      1,
      Math.ceil((mode === "active" ? active.length : events.length) / 10),
    ),
  );
  const older = async () => {
    if (!cursor || busyRef.current) return;
    busyRef.current = true;
    setBusy(true);
    try {
      const next = await client.incidents(clusterId, cursor);
      eventsRef.current = mergeIncidentTimeline(
        eventsRef.current,
        next.events,
      ).events;
      setEvents(eventsRef.current);
      setCursor(next.next_cursor);
      setError(null);
    } catch (cause) {
      setError(haproxyErrorMessage(cause));
    } finally {
      busyRef.current = false;
      setBusy(false);
    }
  };
  return (
    <Section
      layout="stack"
      size="sm"
      title={compact ? "Active incidents" : "Incidents"}
      actions={
        compact && activityHref ? (
          <Link href={activityHref}>View activity</Link>
        ) : undefined
      }
      description="Application checks, loaded certificates, agents and configuration updates. Evaluated continuously by the HAProxy manager."
    >
      <div className="cv-haproxy-configuration">
        {error && (
          <InlineTip variant="warning" title="Incidents could not refresh">
            {error} Previously loaded incidents remain visible.
          </InlineTip>
        )}
        {stale && (
          <InlineTip variant="warning" title="Incident evaluation is delayed">
            Last evaluated {date(view?.evaluated_at_ms ?? 0)}. These incidents
            do not confirm current health.
          </InlineTip>
        )}
        {!view && !error && (
          <LoadingStatus delayMs={1000}>Loading incidents…</LoadingStatus>
        )}
        {view && (
          <>
            {!compact && (
              <Toolbar aria-label="HAProxy incident filters">
                <Select
                  label="Show incidents"
                  value={mode}
                  onChange={(v) => {
                    setMode(String(v));
                    setPage(1);
                  }}
                  options={[
                    {
                      id: "active",
                      label: "Active incidents (" + active.length + ")",
                    },
                    { id: "history", label: "Incident timeline" },
                  ]}
                />
              </Toolbar>
            )}
            {mode === "active" ? (
              active.length ? (
                <>
                  <HAProxyIncidentTable
                    incidents={
                      compact
                        ? active.slice(0, 5)
                        : active.slice((displayPage - 1) * 10, displayPage * 10)
                    }
                    onSelect={setSelected}
                  />
                  {!compact && (
                    <TablePagination
                      page={displayPage}
                      pageSize={10}
                      totalItems={active.length}
                      onPageChange={setPage}
                      itemLabel="incidents"
                    />
                  )}
                </>
              ) : (
                <EmptyState
                  title={
                    stale
                      ? "Waiting for incident evaluation"
                      : "No active incidents"
                  }
                  description="Only configured local checks are monitored. This does not verify public reachability."
                />
              )
            ) : (
              <>
                {events.length ? (
                  <>
                    <DataTable
                      aria-label="HAProxy incident timeline"
                      rows={events
                        .slice((displayPage - 1) * 10, displayPage * 10)
                        .map((e) => ({ ...e, id: String(e.sequence) }))}
                      columns={[
                        {
                          id: "event",
                          header: "Event",
                          isRowHeader: true,
                          cell: (e) => (
                            <Link
                              variant="hover"
                              onPress={() =>
                                setSelected(
                                  view.incidents.find(
                                    (i) => i.id === e.incident.id,
                                  ) ?? e.incident,
                                )
                              }
                            >
                              {incidentEventLabel(e.kind)}
                            </Link>
                          ),
                        },
                        {
                          id: "incident",
                          header: "Incident",
                          cell: (e) => incidentTitle(e.incident),
                        },
                        {
                          id: "resource",
                          header: "Resource",
                          cell: (e) => e.incident.resource_name,
                        },
                        {
                          id: "when",
                          header: "Time",
                          cell: (e) => date(e.at_ms),
                        },
                      ]}
                    />
                    <TablePagination
                      page={displayPage}
                      pageSize={10}
                      totalItems={events.length}
                      onPageChange={setPage}
                      itemLabel="events"
                    />
                  </>
                ) : (
                  <EmptyState
                    title="No incident history"
                    description="Failures and verified recovery will appear here."
                  />
                )}
                {cursor && events.length < 200 && (
                  <Button isPending={busy} onPress={() => void older()}>
                    Load older events
                  </Button>
                )}
              </>
            )}
            {!compact && (
              <>
                <InlineTip>
                  Request and certificate failures open after 10 seconds; agent
                  and metrics failures after 30 seconds. Recovery requires 10
                  seconds of fresh passing evidence. Repeated failures share one
                  incident.
                </InlineTip>
                {view.notifications.configured ? (
                  <InlineTip
                    variant={view.notifications.failed ? "warning" : "info"}
                  >
                    Webhook delivery enabled. {view.notifications.pending}{" "}
                    pending, {view.notifications.failed} failed deliveries.
                    Repeated notifications are grouped for five minutes.
                  </InlineTip>
                ) : (
                  <InlineTip>
                    Console alerts are active. To enable delivery, configure a
                    signed HTTPS webhook on the HAProxy manager.
                  </InlineTip>
                )}
              </>
            )}
          </>
        )}
      </div>
      <Drawer
        isOpen={!!selected}
        onOpenChange={(open) => {
          if (!open) setSelected(null);
        }}
        size="lg"
      >
        <DrawerDialog
          title={selected ? incidentTitle(selected) : "Incident"}
          description={selected?.resource_name}
        >
          <DrawerBody>
            {selected && (
              <div className="cv-haproxy-configuration">
                <InlineTip
                  variant={selected.status === "resolved" ? "info" : "warning"}
                >
                  {incidentExplanation(selected)}
                </InlineTip>
                <DescriptionList columns={2}>
                  <DescriptionItem label="Status">
                    <Badge tone={tone(selected)}>
                      {selected.status === "resolved"
                        ? "Resolved"
                        : selected.severity === "critical"
                          ? "Critical"
                          : "Warning"}
                    </Badge>
                  </DescriptionItem>
                  <DescriptionItem label="Check location">
                    {selected.scope === "node"
                      ? "Direct node"
                      : selected.scope === "entrypoint"
                        ? "Entrypoint"
                        : selected.scope || "Manager"}
                  </DescriptionItem>
                  <DescriptionItem label="Opened">
                    {date(selected.opened_at_ms)}
                  </DescriptionItem>
                  <DescriptionItem label="Resolved">
                    {date(selected.resolved_at_ms ?? 0)}
                  </DescriptionItem>
                  <DescriptionItem label="Last evidence">
                    {date(selected.observed_at_ms)}
                  </DescriptionItem>
                  <DescriptionItem label="Occurrences">
                    {selected.occurrences}
                  </DescriptionItem>
                  <DescriptionItem label="Affected nodes">
                    {selected.node_ids
                      .map((id) => nodeNames[id] ?? id)
                      .join(", ") || "Cluster"}
                  </DescriptionItem>
                </DescriptionList>
                {selected.evidence === "unknown" && (
                  <InlineTip variant="warning">
                    Fresh evidence is missing. This incident has not been
                    verified as recovered.
                  </InlineTip>
                )}
              </div>
            )}
          </DrawerBody>
          <DrawerFooter>
            {selected?.rule === "application_health" &&
              onInspectApplication && (
                <Button
                  onPress={() => {
                    onInspectApplication(selected.resource_id);
                    setSelected(null);
                  }}
                >
                  Inspect application traffic
                </Button>
              )}
            <Button onPress={() => setSelected(null)}>Close</Button>
          </DrawerFooter>
        </DrawerDialog>
      </Drawer>
    </Section>
  );
}
