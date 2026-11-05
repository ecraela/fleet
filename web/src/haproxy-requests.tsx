import { HAProxyCountryFilter } from "./haproxy-country-filter";
import { geographyRequestFilters } from "./haproxy-geography-api";
import { trafficRequestFilters } from "./haproxy-traffic-model";
import {
  countryLabel,
  networkLabel,
  geographyStatus,
} from "./haproxy-geography";
import { useEffect, useState } from "react";
import {
  Badge,
  MetricCard,
  MetricGrid,
  type MetricHistory,
  Button,
  CodeBlock,
  DataTable,
  TablePagination,
  PaginatedDataTable,
  DescriptionItem,
  DescriptionList,
  Drawer,
  DrawerBody,
  DrawerDialog,
  DrawerFooter,
  EmptyState,
  InlineTip,
  LoadingStatus,
  focusFormError,
  Link,
  CountryLabel,
  Section,
  Select,
  TextField,
  TimeSeriesChart,
  Toolbar,
  type TimeSeries,
} from "@ecraela/ui";
import {
  haproxyErrorMessage,
  type HAProxyApplicationRevision,
  type HAProxyClient,
} from "./haproxy-api";
import type {
  HAProxyRequest,
  HAProxyRequestFilters,
  HAProxyRequestPage,
  HAProxyRequestStats,
  HAProxyRequestTraffic,
} from "./haproxy-requests-api";
import { validRequestPath } from "./haproxy-requests-api";

const date = (at: number) => new Date(at).toLocaleString();
const number = (n: number) =>
  new Intl.NumberFormat(undefined, { maximumFractionDigits: 2 }).format(n);
const ms = (n: number | null) =>
  n === null || n < 0 ? "Not observed" : `${number(n)} ms`;
export function requestPathLabel(path?: string, kind?: string): string {
  if (path) return path;
  return (
    (
      {
        unmatched: "Unmatched route (withheld)",
        withheld: "Path collection off",
        invalid: "Invalid or oversized path",
      } as Record<string, string>
    )[kind ?? ""] ?? "Path not collected"
  );
}
export function requestChartSeries(
  view: HAProxyRequestTraffic,
  kind: "responses" | "latency",
): TimeSeries[] {
  if (kind === "responses")
    return ["1xx", "2xx", "3xx", "4xx", "5xx", "other"].map((code, i) => ({
      id: code,
      label: code === "other" ? "No response" : code,
      tone: (
        ["muted", "success", "info", "warning", "error", "muted"] as const
      )[i]!,
      values: view.points
        .filter((p) => p.requests > 0)
        .map(
          (p) =>
            [
              p.at,
              (p.classes[code] ?? 0) /
                Math.max(
                  0.001,
                  Math.min(p.at + view.step_seconds, view.end_ms / 1000) -
                    Math.max(p.at, view.start_ms / 1000),
                ),
            ] as const,
        ),
    }));
  return (["p50_ms", "p95_ms", "p99_ms"] as const).map((key, i) => ({
    id: key,
    label: key.slice(0, -3).toUpperCase(),
    color: (["blue", "violet", "teal"] as const)[i]!,
    values: view.points.flatMap((p) =>
      p[key] === null ? [] : [[p.at, p[key]] as const],
    ),
  }));
}
function Summary({
  value,
  seconds,
  traffic,
}: {
  value: HAProxyRequestStats;
  seconds: number;
  traffic: HAProxyRequestTraffic;
}) {
  const history = (
    label: string,
    select: (point: HAProxyRequestTraffic["points"][number]) => number | null,
  ): MetricHistory => ({
    start:
      Math.floor(traffic.start_ms / 1000 / traffic.step_seconds) *
      traffic.step_seconds,
    end:
      Math.floor((traffic.end_ms - 1) / 1000 / traffic.step_seconds) *
      traffic.step_seconds,
    step: traffic.step_seconds,
    label,
    values: traffic.points.flatMap((point) => {
      const value = select(point);
      return value === null ? [] : [[point.at, value] as const];
    }),
  });
  return (
    <MetricGrid columns={3}>
      <MetricCard
        label="Recorded requests"
        value={number(value.requests)}
        description="In the selected time range"
        history={history("Requests per recorded bucket", (point) =>
          point.requests > 0 ? point.requests : null,
        )}
      />
      <MetricCard
        label="Average recorded rate"
        value={`${number(value.requests / seconds)} requests/s`}
        description="Across the selected time range"
        color="teal"
        history={history("Recorded requests per second", (point) =>
          point.requests > 0
            ? point.requests /
              Math.max(
                0.001,
                Math.min(
                  point.at + traffic.step_seconds,
                  traffic.end_ms / 1000,
                ) - Math.max(point.at, traffic.start_ms / 1000),
              )
            : null,
        )}
      />
      <MetricCard
        label="HTTP 5xx"
        value={
          value.requests
            ? `${number(((value.classes["5xx"] ?? 0) / value.requests) * 100)}%`
            : null
        }
        description="Share of recorded requests"
        color="rose"
        history={history("HTTP 5xx share per recorded bucket", (point) =>
          point.requests > 0
            ? ((point.classes["5xx"] ?? 0) / point.requests) * 100
            : null,
        )}
      />
      <MetricCard
        label="Mean total time"
        value={value.mean_ms === null ? null : ms(value.mean_ms)}
        description="HAProxy total request time"
        color="blue"
        history={history(
          "Mean request time per recorded bucket",
          (point) => point.mean_ms,
        )}
      />
      <MetricCard
        label="Estimated p95"
        value={value.p95_ms === null ? null : ms(value.p95_ms)}
        description="Over recorded requests"
        color="violet"
        history={history(
          "Estimated p95 per recorded bucket",
          (point) => point.p95_ms,
        )}
      />
      <MetricCard
        label="Recorded response bytes"
        value={`${number(value.bytes)} B`}
        description="In the selected time range"
        color="cyan"
        history={history("Response bytes per recorded bucket", (point) =>
          point.requests > 0 ? point.bytes : null,
        )}
      />
    </MetricGrid>
  );
}

