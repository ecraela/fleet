import { HAProxyGeoSetup } from "./haproxy-geo-setup";
import { useEffect, useState } from "react";
import {
  Badge,
  CountryLabel,
  GeographyDistribution,
  MetricCard,
  MetricGrid,
  Button,
  PaginatedDataTable,
  EmptyState,
  InlineTip,
  Link,
  LoadingStatus,
  Section,
  Select,
  Toolbar,
} from "@ecraela/ui";
import { haproxyErrorMessage, type HAProxyClient } from "./haproxy-api";
import {
  geographyRequestFilters,
  type HAProxyGeographyTraffic,
} from "./haproxy-geography-api";
import {
  requestQuery,
  type HAProxyRequestFilters,
} from "./haproxy-requests-api";
import type { HAProxyGeography } from "./haproxy-geography-model";

const number = (n: number) =>
  new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 }).format(n);
export function countryLabel(code?: string): string {
  if (!code) return "Unknown country";
  return `${new Intl.DisplayNames(undefined, { type: "region" }).of(code) ?? code} (${code})`;
}
export const networkLabel = (asn?: number) =>
  asn ? `AS${asn}` : "Unknown network";
export function geographyStatus(g?: HAProxyGeography): string {
  return (
    (
      {
        located: "Located",
        partial: "Partially located",
        not_found: "No database match",
        non_public: "Private or local address",
        unresolved: "Client address unresolved",
        database_unavailable: "Database unavailable",
        disabled: "Geo collection off",
        unavailable: "Not collected",
      } as Record<string, string>
    )[g?.status ?? "unavailable"] ?? "Not collected"
  );
}
export function geographyRequestHref(
  href: string,
  filters: HAProxyRequestFilters,
): string {
  const [path, search] = href.split("?");
  const q = new URLSearchParams(search);
  for (const key of ["country", "asn", "range", "node", "scheme"])
    q.delete(key);
  const selected = new URLSearchParams(requestQuery(filters));
  for (const [key, value] of selected) q.set(key, value);
  return `${path}?${q.toString()}`;
}
export function HAProxyApplicationGeography({
  client,
  clusterId,
  applicationId,
  nodeNames,
  requestHref,
}: {
  client: HAProxyClient;
  clusterId: string;
  applicationId: string;
  nodeNames: Record<string, string>;
  requestHref: string;
}) {
  const [draft, setDraft] = useState<HAProxyRequestFilters>(() =>
    geographyRequestFilters(
      typeof window === "undefined" ? "" : window.location.search,
    ),
  );
  const [filters, setFilters] = useState(draft);
  const resourceKey = `${clusterId}/${applicationId}?${requestQuery(filters)}`;
  const [loaded, setLoaded] = useState<{
    key: string;
    value: HAProxyGeographyTraffic | null;
  }>(() => ({
    key: resourceKey,
    value: client.cachedGeography(clusterId, applicationId, filters) ?? null,
  }));
  const data =
    loaded.key === resourceKey
      ? loaded.value
      : (client.cachedGeography(clusterId, applicationId, filters) ?? null);
  const [error, setError] = useState<string | null>(null);
  const [setup, setSetup] = useState(false);
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const controller = new AbortController();
    let pending = false;
    setError(null);
    const update = async () => {
      setNow(Date.now());
      if (
        pending ||
        setup ||
        document.visibilityState === "hidden" ||
        controller.signal.aborted
      )
        return;
      pending = true;
      try {
        const result = await client.applicationGeography(
          clusterId,
          applicationId,
          filters,
          controller.signal,
        );
        if (!controller.signal.aborted) {
          setLoaded({ key: resourceKey, value: result });
          setError(null);
        }
      } catch (cause) {
        if (!controller.signal.aborted) setError(haproxyErrorMessage(cause));
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
  }, [client, clusterId, applicationId, filters, setup, resourceKey]);
  const share = (known: number, total: number) =>
    total
      ? `${number((known / total) * 100)}% (${number(known)} of ${number(total)})`
      : "No recorded requests";
  return (
    <div className="cv-haproxy-configuration">
      <Section
        layout="stack"
        title="Geography"
        description="Country and network estimates from the nodes’ local databases. Raw client addresses and forwarded headers stay on the nodes."
        actions={
          <Button onPress={() => setSetup(true)}>Client IP & Geo setup</Button>
        }
      >
        <form
          onSubmit={(event) => {
            event.preventDefault();
            setFilters(draft);
          }}
        >
          <Toolbar aria-label="Geography filters">
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
            <Button type="submit">Apply filters</Button>
          </Toolbar>
        </form>
        {(filters.country || filters.asn) && (
          <InlineTip>
            Filtered to{" "}
            {filters.country
              ? countryLabel(
                  filters.country === "unknown" ? "" : filters.country,
                )
              : "all countries"}{" "}
            and{" "}
            {filters.asn ? networkLabel(Number(filters.asn)) : "all networks"}.{" "}
            <Button
              variant="ghost"
              onPress={() => {
                const { country: _country, asn: _asn, ...rest } = filters;
                setFilters(rest);
                setDraft(rest);
              }}
            >
              Clear geography filters
            </Button>
          </InlineTip>
        )}
        {error && (
          <InlineTip variant="warning" title="Geography could not refresh">
            {error} Previously loaded data remains visible.
          </InlineTip>
        )}
        {!data && !error && (
          <LoadingStatus delayMs={1000}>Loading geography…</LoadingStatus>
        )}
        {data && (
          <>
            {now - data.end_ms > 15000 && (
              <InlineTip variant="warning">
                Geography data is stale. Last query{" "}
                {new Date(data.end_ms).toLocaleString()}.
              </InlineTip>
            )}
            <MetricGrid columns={3}>
              <MetricCard
                label="Recorded requests"
                value={number(data.summary.requests)}
                description="In the selected time range"
              />
              <MetricCard
                label="Country coverage"
                value={share(data.country_known, data.summary.requests)}
                description="Requests with a resolved country"
                color="teal"
              />
              <MetricCard
                label="Network coverage"
                value={share(data.asn_known, data.summary.requests)}
                description="Requests with a resolved ASN"
                color="violet"
              />
            </MetricGrid>
            {!!(data.unresolved || data.database_unavailable) && (
              <InlineTip variant="warning">
                {number(data.unresolved)} requests had unresolved client
                addresses; {number(data.database_unavailable)} could not use the
                configured databases. Inspect node collection below.
              </InlineTip>
            )}
          </>
        )}
        <InlineTip>
          These are recorded requests, including listener health checks.
          Missing, disabled, older and unresolved observations remain unknown.
          IP geography is an estimate; it does not identify a visitor or their
          physical location.
        </InlineTip>
      </Section>
      {data && (
        <>
          <GeographyDistribution
            countries={data.countries}
            total={data.summary.requests}
            durationSeconds={(data.end_ms - data.start_ms) / 1000}
            flagBasePath={`${import.meta.env.BASE_URL}flags/4x3`}
            moreCountries={data.more_countries}
          />
          <Section
            layout="stack"
            title="Countries"
            description="Top ten country groups by recorded request count. Unknown requests remain in the totals."
          >
            <PaginatedDataTable
              paginationKey={JSON.stringify(filters)}
              aria-label="Application traffic by country"
              itemLabel="countries"
              rows={data.countries.map((c) => ({
                ...c,
                id: c.country || "unknown",
              }))}
              rowLabel={(c) => countryLabel(c.country)}
              columns={[
                {
                  id: "country",
                  header: "Country",
                  isRowHeader: true,
                  cell: (c) => (
                    <CountryLabel
                      code={c.country}
                      basePath={`${import.meta.env.BASE_URL}flags/4x3`}
                    >
                      {countryLabel(c.country)}
                    </CountryLabel>
                  ),
                },
                {
                  id: "requests",
                  header: "Requests",
                  cell: (c) => number(c.requests),
                },
                {
                  id: "share",
                  header: "Share",
                  cell: (c) =>
                    `${number((c.requests / Math.max(1, data.summary.requests)) * 100)}%`,
                },
                {
                  id: "errors",
                  header: "HTTP 5xx",
                  cell: (c) => number(c.classes["5xx"] ?? 0),
                },
                {
                  id: "p95",
                  header: "Estimated p95",
                  cell: (c) =>
                    c.p95_ms === null
                      ? "Not observed"
                      : `${number(c.p95_ms)} ms`,
                },
                {
                  id: "inspect",
                  header: "",
                  cell: (c) => (
                    <Link
                      href={geographyRequestHref(requestHref, {
                        ...filters,
                        country: c.country || "unknown",
                      })}
                    >
                      View requests
                    </Link>
                  ),
                },
              ]}
              emptyState={
                <EmptyState
                  title="No recorded country data"
                  description="Enable application request collection and configure Geo on the nodes. Earlier requests cannot be backfilled."
                />
              }
            />
            {data.more_countries && (
              <InlineTip>
                More countries exist. Narrow the time range or node filter to
                investigate them.
              </InlineTip>
            )}
          </Section>
          <Section
            layout="stack"
            title="Networks"
            description="Top ten autonomous system numbers by recorded request count."
          >
            <PaginatedDataTable
              paginationKey={JSON.stringify(filters)}
              aria-label="Application traffic by network"
              itemLabel="networks"
              rows={data.asns.map((c) => ({ ...c, id: String(c.asn) }))}
              rowLabel={(c) => networkLabel(c.asn)}
              columns={[
                {
                  id: "asn",
                  header: "Network",
                  isRowHeader: true,
                  cell: (c) => networkLabel(c.asn),
                },
                {
                  id: "requests",
                  header: "Requests",
                  cell: (c) => number(c.requests),
                },
                {
                  id: "share",
                  header: "Share",
                  cell: (c) =>
                    `${number((c.requests / Math.max(1, data.summary.requests)) * 100)}%`,
                },
                {
                  id: "errors",
                  header: "HTTP 5xx",
                  cell: (c) => number(c.classes["5xx"] ?? 0),
                },
                {
                  id: "inspect",
                  header: "",
                  cell: (c) => (
                    <Link
                      href={geographyRequestHref(requestHref, {
                        ...filters,
                        asn: String(c.asn),
                      })}
                    >
                      View requests
                    </Link>
                  ),
                },
              ]}
              emptyState={
                <EmptyState
                  title="No recorded network data"
                  description="Network lookup requires a local ASN database. Unknown results are kept separate."
                />
              }
            />
            {data.more_asns && (
              <InlineTip>
                More networks exist. Narrow the time range or node filter to
                investigate them.
              </InlineTip>
            )}
          </Section>
          <Section
            layout="stack"
            title="Node Geo collection"
            description="Host administrators choose the trusted proxy ranges and database files. Collection status covers the node’s current agent process."
          >
            <PaginatedDataTable
              paginationKey={JSON.stringify(filters)}
              aria-label="Node Geo collection"
              itemLabel="nodes"
              rows={data.collection.map((c) => ({ ...c, id: c.node_id }))}
              rowLabel={(c) => c.name}
              columns={[
                {
                  id: "node",
                  header: "Node",
                  isRowHeader: true,
                  cell: (c) => c.name,
                },
                {
                  id: "status",
                  header: "Status",
                  cell: (c) => (
                    <Badge
                      tone={
                        !c.fresh ||
                        now - (c.pipeline?.at_ms ?? 0) > 15000 ||
                        c.pipeline?.geo?.status === "database_unavailable"
                          ? "warning"
                          : c.pipeline?.geo?.status === "ready"
                            ? "success"
                            : "neutral"
                      }
                    >
                      {!c.fresh || now - (c.pipeline?.at_ms ?? 0) > 15000
                        ? "Not reporting"
                        : c.pipeline?.geo?.status === "ready"
                          ? "Ready"
                          : c.pipeline?.geo?.status === "database_unavailable"
                            ? "Database unavailable"
                            : c.pipeline?.geo?.status === "disabled"
                              ? "Off"
                              : "Not configured"}
                    </Badge>
                  ),
                },
                {
                  id: "source",
                  header: "Client address",
                  cell: (c) =>
                    ({
                      peer: "Direct peer",
                      xff: "Trusted X-Forwarded-For",
                      proxy_protocol: "Trusted PROXY protocol",
                      off: "Off",
                    })[c.pipeline?.geo?.mode ?? "off"],
                },
                {
                  id: "country",
                  header: "Country database built",
                  cell: (c) =>
                    c.pipeline?.geo?.country_database
                      ? new Date(
                          c.pipeline.geo.country_database.built_at * 1000,
                        ).toLocaleDateString()
                      : "Not observed",
                },
                {
                  id: "asn",
                  header: "ASN database built",
                  cell: (c) =>
                    c.pipeline?.geo?.asn_database
                      ? new Date(
                          c.pipeline.geo.asn_database.built_at * 1000,
                        ).toLocaleDateString()
                      : "Not observed",
                },
                {
                  id: "profile",
                  header: "Profile",
                  cell: (c) =>
                    c.pipeline?.geo ? (
                      <code title={c.pipeline.geo.profile}>
                        {c.pipeline.geo.profile.slice(0, 12)}
                      </code>
                    ) : (
                      "Not observed"
                    ),
                },
                {
                  id: "drops",
                  header: "Queue drops / unconfirmed",
                  cell: (c) =>
                    c.pipeline
                      ? `${number(c.pipeline.dropped)} / ${number(c.pipeline.unconfirmed)}`
                      : "Not observed",
                },
              ]}
            />
          </Section>
        </>
      )}
      {setup && (
        <HAProxyGeoSetup
          client={client}
          clusterId={clusterId}
          applicationId={applicationId}
          onClose={() => setSetup(false)}
        />
      )}
    </div>
  );
}
