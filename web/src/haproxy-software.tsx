import { useEffect, useRef, useState } from "react";
import {
  Badge,
  Button,
  DescriptionItem,
  DescriptionList,
  InlineTip,
  LoadingStatus,
  PaginatedDataTable,
  Section,
  Select,
} from "@ecraela/ui";
import {
  haproxyErrorMessage,
  type HAProxyClient,
  type HAProxyNode,
  type HAProxySoftwarePlan,
  type HAProxySoftwareRelease,
  type HAProxySoftwareInventoryNode,
  type HAProxySoftwareView,
  type HAProxyInstallationSoftwarePlan,
  type HAProxyInstallationSoftwareView,
} from "./haproxy-api";

const active = new Set([
  "queued",
  "preparing",
  "activating",
  "verifying",
  "needs_attention",
]);

export function SignedReleaseDetails({
  release,
  platform,
  role,
}: {
  release: HAProxySoftwareRelease;
  platform?: string | undefined;
  role: "agent" | "manager";
}) {
  const digest =
    platform &&
    (role === "agent" ? release.agent_digests : release.manager_digests)[
      platform
    ];
  return (
    <div className="cv-haproxy-release-details">
      <p>
        <strong>Release notes</strong>
      </p>
      <p className="cv-haproxy-release-notes">
        {release.notes || "No signed release notes were supplied."}
      </p>
      <DescriptionList columns={2}>
        <DescriptionItem label="Publisher">{release.publisher}</DescriptionItem>
        <DescriptionItem label="Supported platforms">
          {release.platforms.join(", ")}
        </DescriptionItem>
        <DescriptionItem label="Protocol / state format">
          {release.protocol} / {release.state_schema}
        </DescriptionItem>
        <DescriptionItem label="Manifest SHA-256">
          <code className="cv-haproxy-digest">{release.manifest_sha256}</code>
        </DescriptionItem>
        <DescriptionItem
          label={`${role === "agent" ? "Agent" : "Manager"} SHA-256${platform ? ` (${platform})` : ""}`}
        >
          {digest ? (
            <code className="cv-haproxy-digest">{digest}</code>
          ) : (
            "No artifact for this platform"
          )}
        </DescriptionItem>
        <DescriptionItem label="Manager compatibility">
          {release.compatibility.manager.min}–
          {release.compatibility.manager.max}
        </DescriptionItem>
        <DescriptionItem label="Agent compatibility">
          {release.compatibility.agent.min}–{release.compatibility.agent.max}
        </DescriptionItem>
      </DescriptionList>
    </div>
  );
}