function minimumTimeError(value: string): string | null {
  return /^\d+$/.test(value) && Number(value) <= 60000
    ? null
    : "Enter a minimum total time from 0 to 60000 milliseconds.";
}
function asnFilterError(value: string): string | null {
  return !value ||
    (/^(0|[1-9][0-9]{0,9})$/.test(value) && Number(value) <= 4294967295)
    ? null
    : "Enter an ASN from 0 to 4294967295.";
}
function pathFilterError(value: string): string | null {
  return !value || validRequestPath(value)
    ? null
    : "Enter a path or route pattern of up to 512 ASCII characters, without a query string or fragment.";
}

export function HAProxyApplicationTraffic({
  clusterId,
  application,
  client,
  nodeNames,
  onClose,
  onConfigure,
  inline = false,
}: {
  inline?: boolean;
  clusterId: string;
  application: HAProxyApplicationRevision;
  client: HAProxyClient;
  nodeNames: Record<string, string>;
  onClose: () => void;
  onConfigure?: () => void;
}) {
  const [draft, setDraft] = useState<HAProxyRequestFilters>(() => {
    const search = typeof window === "undefined" ? "" : window.location.search;
    return {
      ...geographyRequestFilters(search),
      ...trafficRequestFilters(search),
    };
  });
  const [minimum, setMinimum] = useState("0");
  const [filters, setFilters] = useState<HAProxyRequestFilters>(draft);
  const [geoError, setGeoError] = useState<string | null>(null);
  const [filterError, setFilterError] = useState<string | null>(null);
  const [pathError, setPathError] = useState<string | null>(null);
  const [selected, setSelected] = useState<HAProxyRequest | null>(null);
  const filterPath = (path: string) => {
    const next = { ...filters, path };
    setFilters(next);
    setDraft(next);
    setMinimum(String(next.min_ms ?? 0));
    setPathError(null);
    setSelected(null);
  };
  const content = (
    <>
      {selected && !inline ? (
        <RequestDetails
          request={selected}
          nodeNames={nodeNames}
          onPathFilter={() => {
            if (selected.path) filterPath(selected.path);
          }}
          onFilter={() => {
            const next = { ...filters, spec_digest: selected.spec_digest };
            setFilters(next);
            setDraft(next);
            setSelected(null);
          }}
        />
      ) : (
        <>
          {!application.spec.request_telemetry && (
            <InlineTip>
              Collection is disabled for the current application settings.
              Retained requests remain searchable.
              {onConfigure && (
                <Button variant="ghost" onPress={onConfigure}>
                  Configure collection
                </Button>
              )}
            </InlineTip>
          )}
          <form
            onSubmit={(event) => {
              event.preventDefault();
              const timeError = minimumTimeError(minimum);
              if (timeError) {
                setFilterError(timeError);
                focusFormError(event.currentTarget);
                return;
              }
              const asnError = asnFilterError(draft.asn ?? "");
              if (asnError) {
                setGeoError(asnError);
                focusFormError(event.currentTarget);
                return;
              }
              setGeoError(null);
              setFilterError(null);
              const pathError = pathFilterError(draft.path ?? "");
              if (pathError) {
                setPathError(pathError);
                focusFormError(event.currentTarget);
                return;
              }
              setPathError(null);
              setFilters({ ...draft, min_ms: Number(minimum) });
            }}
          >
            <Toolbar aria-label="Application request filters">
              <Select
                label="Time range"
                value={draft.range}
                onChange={(range) =>
                  setDraft((v) => ({
                    ...v,
                    range: String(range) as HAProxyRequestFilters["range"],
                  }))
                }
                options={[
                  { id: "15m", label: "Last 15 minutes" },
                  { id: "1h", label: "Last hour" },
                  { id: "6h", label: "Last 6 hours" },
                  { id: "24h", label: "Last 24 hours" },
                ]}
              />
              <Select
                label="Node"
                value={draft.node ?? ""}
                onChange={(node) =>
                  setDraft((v) => ({ ...v, node: String(node) }))
                }
                options={[
                  { id: "", label: "All nodes" },
                  ...Object.entries(nodeNames).map(([id, label]) => ({
                    id,
                    label,
                  })),
                ]}
              />
              <Select
                label="Response"
                value={draft.status_class ?? ""}
                onChange={(status_class) =>
                  setDraft((v) => ({
                    ...v,
                    status_class: String(status_class),
                  }))
                }
                options={[
                  { id: "", label: "All responses" },
                  ...["1xx", "2xx", "3xx", "4xx", "5xx"].map((id) => ({
                    id,
                    label: id,
                  })),
                  { id: "other", label: "No response" },
                ]}
              />
              <Select
                label="Protocol"
                value={draft.scheme ?? ""}
                onChange={(scheme) =>
                  setDraft((v) => ({ ...v, scheme: String(scheme) }))
                }
                options={[
                  { id: "", label: "HTTP and HTTPS" },
                  { id: "http", label: "HTTP" },
                  { id: "https", label: "HTTPS" },
                ]}
              />
              <Select
                label="Method"
                value={draft.method ?? ""}
                onChange={(method) =>
                  setDraft((v) => ({ ...v, method: String(method) }))
                }
                options={[
                  { id: "", label: "All methods" },
                  ...[
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
                  ].map((id) => ({ id, label: id })),
                ]}
              />
              <TextField
                label="Path or route pattern"
                value={draft.path ?? ""}
                onChange={(path) => {
                  setDraft((v) => ({ ...v, path }));
                  if (pathError) setPathError(pathFilterError(path));
                }}
                validationBehavior="aria"
                placeholder="/orders/:id"
                maxLength={512}
                isInvalid={!!pathError}
                errorMessage={pathError ?? undefined}
              />
              <Select
                label="Rank paths by"
                value={draft.path_order ?? "requests"}
                onChange={(order) =>
                  setDraft((v) => ({
                    ...v,
                    path_order: String(order) as NonNullable<
                      HAProxyRequestFilters["path_order"]
                    >,
                  }))
                }
                options={[
                  { id: "requests", label: "Most requests" },
                  { id: "errors", label: "Most HTTP 5xx" },
                  { id: "latency", label: "Highest p95 latency" },
                ]}
              />
              <TextField
                label="Minimum total time"
                value={minimum}
                onChange={(value) => {
                  setMinimum(value);
                  if (filterError) setFilterError(minimumTimeError(value));
                }}
                validationBehavior="aria"
                inputMode="numeric"
                suffix="ms"
                maxLength={5}
                isInvalid={!!filterError}
                errorMessage={filterError ?? undefined}
              />
              <HAProxyCountryFilter
                value={draft.country}
                onChange={(country) => setDraft((v) => ({ ...v, country }))}
              />
              <TextField
                label="ASN"
                value={draft.asn ?? ""}
                onChange={(asn) => {
                  const value = asn.trim();
                  setDraft((v) => ({ ...v, asn: value }));
                  if (geoError) setGeoError(asnFilterError(value));
                }}
                validationBehavior="aria"
                placeholder="15169"
                inputMode="numeric"
                maxLength={10}
                isInvalid={!!geoError}
                errorMessage={geoError ?? undefined}
              />
              <Button type="submit">Apply filters</Button>
            </Toolbar>
          </form>
          {filters.path && (
            <InlineTip>
              Filtered to path <code>{filters.path}</code>.
              <Button
                variant="ghost"
                onPress={() => {
                  const { path: _removed, ...rest } = filters;
                  setFilters(rest);
                  setDraft(rest);
                  setPathError(null);
                }}
              >
                Clear path filter
              </Button>
            </InlineTip>
          )}
          {filters.spec_digest && (
            <InlineTip>
              Filtered to configuration {filters.spec_digest.slice(0, 12)}.
              <Button
                variant="ghost"
                onPress={() => {
                  const { spec_digest: _removed, ...rest } = filters;
                  setFilters(rest);
                  setDraft(rest);
                }}
              >
                Clear configuration filter
              </Button>
            </InlineTip>
          )}
          {(filters.traffic_rule || filters.traffic_decision) && (
            <InlineTip>
              Filtered to traffic rule {filters.traffic_rule ?? "any"}
              {filters.traffic_decision
                ? ` (${filters.traffic_decision.replaceAll("_", " ")})`
                : ""}
              .
              <Button
                variant="ghost"
                onPress={() => {
                  const {
                    traffic_rule: _rule,
                    traffic_decision: _decision,
                    ...rest
                  } = filters;
                  setFilters(rest);
                  setDraft(rest);
                }}
              >
                Clear traffic rule filter
              </Button>
            </InlineTip>
          )}
          <RequestResults
            key={JSON.stringify(filters)}
            clusterId={clusterId}
            applicationId={application.application_id}
            client={client}
            filters={filters}
            nodeNames={nodeNames}
            onSelect={setSelected}
            onPath={filterPath}
          />
        </>
      )}
    </>
  );
  if (inline)
    return (
      <div className="cv-haproxy-configuration">
        {content}
        <Drawer
          size="lg"
          isOpen={!!selected}
          onOpenChange={(open) => {
            if (!open) setSelected(null);
          }}
        >
          <DrawerDialog title="Request details">
            <DrawerBody>
              {selected && (
                <RequestDetails
                  request={selected}
                  nodeNames={nodeNames}
                  onPathFilter={() => {
                    if (selected.path) filterPath(selected.path);
                  }}
                  onFilter={() => {
                    const next = {
                      ...filters,
                      spec_digest: selected.spec_digest,
                    };
                    setFilters(next);
                    setDraft(next);
                    setSelected(null);
                  }}
                />
              )}
            </DrawerBody>
            <DrawerFooter>
              <Button onPress={() => setSelected(null)}>Close request</Button>
            </DrawerFooter>
          </DrawerDialog>
        </Drawer>
      </div>
    );
  return (
    <Drawer
      size="lg"
      isOpen
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DrawerDialog
        title={selected ? "Request details" : "Traffic and requests"}
        description={application.spec.name}
      >
        <DrawerBody>{content}</DrawerBody>
        <DrawerFooter>
          {selected && (
            <Button onPress={() => setSelected(null)}>Back to requests</Button>
          )}
          <Button onPress={onClose}>Close</Button>
        </DrawerFooter>
      </DrawerDialog>
    </Drawer>
  );
}

