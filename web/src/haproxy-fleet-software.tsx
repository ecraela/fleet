import { useEffect, useState } from "react";
import {
  Button,
  DescriptionItem,
  DescriptionList,
  InlineTip,
  LoadingStatus,
  Section,
  Select,
} from "@ecraela/ui";
import {
  HAProxyError,
  haproxyErrorMessage,
  type HAProxyClient,
  type HAProxyFleetSoftwarePlan,
  type HAProxyFleetSoftwareView,
  type HAProxySoftwareRelease,
} from "./haproxy-api";
import { HAProxyManagerSoftware } from "./haproxy-software";

export function HAProxyFleetSoftware({
  client,
}: {
  client: HAProxyClient | null;
}) {
  const [releases, setReleases] = useState<HAProxySoftwareRelease[]>([]);
  const [catalogLoaded, setCatalogLoaded] = useState(false);
  const [catalogError, setCatalogError] = useState<string | null>(null);
  const [view, setView] = useState<HAProxyFleetSoftwareView | null>(null);
  const [legacyManager, setLegacyManager] = useState(false);
  const [version, setVersion] = useState("");
  const [plan, setPlan] = useState<HAProxyFleetSoftwarePlan | null>(null);
  const [viewError, setViewError] = useState<string | null>(null);
  const [requestError, setRequestError] = useState<string | null>(null);
  const [planError, setPlanError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const latest = view?.updates[0];
  const running = latest?.status === "queued" || latest?.status === "running";
  const selectedPlan = plan?.release.version === version ? plan : null;

  useEffect(() => {
    setView(null);
    setLegacyManager(false);
    setReleases([]);
    setCatalogLoaded(false);
    setCatalogError(null);
    setViewError(null);
    setRequestError(null);
    if (!client) return;
    let alive = true;
    void client
      .softwareReleases()
      .then((rows) => {
        if (alive) {
          setReleases(rows);
          setCatalogLoaded(true);
        }
      })
      .catch((cause) => {
        if (alive) setCatalogError(haproxyErrorMessage(cause));
      });
    const refresh = () => {
      if (document.hidden) return;
      void client
        .fleetSoftware()
        .then((result) => {
          if (!alive) return;
          setView(result);
          setLegacyManager(false);
          setViewError(null);
        })
        .catch((cause) => {
          if (!alive) return;
          if (cause instanceof HAProxyError && cause.status === 404) {
            setLegacyManager(true);
            setViewError(null);
          } else {
            setViewError(haproxyErrorMessage(cause));
          }
        });
    };
    refresh();
    const timer = window.setInterval(refresh, 5000);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
  }, [client]);

  useEffect(() => {
    setPlan(null);
    setPlanError(null);
    if (!client || !version || running) return;
    let alive = true;
    void client
      .fleetSoftwarePlan(version)
      .then((result) => {
        if (alive) setPlan(result);
      })
      .catch((cause) => {
        if (alive) setPlanError(haproxyErrorMessage(cause));
      });
    return () => {
      alive = false;
    };
  }, [client, version, running, latest?.status]);

  const request = async () => {
    if (
      !client ||
      !selectedPlan?.eligible ||
      !selectedPlan.fingerprint ||
      running ||
      busy
    )
      return;
    setBusy(true);
    setRequestError(null);
    try {
      await client.login(true);
      await client.requestFleetSoftware({
        request_id: crypto.randomUUID(),
        version,
        fingerprint: selectedPlan.fingerprint,
      });
      setView(await client.fleetSoftware());
      setPlan(null);
    } catch (cause) {
      setRequestError(haproxyErrorMessage(cause));
    } finally {
      setBusy(false);
    }
  };

  const currentStep = latest?.steps[latest.next_step];
  const agentChanges =
    selectedPlan?.steps.filter((step) => step.role === "agent").length ?? 0;

  if (legacyManager) {
    return (
      <>
        <Section layout="stack" title="Fleet software">
          <InlineTip>
            This manager needs a two-step update before it can coordinate the
            whole fleet. Install v0.1.0-rc.23 first, then v0.1.0-rc.24. Once
            rc.24 is running, this page will show the fleet-wide release picker.
          </InlineTip>
        </Section>
        <HAProxyManagerSoftware client={client} bridgeOnly />
      </>
    );
  }

  return (
    <Section
      className="cv-haproxy-software"
      layout="stack"
      title="Fleet software"
      description="Choose one verified release for the manager, ingest and every agent in this installation."
    >
      {!client && (
        <InlineTip>Connect to the manager to inspect its software.</InlineTip>
      )}
      {client && !view && !viewError && (
        <LoadingStatus delayMs={1000}>Loading Fleet software…</LoadingStatus>
      )}
      {viewError && (
        <InlineTip variant="error" role="alert">
          {viewError}
        </InlineTip>
      )}
      {requestError && (
        <InlineTip variant="error" role="alert">
          {requestError}
        </InlineTip>
      )}
      {latest && (
        <InlineTip variant={latest.status === "paused" ? "error" : "info"}>
          {latest.status === "succeeded"
            ? `All hosts completed ${latest.target_version}.`
            : latest.status === "paused"
              ? `Update to ${latest.target_version} paused at ${currentStep?.name ?? "a host"}: ${latest.reason ?? "inspect the host updater"}`
              : `Updating to ${latest.target_version}: ${latest.next_step} of ${latest.steps.length} hosts complete. ${currentStep?.name ?? "Next host"}: ${view?.current_update?.status?.replaceAll("_", " ") ?? "queued"}.`}
        </InlineTip>
      )}
      <div className="cv-haproxy-fleet-controls">
        <Select
          label="Desired signed release"
          value={version}
          onChange={(value) => setVersion(String(value))}
          options={[
            { id: "", label: "Choose a verified release" },
            ...releases.map((release) => ({
              id: release.version,
              label: release.version,
            })),
          ]}
          isDisabled={!client || running || busy || releases.length === 0}
        />
        <Button
          onPress={() => void request()}
          isDisabled={
            !selectedPlan?.eligible ||
            !selectedPlan.fingerprint ||
            running ||
            busy
          }
        >
          {busy ? "Scheduling…" : "Set desired release"}
        </Button>
      </div>
      {catalogError && (
        <InlineTip variant="warning">
          No verified releases are available from this manager. Managed updates
          require a signed release catalog and host updaters.
        </InlineTip>
      )}
      {catalogLoaded && releases.length === 0 && (
        <InlineTip>No verified releases are available to select.</InlineTip>
      )}
      {planError && (
        <InlineTip variant="error" role="alert">
          {planError}
        </InlineTip>
      )}
      {selectedPlan && (
        <>
          <DescriptionList columns={3}>
            <DescriptionItem label="Manager now">
              {selectedPlan.manager.version}
            </DescriptionItem>
            <DescriptionItem label="Agents">
              {selectedPlan.node_count}
            </DescriptionItem>
            <DescriptionItem label="Hosts to update">
              {selectedPlan.steps.length} ({agentChanges} agents)
            </DescriptionItem>
          </DescriptionList>
          {selectedPlan.eligible ? (
            <InlineTip variant="warning">
              {selectedPlan.management_impact} Updates run in order and pause if
              a host fails.
            </InlineTip>
          ) : (
            <InlineTip variant="warning">
              {selectedPlan.reason ?? "This release cannot be installed yet."}
            </InlineTip>
          )}
          <p>
            Verified publisher: {selectedPlan.release.publisher}. Manifest:{" "}
            <code className="cv-haproxy-digest">
              {selectedPlan.release.manifest_sha256}
            </code>
          </p>
        </>
      )}
    </Section>
  );
}