export function HAProxySoftwareInventory({
  client,
}: {
  client: HAProxyClient | null;
}) {
  const [rows, setRows] = useState<HAProxySoftwareInventoryNode[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [healthPartial, setHealthPartial] = useState(false);
  const [releases, setReleases] = useState<HAProxySoftwareRelease[]>([]);
  const [manager, setManager] =
    useState<HAProxyInstallationSoftwareView | null>(null);
  const cursors = useRef<(string | undefined)[]>([undefined]);

  useEffect(() => {
    setRows([]);
    setLoaded(false);
    setCursor(null);
    setManager(null);
    setReleases([]);
    setError(null);
    setHealthPartial(false);
    cursors.current = [undefined];
    if (!client) return;
    let alive = true;
    const refresh = () => {
      if (document.hidden) return;
      void Promise.all([
        Promise.all(
          cursors.current.map((cursor) => client.softwareInventory(cursor)),
        ),
        client.installationSoftware(),
        client.softwareReleases(),
      ])
        .then(([pages, installation, catalog]) => {
          if (!alive) return;
          setRows([
            ...new Map(
              pages
                .flatMap((page) => page.nodes)
                .map((row) => [row.node.id, row]),
            ).values(),
          ]);
          setCursor(pages.at(-1)?.next_cursor ?? null);
          setHealthPartial(pages.some((page) => page.partial));
          setManager(installation);
          setReleases(catalog);
          setError(null);
          setLoaded(true);
        })
        .catch((cause) => {
          if (alive) {
            setError(haproxyErrorMessage(cause));
            setLoaded(true);
          }
        });
    };
    refresh();
    const timer = window.setInterval(refresh, 30_000);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
  }, [client]);

  const loadMore = async () => {
    if (!client || !cursor || loading) return;
    setLoading(true);
    try {
      const page = await client.softwareInventory(cursor);
      cursors.current.push(cursor);
      setRows((current) => [
        ...new Map(
          [...current, ...page.nodes].map((row) => [row.node.id, row]),
        ).values(),
      ]);
      setCursor(page.next_cursor);
      setHealthPartial((current) => current || page.partial);
      setError(null);
    } catch (cause) {
      setError(haproxyErrorMessage(cause));
    } finally {
      setLoading(false);
    }
  };

  const signedBuild = (row: HAProxySoftwareInventoryNode) => {
    const build = row.observed?.build;
    return releases.find(
      (release) =>
        release.version === build?.version &&
        release.agent_digests[build.platform] === build.executable_sha256,
    );
  };
  const currentStatus = (row: HAProxySoftwareInventoryNode) =>
    row.observed?.connected &&
    Date.now() - Date.parse(row.observed.last_seen) < 6_000
      ? "Current"
      : row.observed
        ? "Last reported"
        : "Unknown";
  const updateStatus = (row: HAProxySoftwareInventoryNode) =>
    row.latest
      ? `${row.latest.status.replaceAll("_", " ")}${row.latest.error_code ? ` (${row.latest.error_code})` : ""}`
      : "No update recorded";
  const healthStatus = (row: HAProxySoftwareInventoryNode) =>
    !row.metrics_available
      ? "Unknown"
      : !row.telemetry_fresh
        ? "Telemetry stale"
        : row.scrape_ok
          ? "Healthy"
          : "HAProxy unavailable";
  const tableRows = rows.map((row) => ({ ...row, id: row.node.id }));
  const managerRelease =
    manager &&
    releases.find(
      (release) =>
        release.version === manager.observed.version &&
        release.manager_digests[manager.observed.platform] ===
          manager.observed.executable_sha256,
    );
  const managerUpdaterFresh =
    !!manager?.updater &&
    Date.now() - Date.parse(manager.updater.last_seen) < 90_000;
  const ingestVerified =
    !!manager?.updater?.ready &&
    managerUpdaterFresh &&
    manager.updater.ingest_version === manager.observed.version;
  return (
    <Section
      layout="stack"
      title="Fleet software inventory"
      description="Observed processes and host updater state across all nodes. Unknown values have not been verified by the host."
    >
      {error && (
        <InlineTip variant="error" role="alert">
          {error}
        </InlineTip>
      )}
      {healthPartial && (
        <InlineTip>Some HAProxy health observations are unavailable.</InlineTip>
      )}
      {!loaded && client && (
        <LoadingStatus delayMs={1000}>Loading fleet software…</LoadingStatus>
      )}
      {!client && (
        <InlineTip>Connect to the manager to inspect fleet software.</InlineTip>
      )}
      {manager && (
        <DescriptionList columns={3}>
          <DescriptionItem label="Manager">
            {manager.observed.version} ({manager.observed.platform}, protocol{" "}
            {manager.observed.protocol})
          </DescriptionItem>
          <DescriptionItem label="Manager signed release match">
            {managerRelease?.publisher || "Unknown"}
          </DescriptionItem>
          <DescriptionItem label="Manager health">Responding</DescriptionItem>
          <DescriptionItem label="Manager SHA-256">
            <code className="cv-haproxy-digest">
              {manager.observed.executable_sha256}
            </code>
          </DescriptionItem>
          <DescriptionItem label="Ingest">
            {manager.updater?.ingest_version || "Unknown"}
          </DescriptionItem>
          <DescriptionItem label="Ingest health">
            {ingestVerified ? "Verified by host updater" : "Unknown"}
          </DescriptionItem>
          <DescriptionItem label="Ingest platform / protocol">
            {ingestVerified
              ? `${manager.observed.platform} / ${manager.observed.protocol}`
              : "Unknown"}
          </DescriptionItem>
          <DescriptionItem label="Ingest SHA-256">
            {ingestVerified && managerRelease ? (
              <code className="cv-haproxy-digest">
                {managerRelease.manager_digests[manager.observed.platform]}
              </code>
            ) : (
              "Unknown"
            )}
          </DescriptionItem>
          <DescriptionItem label="Manager updater">
            {manager.updater?.ready && managerUpdaterFresh
              ? "Ready"
              : "Unavailable or not reporting"}
          </DescriptionItem>
          <DescriptionItem label="Host report last seen">
            {manager.updater?.last_seen
              ? new Date(manager.updater.last_seen).toLocaleString()
              : "Unknown"}
          </DescriptionItem>
          <DescriptionItem label="Manager desired">
            {manager.updates.find((item) => active.has(item.status))
              ?.target_version || "None"}
          </DescriptionItem>
          <DescriptionItem label="Manager previous">
            {manager.updater?.previous_version || "Unknown"}
          </DescriptionItem>
          <DescriptionItem label="Manager update">
            {manager.updates[0]?.status.replaceAll("_", " ") ||
              "No update recorded"}
          </DescriptionItem>
        </DescriptionList>
      )}
      {loaded && (
        <PaginatedDataTable
          aria-label="Fleet software inventory"
          itemLabel="nodes"
          rows={tableRows}
          rowLabel={(row) => row.node.name}
          columns={[
            {
              id: "node",
              header: "Node",
              isRowHeader: true,
              cell: (row) => row.node.name,
            },
            {
              id: "current",
              header: "Agent / HAProxy",
              cell: (row) =>
                `${row.observed?.build?.version || "Unknown"} / ${row.observed?.haproxy_version || "Unknown"}`,
            },
            {
              id: "desired",
              header: "Desired / previous",
              cell: (row) =>
                `${row.latest && active.has(row.latest.status) ? row.latest.target_version : "None"} / ${row.updater?.previous_version || "Unknown"}`,
            },
            {
              id: "platform",
              header: "Platform / protocol",
              cell: (row) =>
                row.observed?.build
                  ? `${row.observed.build.platform} / ${row.observed.build.protocol}`
                  : "Unknown",
            },
            {
              id: "publisher",
              header: "Signed release match",
              cell: (row) => signedBuild(row)?.publisher || "Unknown",
            },
            {
              id: "digest",
              header: "Agent SHA-256",
              cell: (row) =>
                row.observed?.build ? (
                  <code className="cv-haproxy-digest">
                    {row.observed.build.executable_sha256}
                  </code>
                ) : (
                  "Unknown"
                ),
            },
            {
              id: "freshness",
              header: "Observation",
              cell: (row) => currentStatus(row),
            },
            {
              id: "health",
              header: "HAProxy health",
              cell: (row) => healthStatus(row),
            },
            {
              id: "last_seen",
              header: "Last seen",
              cell: (row) =>
                row.observed?.last_seen
                  ? new Date(row.observed.last_seen).toLocaleString()
                  : "Unknown",
            },
            {
              id: "updater",
              header: "Host updater",
              cell: (row) =>
                row.updater?.ready &&
                Date.now() - Date.parse(row.updater.last_seen) < 90_000
                  ? "Ready"
                  : "Unavailable or not reporting",
            },
            {
              id: "status",
              header: "Update status",
              cell: (row) => updateStatus(row),
            },
          ]}
        />
      )}
      {cursor && (
        <Button isLoading={loading} onPress={() => void loadMore()}>
          Load more nodes
        </Button>
      )}
    </Section>
  );
}

export function HAProxySoftware({
  client,
  nodes,
  managerVersion,
}: {
  client: HAProxyClient | null;
  nodes: HAProxyNode[];
  managerVersion?: string | undefined;
}) {
  const [nodeId, setNodeId] = useState("");
  const [action, setAction] = useState<"upgrade" | "rollback">("upgrade");
  const [version, setVersion] = useState("");
  const [releases, setReleases] = useState<HAProxySoftwareRelease[] | null>(
    null,
  );
  const [view, setView] = useState<HAProxySoftwareView | null>(null);
  const [plan, setPlan] = useState<HAProxySoftwarePlan | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [planError, setPlanError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const node = nodes.find((item) => item.id === nodeId);

  useEffect(() => {
    if (!nodes.some((item) => item.id === nodeId))
      setNodeId(nodes[0]?.id ?? "");
  }, [nodes, nodeId]);

  useEffect(() => {
    if (!client) return;
    let alive = true;
    void client
      .softwareReleases()
      .then((rows) => {
        if (alive) setReleases(rows);
      })
      .catch((cause) => {
        if (alive) setError(haproxyErrorMessage(cause));
      });
    return () => {
      alive = false;
    };
  }, [client]);

  useEffect(() => {
    if (!client || !nodeId) return;
    let alive = true;
    const load = () => {
      if (document.hidden) return;
      void client
        .nodeSoftware(nodeId)
        .then((result) => {
          if (alive) setView(result);
        })
        .catch((cause) => {
          if (alive) setError(haproxyErrorMessage(cause));
        });
    };
    setView(null);
    load();
    const timer = window.setInterval(load, 5000);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
  }, [client, nodeId]);

  useEffect(() => {
    if (!client || !nodeId || !version) {
      setPlan(null);
      return;
    }
    let alive = true;
    setPlan(null);
    setPlanError(null);
    void client
      .softwarePlan(nodeId, version, action)
      .then((result) => {
        if (alive) setPlan(result);
      })
      .catch((cause) => {
        if (alive) setPlanError(haproxyErrorMessage(cause));
      });
    return () => {
      alive = false;
    };
  }, [
    client,
    nodeId,
    version,
    action,
    node?.agent?.version,
    view?.updater?.last_seen,
    view?.updates[0]?.status,
  ]);

  const latest = view?.updates[0];
  const pending = view?.updates.find((item) => active.has(item.status));
  const updater = view?.updater;
  const updaterFresh =
    updater && Date.now() - Date.parse(updater.last_seen) < 90_000;

  const request = async () => {
    if (!client || !plan?.eligible || !node || !version || busy) return;
    setBusy(true);
    setError(null);
    try {
      await client.login(true);
      await client.requestSoftware(node.id, {
        request_id: crypto.randomUUID(),
        action,
        version,
        expected_version: plan.current.version,
      });
      setView(await client.nodeSoftware(node.id));
      setPlan(null);
    } catch (cause) {
      setError(haproxyErrorMessage(cause));
    } finally {
      setBusy(false);
    }
  };

  const cancel = async () => {
    if (!client || !node || !pending || pending.status !== "queued" || busy)
      return;
    setBusy(true);
    setError(null);
    try {
      await client.login(true);
      await client.cancelSoftware(node.id, pending.id);
      setView(await client.nodeSoftware(node.id));
    } catch (cause) {
      setError(haproxyErrorMessage(cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Section
      layout="stack"
      title="Signed agent updates"
      description="Choose an exact verified release for one node. The separate host updater checks the bundle and activates it."
    >
      <InlineTip>
        HAProxy keeps serving its current configuration during an agent update.
        Telemetry and management from this node may pause while the agent
        restarts. Fleet updates one node at a time per cluster.
      </InlineTip>
      {error && (
        <InlineTip variant="error" role="alert">
          {error}
        </InlineTip>
      )}
      {!client && (
        <InlineTip>
          Connect to the manager to inspect signed releases.
        </InlineTip>
      )}
      {client && !releases && !error && (
        <LoadingStatus delayMs={1000}>Loading verified releases…</LoadingStatus>
      )}
      {client && releases?.length === 0 && (
        <InlineTip>
          No verified releases are in the host catalog yet. The installation
          administrator can sync or import a signed bundle.
        </InlineTip>
      )}
      {client && nodes.length > 0 && (
        <>
          <Select
            label="Node"
            value={nodeId}
            onChange={(value) => {
              setNodeId(String(value));
              setVersion("");
            }}
            options={nodes.map((item) => ({ id: item.id, label: item.name }))}
          />
          <DescriptionList columns={3}>
            <DescriptionItem label="Manager">
              {managerVersion ?? "Not reported"}
            </DescriptionItem>
            <DescriptionItem label="Agent">
              {node?.agent?.version ?? "Not reported"}
            </DescriptionItem>
            <DescriptionItem label="HAProxy">
              {node?.haproxy_version ?? "Not reported"}
            </DescriptionItem>
            <DescriptionItem label="Host updater">
              {updaterFresh && updater?.ready
                ? "Ready"
                : updater
                  ? "Unavailable or busy"
                  : "Not installed or not reporting"}
            </DescriptionItem>
            <DescriptionItem label="Retained previous release">
              {updater?.previous_version || "Not reported"}
            </DescriptionItem>
            <DescriptionItem label="Desired update">
              {pending
                ? `${pending.action} to ${pending.target_version}`
                : "None"}
            </DescriptionItem>
          </DescriptionList>
          {pending && (
            <InlineTip
              variant={pending.status === "needs_attention" ? "error" : "info"}
            >
              {pending.target_version}: {pending.status.replaceAll("_", " ")}
              {pending.error_code ? ` (${pending.error_code})` : ""}.
              {pending.status === "needs_attention"
                ? " Inspect the host updater log and recover the retained release before another change."
                : ""}
            </InlineTip>
          )}
          {pending?.status === "queued" && (
            <Button onPress={() => void cancel()} isDisabled={busy}>
              Cancel queued update
            </Button>
          )}
          {latest && !pending && (
            <p>
              Latest update:{" "}
              <Badge
                tone={latest.status === "succeeded" ? "success" : "warning"}
              >
                {latest.status.replaceAll("_", " ")}
              </Badge>{" "}
              {latest.expected_version} → {latest.target_version}
            </p>
          )}
          <Select
            label="Change"
            value={action}
            onChange={(value) => {
              setAction(String(value) as "upgrade" | "rollback");
              setVersion("");
            }}
            options={[
              { id: "upgrade", label: "Upgrade agent" },
              { id: "rollback", label: "Roll back to retained release" },
            ]}
            isDisabled={!!pending || busy}
          />
          <Select
            label="Signed release"
            value={version}
            onChange={(value) => setVersion(String(value))}
            options={[
              { id: "", label: "Choose a verified release" },
              ...(releases ?? []).map((item) => ({
                id: item.version,
                label: item.version,
              })),
            ]}
            isDisabled={!!pending || busy || !releases?.length}
          />
          {releases?.find((release) => release.version === version) && (
            <SignedReleaseDetails
              release={releases.find((release) => release.version === version)!}
              platform={node?.agent?.platform}
              role="agent"
            />
          )}
          {planError && (
            <InlineTip variant="error" role="alert">
              {planError}
            </InlineTip>
          )}
          {plan && (
            <>
              <InlineTip variant={plan.eligible ? "info" : "warning"}>
                {plan.eligible
                  ? "Ready to request this exact release."
                  : (plan.reason ?? "This update is blocked.")}
              </InlineTip>
              <p>
                {plan.traffic_impact}. {plan.management_impact}.
              </p>
              <p>
                Publisher: {plan.release.publisher}. Manager compatibility:{" "}
                {plan.release.compatibility.manager.min}–
                {plan.release.compatibility.manager.max}.
              </p>
            </>
          )}
          <Button
            onPress={() => void request()}
            isDisabled={busy || !!pending || !plan?.eligible}
          >
            {busy
              ? "Requesting…"
              : action === "rollback"
                ? "Request rollback"
                : "Request agent update"}
          </Button>
        </>
      )}
    </Section>
  );
}

export function HAProxyManagerSoftware({
  client,
}: {
  client: HAProxyClient | null;
}) {
  const [releases, setReleases] = useState<HAProxySoftwareRelease[]>([]);
  const [view, setView] = useState<HAProxyInstallationSoftwareView | null>(
    null,
  );
  const [action, setAction] = useState<"upgrade" | "rollback">("upgrade");
  const [version, setVersion] = useState("");
  const [plan, setPlan] = useState<HAProxyInstallationSoftwarePlan | null>(
    null,
  );
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!client) return;
    let alive = true;
    void client
      .softwareReleases()
      .then((items) => {
        if (alive) setReleases(items);
      })
      .catch((cause) => {
        if (alive) setError(haproxyErrorMessage(cause));
      });
    const load = () => {
      if (document.hidden) return;
      void client
        .installationSoftware()
        .then((result) => {
          if (alive) setView(result);
        })
        .catch((cause) => {
          if (alive) setError(haproxyErrorMessage(cause));
        });
    };
    load();
    const timer = window.setInterval(load, 5000);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
  }, [client]);

  useEffect(() => {
    if (!client || !version) {
      setPlan(null);
      return;
    }
    let alive = true;
    setPlan(null);
    void client
      .installationSoftwarePlan(version, action)
      .then((result) => {
        if (alive) setPlan(result);
      })
      .catch((cause) => {
        if (alive) setError(haproxyErrorMessage(cause));
      });
    return () => {
      alive = false;
    };
  }, [
    client,
    version,
    action,
    view?.observed.version,
    view?.updater?.last_seen,
    view?.updates[0]?.status,
  ]);

  const pending = view?.updates.find((item) => active.has(item.status));
  const updaterFresh =
    view?.updater && Date.now() - Date.parse(view.updater.last_seen) < 90_000;
  const request = async () => {
    if (!client || !plan?.eligible || busy) return;
    setBusy(true);
    setError(null);
    try {
      await client.login(true);
      await client.requestInstallationSoftware({
        request_id: crypto.randomUUID(),
        action,
        version,
        expected_version: plan.current.version,
      });
      setView(await client.installationSoftware());
      setPlan(null);
    } catch (cause) {
      setError(haproxyErrorMessage(cause));
    } finally {
      setBusy(false);
    }
  };
  const cancel = async () => {
    if (!client || !pending || pending.status !== "queued" || busy) return;
    setBusy(true);
    setError(null);
    try {
      await client.login(true);
      await client.cancelInstallationSoftware(pending.id);
      setView(await client.installationSoftware());
    } catch (cause) {
      setError(haproxyErrorMessage(cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Section
      layout="stack"
      title="Manager and ingest updates"
      description="Choose an exact signed Fleet release for the installation control plane."
    >
      <InlineTip variant="warning">
        Manager, ingest, this console, and telemetry pause during the switch.
        HAProxy nodes continue serving their last deployed configuration.
      </InlineTip>
      {error && (
        <InlineTip variant="error" role="alert">
          {error}
        </InlineTip>
      )}
      {!client && (
        <InlineTip>Connect to the manager to inspect its software.</InlineTip>
      )}
      {client && !view && !error && (
        <LoadingStatus delayMs={1000}>Loading manager software…</LoadingStatus>
      )}
      {view && (
        <>
          <DescriptionList columns={3}>
            <DescriptionItem label="Manager">
              {view.observed.version}
            </DescriptionItem>
            <DescriptionItem label="Ingest">
              {view.updater?.ingest_version || "Not reported"}
            </DescriptionItem>
            <DescriptionItem label="Host updater">
              {updaterFresh && view.updater?.ready
                ? "Ready"
                : "Unavailable or busy"}
            </DescriptionItem>
            <DescriptionItem label="Retained previous release">
              {view.updater?.previous_version || "Not reported"}
            </DescriptionItem>
            <DescriptionItem label="Desired update">
              {pending
                ? `${pending.action} to ${pending.target_version}`
                : "None"}
            </DescriptionItem>
          </DescriptionList>
          {pending && (
            <InlineTip
              variant={pending.status === "needs_attention" ? "error" : "info"}
            >
              {pending.target_version}: {pending.status.replaceAll("_", " ")}
              {pending.error_code ? ` (${pending.error_code})` : ""}.
              {pending.status === "needs_attention"
                ? " Inspect the host updater log before another change."
                : ""}
            </InlineTip>
          )}
          {pending?.status === "queued" && (
            <Button onPress={() => void cancel()} isDisabled={busy}>
              Cancel queued update
            </Button>
          )}
          {view.updates[0] && !pending && (
            <p>
              Latest update:{" "}
              <Badge
                tone={
                  view.updates[0].status === "succeeded" ? "success" : "warning"
                }
              >
                {view.updates[0].status.replaceAll("_", " ")}
              </Badge>{" "}
              {view.updates[0].expected_version} →{" "}
              {view.updates[0].target_version}
            </p>
          )}
          <Select
            label="Change"
            value={action}
            onChange={(value) => {
              setAction(String(value) as "upgrade" | "rollback");
              setVersion("");
            }}
            options={[
              { id: "upgrade", label: "Upgrade manager and ingest" },
              { id: "rollback", label: "Roll back to retained release" },
            ]}
            isDisabled={!!pending || busy}
          />
          <Select
            label="Signed release"
            value={version}
            onChange={(value) => setVersion(String(value))}
            options={[
              { id: "", label: "Choose a verified release" },
              ...releases.map((item) => ({
                id: item.version,
                label: item.version,
              })),
            ]}
            isDisabled={!!pending || busy || !releases.length}
          />
          {releases.find((release) => release.version === version) && (
            <SignedReleaseDetails
              release={releases.find((release) => release.version === version)!}
              platform={view?.observed.platform}
              role="manager"
            />
          )}
          {plan && (
            <>
              <InlineTip variant={plan.eligible ? "info" : "warning"}>
                {plan.eligible
                  ? `Ready with ${plan.agent_count} compatible agent${plan.agent_count === 1 ? "" : "s"}.`
                  : plan.reason || "This update is blocked."}
              </InlineTip>
              <p>
                {plan.traffic_impact}. {plan.management_impact}.
              </p>
              <p>
                Publisher: {plan.release.publisher}. Agent compatibility:{" "}
                {plan.release.compatibility.agent.min}–
                {plan.release.compatibility.agent.max}.
              </p>
            </>
          )}
          <Button
            onPress={() => void request()}
            isDisabled={busy || !!pending || !plan?.eligible}
          >
            {busy
              ? "Requesting…"
              : action === "rollback"
                ? "Request manager rollback"
                : "Request manager update"}
          </Button>
        </>
      )}
    </Section>
  );
}