function RequestResults({
  clusterId,
  applicationId,
  client,
  filters,
  nodeNames,
  onSelect,
  onPath,
}: {
  clusterId: string;
  applicationId: string;
  client: HAProxyClient;
  filters: HAProxyRequestFilters;
  nodeNames: Record<string, string>;
  onSelect: (request: HAProxyRequest) => void;
  onPath: (path: string) => void;
}) {
  const [traffic, setTraffic] = useState<HAProxyRequestTraffic | null>(
    () =>
      client.cachedRequestTraffic(clusterId, applicationId, filters) ?? null,
  );
  const [trafficError, setTrafficError] = useState<string | null>(null);
  const [requests, setRequests] = useState<HAProxyRequestPage | null>(
    () => client.cachedRequests(clusterId, applicationId, filters) ?? null,
  );
  const [requestError, setRequestError] = useState<string | null>(null);
  const [paging, setPaging] = useState(false);
  const [cursors, setCursors] = useState<string[]>([""]);
  const [page, setPage] = useState(0);
  const [end, setEnd] = useState<number | undefined>();
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const controller = new AbortController();
    let pending = false;
    const update = async () => {
      setNow(Date.now());
      if (
        pending ||
        document.visibilityState === "hidden" ||
        controller.signal.aborted
      )
        return;
      pending = true;
      try {
        const next = await client.requestTraffic(
          clusterId,
          applicationId,
          filters,
          controller.signal,
        );
        if (!controller.signal.aborted) {
          setTraffic(next);
          setTrafficError(null);
        }
      } catch (cause) {
        if (!controller.signal.aborted)
          setTrafficError(haproxyErrorMessage(cause));
      } finally {
        pending = false;
      }
    };
    void update();
    const timer = setInterval(() => void update(), 3000);
    document.addEventListener("visibilitychange", update);
    return () => {
      controller.abort();
      clearInterval(timer);
      document.removeEventListener("visibilitychange", update);
    };
  }, [client, clusterId, applicationId, filters]);
  const cursor = cursors[page];
  useEffect(() => {
    const controller = new AbortController();
    let pending = false;
    let initial = true;
    const update = async () => {
      if (
        pending ||
        document.visibilityState === "hidden" ||
        controller.signal.aborted
      )
        return;
      pending = true;
      if (initial) setPaging(true);
      try {
        const next = await client.applicationRequests(
          clusterId,
          applicationId,
          filters,
          page ? end : undefined,
          cursor,
          controller.signal,
        );
        if (!controller.signal.aborted) {
          setRequests(next);
          setRequestError(null);
        }
      } catch (cause) {
        if (!controller.signal.aborted)
          setRequestError(haproxyErrorMessage(cause));
      } finally {
        initial = false;
        pending = false;
        if (!controller.signal.aborted) setPaging(false);
      }
    };
    void update();
    const timer = page ? undefined : setInterval(() => void update(), 3000);
    document.addEventListener("visibilitychange", update);
    return () => {
      controller.abort();
      clearInterval(timer);
      document.removeEventListener("visibilitychange", update);
    };
  }, [client, clusterId, applicationId, filters, page, end, cursor]);
  return (
    <div className="cv-haproxy-configuration">
      <Section
        layout="stack"
        size="sm"
        title="Application traffic"
        description="Recorded requests for this application, including monitoring checks through its listener. Shared frontend counters are kept separate."
      >
        {trafficError && (
          <InlineTip variant="warning" title="Traffic could not refresh">
            {trafficError}
            {traffic && " Previously loaded data remains visible."}
          </InlineTip>
        )}
        {!traffic && !trafficError && (
          <LoadingStatus delayMs={1000}>
            Loading application traffic…
          </LoadingStatus>
        )}
        {traffic && (
          <>
            {now - traffic.end_ms > 15000 && (
              <InlineTip variant="warning">
                Traffic data is stale. Last successful query{" "}
                {date(traffic.end_ms)}.
              </InlineTip>
            )}
            <Summary
              value={traffic.summary}
              traffic={traffic}
              seconds={(traffic.end_ms - traffic.start_ms) / 1000}
            />
            <TimeSeriesChart
              variant="panel"
              plot="bars"
              title="Recorded responses"
              unit="requests/s"
              start={
                Math.floor(traffic.start_ms / 1000 / traffic.step_seconds) *
                traffic.step_seconds
              }
              end={
                Math.floor((traffic.end_ms - 1) / 1000 / traffic.step_seconds) *
                traffic.step_seconds
              }
              step={traffic.step_seconds}
              series={requestChartSeries(traffic, "responses")}
              emptyMessage="No recorded requests in this time range"
            />
            <TimeSeriesChart
              variant="panel"
              title="Total request time"
              unit="ms"
              start={
                Math.floor(traffic.start_ms / 1000 / traffic.step_seconds) *
                traffic.step_seconds
              }
              end={
                Math.floor((traffic.end_ms - 1) / 1000 / traffic.step_seconds) *
                traffic.step_seconds
              }
              step={traffic.step_seconds}
              series={requestChartSeries(traffic, "latency")}
              formatValue={ms}
              emptyMessage="No observed request durations"
            />
            <InlineTip>
              Percentiles are estimates over recorded HAProxy total request
              times, not backend rolling averages or client round-trip times.
              Missing request buckets remain gaps. Last query{" "}
              {date(traffic.end_ms)}.
            </InlineTip>
            <Section
              layout="stack"
              size="sm"
              title="Top paths"
              description="Up to ten path groups for the current filters, ranked by recorded requests, HTTP 5xx count or estimated p95. Select a collected path to inspect its charts and requests."
            >
              <DataTable
                aria-label="Top application paths"
                rows={(traffic.paths ?? []).map((p) => ({
                  ...p,
                  id: `${p.path_kind}:${p.path}`,
                }))}
                emptyState={
                  <EmptyState
                    title="No path statistics"
                    description="Check the agents’ local path collection settings and send fresh traffic. Existing recordings cannot be backfilled."
                  />
                }
                columns={[
                  {
                    id: "path",
                    header: "Path",
                    isRowHeader: true,
                    cell: (p) =>
                      p.path ? (
                        <Link variant="hover" onPress={() => onPath(p.path)}>
                          {p.path}
                        </Link>
                      ) : (
                        requestPathLabel(p.path, p.path_kind)
                      ),
                  },
                  {
                    id: "kind",
                    header: "Capture",
                    cell: (p) =>
                      p.path_kind === "template"
                        ? "Route pattern"
                        : p.path_kind === "exact"
                          ? "Exact path"
                          : "Withheld / unavailable",
                  },
                  {
                    id: "count",
                    header: "Requests",
                    cell: (p) => number(p.requests),
                  },
                  {
                    id: "rate",
                    header: "Average rate",
                    cell: (p) =>
                      `${number(p.requests / ((traffic.end_ms - traffic.start_ms) / 1000))} requests/s`,
                  },
                  {
                    id: "errors",
                    header: "HTTP 5xx",
                    cell: (p) => number(p.classes["5xx"] ?? 0),
                  },
                  {
                    id: "p95",
                    header: "Estimated p95",
                    cell: (p) => ms(p.p95_ms),
                  },
                ]}
              />
              <InlineTip>
                {traffic.more_paths
                  ? "More path groups exist; refine the filters to investigate them. "
                  : ""}
                Route patterns come from local agent rules. Unmatched, disabled,
                invalid and older paths are shown explicitly. Query strings are
                never collected. Exact capture is an explicit local operator
                setting.
              </InlineTip>
            </Section>
            <PaginatedDataTable
              aria-label="Request collection status"
              itemLabel="nodes"
              rows={traffic.collection.map((c) => ({ ...c, id: c.node_id }))}
              columns={[
                {
                  id: "node",
                  header: "Node",
                  isRowHeader: true,
                  cell: (c) => c.name,
                },
                {
                  id: "status",
                  header: "Collection",
                  cell: (c) => (
                    <Badge
                      tone={
                        c.fresh && c.pipeline && now - c.pipeline.at_ms <= 15000
                          ? "success"
                          : "warning"
                      }
                    >
                      {c.fresh && c.pipeline && now - c.pipeline.at_ms <= 15000
                        ? "Socket listening"
                        : "Not reporting"}
                    </Badge>
                  ),
                },
                {
                  id: "paths",
                  header: "Path collection",
                  cell: (c) =>
                    c.pipeline?.path_mode === "sanitized"
                      ? "Sanitized routes"
                      : c.pipeline?.path_mode === "exact"
                        ? "Exact paths with local redaction"
                        : c.pipeline?.path_mode === "off"
                          ? "Off"
                          : "Not observed",
                },
                {
                  id: "received",
                  header: "Received",
                  cell: (c) =>
                    c.pipeline ? number(c.pipeline.received) : "Not observed",
                },
                {
                  id: "dropped",
                  header: "Queue drops",
                  cell: (c) =>
                    c.pipeline ? number(c.pipeline.dropped) : "Not observed",
                },
                {
                  id: "unconfirmed",
                  header: "Unconfirmed",
                  cell: (c) =>
                    c.pipeline
                      ? number(c.pipeline.unconfirmed)
                      : "Not observed",
                },
                {
                  id: "rejected",
                  header: "Rejected",
                  cell: (c) =>
                    c.pipeline ? number(c.pipeline.rejected) : "Not observed",
                },
                {
                  id: "run",
                  header: "Collection run",
                  cell: (c) =>
                    c.pipeline ? (
                      <code title={c.pipeline.boot_id}>
                        {c.pipeline.boot_id.slice(0, 8)}
                      </code>
                    ) : (
                      "Not observed"
                    ),
                },
                {
                  id: "report",
                  header: "Last report",
                  cell: (c) =>
                    c.pipeline ? date(c.pipeline.at_ms) : "Not observed",
                },
              ]}
            />
          </>
        )}
        <InlineTip>
          Best-effort collection without intentional sampling. Counters cover
          each agent’s current process and all its applications; losses before
          the local socket cannot be measured. Unconfirmed deliveries may have
          reached storage. Path collection follows each agent’s local policy.
          Request bodies, full URLs, query strings, client addresses, cookies
          and headers are excluded.
        </InlineTip>
      </Section>
      <Section
        layout="stack"
        size="sm"
        title="Request search"
        description="Inspect completed requests by path, response, protocol, method, node and total time. Opening a row shows its configuration fingerprint and matching retained versions."
      >
        {requestError && (
          <InlineTip variant="warning" title="Requests could not refresh">
            {requestError}
            {requests && " Previously loaded requests remain visible."}
          </InlineTip>
        )}
        {!requests && !requestError && (
          <LoadingStatus delayMs={1000}>Loading requests…</LoadingStatus>
        )}
        {requests && (
          <>
            <DataTable
              aria-label="Application requests"
              isLoading={paging}
              rows={requests.requests}
              emptyState={
                <EmptyState
                  title="No recorded requests"
                  description="Broaden the filters or send traffic after enabling collection. Missing logs do not prove that no requests reached HAProxy."
                />
              }
              columns={[
                {
                  id: "when",
                  header: "Observed",
                  isRowHeader: true,
                  cell: (r) => (
                    <Link variant="hover" onPress={() => onSelect(r)}>
                      {date(r.at_ms)}
                    </Link>
                  ),
                },
                { id: "method", header: "Method", cell: (r) => r.method },
                {
                  id: "path",
                  header: "Path",
                  cell: (r) => requestPathLabel(r.path, r.path_kind),
                },
                {
                  id: "scheme",
                  header: "Protocol",
                  cell: (r) => r.scheme.toUpperCase(),
                },
                {
                  id: "status",
                  header: "Response",
                  cell: (r) => (
                    <Badge
                      tone={
                        r.status >= 500 || !r.status
                          ? "error"
                          : r.status >= 400
                            ? "warning"
                            : "neutral"
                      }
                    >
                      {r.status || "No response"}
                    </Badge>
                  ),
                },
                {
                  id: "node",
                  header: "Node",
                  cell: (r) => nodeNames[r.node_id] ?? r.node_id,
                },
                {
                  id: "duration",
                  header: "Total time",
                  cell: (r) => ms(r.duration_ms),
                },
              ]}
            />
            <TablePagination
              aria-label="Request pagination"
              page={page + 1}
              totalItems={null}
              currentPageItems={requests.requests.length}
              itemLabel="recorded requests"
              hasNextPage={!!requests.next_cursor}
              isDisabled={paging}
              onPageChange={(next) => {
                if (next > page + 1) {
                  setEnd(requests.end_ms);
                  setCursors((v) => [
                    ...v.slice(0, page + 1),
                    requests.next_cursor,
                  ]);
                }
                setPage(next - 1);
              }}
              trailingAction={
                page > 0 ? (
                  <Button
                    size="sm"
                    isDisabled={paging}
                    onPress={() => {
                      setPage(0);
                      setCursors([""]);
                      setEnd(undefined);
                    }}
                  >
                    Return to live
                  </Button>
                ) : undefined
              }
            />
            <InlineTip>
              {page ? "Search snapshot" : "Latest recorded requests"}:{" "}
              {date(requests.start_ms)} to {date(requests.end_ms)}. Late
              arrivals may appear on refresh.
            </InlineTip>
          </>
        )}
      </Section>
    </div>
  );
}

