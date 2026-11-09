import { useEffect, useState } from "react";
import {
  Badge,
  Button,
  DescriptionItem,
  DescriptionList,
  InlineTip,
  LoadingStatus,
  Section,
  Select,
} from "@ecraela/ui";
import {
  haproxyErrorMessage,
  type HAProxyClient,
  type HAProxyNode,
  type HAProxySoftwarePlan,
  type HAProxySoftwareRelease,
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