function RequestDetails({
  request: r,
  nodeNames,
  onFilter,
  onPathFilter,
}: {
  request: HAProxyRequest;
  nodeNames: Record<string, string>;
  onFilter: () => void;
  onPathFilter: () => void;
}) {
  return (
    <div className="cv-haproxy-configuration">
      <DescriptionList columns={2}>
        <DescriptionItem label="Observed completion">
          {date(r.at_ms)}
        </DescriptionItem>
        <DescriptionItem label="Node">
          {nodeNames[r.node_id] ?? r.node_id}
        </DescriptionItem>
        <DescriptionItem label="Request">
          {r.method} over {r.scheme.toUpperCase()}
        </DescriptionItem>
        <DescriptionItem label="Response">
          {r.status || "No HTTP response"}
        </DescriptionItem>
        {r.traffic && (
          <DescriptionItem label="Traffic rule decision">
            {r.traffic.decision.replaceAll("_", " ")} –{" "}
            {r.traffic.rule_id === "unknown"
              ? "Unknown client or lookup"
              : r.traffic.rule_id}
          </DescriptionItem>
        )}
        <DescriptionItem label="Backend server">{r.server}</DescriptionItem>
        <DescriptionItem label="Country">
          <CountryLabel
            code={r.geo?.country}
            basePath={`${import.meta.env.BASE_URL}flags/4x3`}
          >
            {countryLabel(r.geo?.country)}
          </CountryLabel>
        </DescriptionItem>
        <DescriptionItem label="Network">
          {networkLabel(r.geo?.asn)}
        </DescriptionItem>
        <DescriptionItem label="Geography">
          {geographyStatus(r.geo)}
        </DescriptionItem>
        <DescriptionItem label="Client address source">
          {
            {
              peer: "Direct peer",
              xff: "Trusted X-Forwarded-For",
              proxy_protocol: "Trusted PROXY protocol",
              unknown: "Not resolved",
            }[r.geo?.source ?? "unknown"]
          }
        </DescriptionItem>
        <DescriptionItem label="Response bytes">
          {number(r.bytes)} B
        </DescriptionItem>
        <DescriptionItem label="HAProxy total time">
          {ms(r.duration_ms)}
        </DescriptionItem>
        <DescriptionItem label="Server response headers">
          {ms(r.response_ms)}
        </DescriptionItem>
        <DescriptionItem label="Connect">{ms(r.connect_ms)}</DescriptionItem>
        <DescriptionItem label="Queue">{ms(r.queue_ms)}</DescriptionItem>
        <DescriptionItem label="HAProxy termination">
          {r.termination}
        </DescriptionItem>
        <DescriptionItem label="Matching retained versions">
          {r.matching_versions.length
            ? r.matching_versions.join(", ")
            : "Outside retained version history"}
        </DescriptionItem>
      </DescriptionList>
      <CodeBlock
        code={requestPathLabel(r.path, r.path_kind)}
        language="text"
        label="Collected request path"
        title={r.path_kind === "template" ? "Route pattern" : "Path"}
        wrap
      />
      {r.path && (
        <Link variant="hover" onPress={onPathFilter}>
          Filter requests by this path
        </Link>
      )}
      <CodeBlock
        code={r.id}
        language="text"
        label="Request observation ID"
        title="Observation ID"
        wrap
      />
      <CodeBlock
        code={r.spec_digest}
        language="text"
        label="Application configuration fingerprint"
        title="Configuration fingerprint"
        wrap
      />
      <InlineTip>
        The backend compiler assigns this fingerprint. Identical settings can
        match multiple versions; this does not identify a unique deployment. The
        observation ID is generated by the agent and is not a client trace ID.
      </InlineTip>
      <Link variant="hover" onPress={onFilter}>
        Filter requests by this configuration
      </Link>
    </div>
  );
}
