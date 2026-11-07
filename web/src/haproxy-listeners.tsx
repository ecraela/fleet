import { ConnectionIssue } from "./connection-health";
import { useEffect, useRef, useState } from "react";
import {
  Badge,
  Button,
  PaginatedDataTable,
  DialogForm,
  Drawer,
  DrawerBody,
  DrawerDialog,
  DrawerFooter,
  EmptyState,
  InlineTip,
  LoadingStatus,
  MenuItem,
  Section,
  Select,
  Switch,
  TextField,
} from "@ecraela/ui";
import {
  HAProxyClient,
  haproxyErrorMessage,
  type HAProxyCluster,
  type HAProxyConfigurationView,
  type HAProxyNodeDiscovery,
  type HAProxyHTTPSListener,
  type HAProxyListenerRevision,
  type HAProxyTLSCertificate,
} from "./haproxy-api";
import { configurationControlReason } from "./haproxy-configuration";
import { isActionCancelled, usePermissionAction } from "./permissions";
import { rolloutStatus } from "./haproxy-rollouts";

function covers(c: HAProxyTLSCertificate, hostname: string): boolean {
  return c.dns_names.some(
    (name) =>
      name.toLowerCase() === hostname ||
      (name.startsWith("*.") &&
        hostname.split(".").length === name.split(".").length &&
        hostname.endsWith(name.slice(1).toLowerCase())),
  );
}
export function sharedCertificates(
  views: Array<Pick<HAProxyConfigurationView, "control">>,
  now = Date.now(),
): Array<
  HAProxyTLSCertificate & {
    reason?: string;
    node_certificates?: Record<string, string>;
  }
> {
  const states = views.map((v) => v.control.discovery?.configuration);
  return (states[0]?.tls_certificates ?? []).map((c) => {
    const copies = states.map((s) =>
      s?.tls_certificates?.find((other) => other.reference === c.reference),
    );
    const separate = c.reference.startsWith("acme-");
    const pins = separate
      ? Object.fromEntries(
          copies.map((other, i) => [
            views[i]!.control.node_id,
            other?.sha256 ?? "",
          ]),
        )
      : undefined;
    const reason = copies.some(
      (other) => !other || (!separate && other.sha256 !== c.sha256),
    )
      ? "The same certificate identity must be available on every node."
      : copies.some(
            (other) =>
              other!.error_code !== "" ||
              other!.not_before_ms > now ||
              other!.not_after_ms <= now + 60_000,
          )
        ? "This certificate is unavailable, invalid or outside its validity period."
        : states.some((s) =>
              s?.document?.applications?.some((a) => !covers(c, a.hostname)),
            )
          ? "This certificate must cover every application hostname on this listener."
          : undefined;
    return {
      ...c,
      ...(pins ? { node_certificates: pins } : {}),
      ...(reason ? { reason } : {}),
    };
  });
}
function certificateMatches(
  c: HAProxyTLSCertificate & { node_certificates?: Record<string, string> },
  l: HAProxyHTTPSListener,
): boolean {
  return l.node_certificates
    ? !!c.node_certificates &&
        Object.entries(l.node_certificates).every(
          ([id, hash]) => c.node_certificates?.[id] === hash,
        )
    : c.sha256 === l.certificate_sha256;
}
export function httpsListenerDraft(
  enabled: boolean,
  binding: Pick<HAProxyHTTPSListener, "name" | "address" | "port"> | undefined,
  certificate: ReturnType<typeof sharedCertificates>[number] | undefined,
): HAProxyHTTPSListener[] | null {
  if (!enabled) return [];
  if (!binding || !certificate || certificate.reason) return null;
  return [
    {
      ...binding,
      certificate_ref: certificate.reference,
      certificate_sha256: certificate.node_certificates
        ? ""
        : certificate.sha256,
      ...(certificate.node_certificates
        ? { node_certificates: certificate.node_certificates }
        : {}),
    },
  ];
}

const expiry = (c?: HAProxyTLSCertificate) =>
  c?.not_after_ms
    ? new Date(c.not_after_ms).toLocaleDateString(undefined, {
        year: "numeric",
        month: "short",
        day: "numeric",
      })
    : "Unavailable";

function freshTLSView(view?: HAProxyNodeDiscovery): boolean {
  const state = view?.control.discovery?.configuration;
  return (
    !!view?.fresh &&
    view.connected &&
    !!state?.control_allowed &&
    state.error_code === "" &&
    Date.now() - state.observed_at_ms <= 6000 &&
    Date.now() - state.observed_at_ms >= -60000
  );
}

export function HAProxyListeners({
  cluster,
  nodeIds,
  client,
  permissionReason,
}: {
  cluster: HAProxyCluster;
  nodeIds: string[];
  client: HAProxyClient;
  permissionReason?: string;
}) {
  const { runAction } = usePermissionAction();
  const [records, setRecords] = useState<HAProxyListenerRevision[] | null>(
    null,
  );
  const [views, setViews] = useState<HAProxyNodeDiscovery[]>([]);
  const [current, setCurrent] = useState<HAProxyHTTPSListener[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [editor, setEditor] = useState(false);
  const [history, setHistory] = useState(false);
  const [reference, setReference] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [busy, setBusy] = useState(false);
  const busyRef = useRef(false);
  const lifetime = useRef<AbortController | null>(null);
  const refresh = useRef<() => Promise<void>>(async () => {});
  const nodeKey = [...nodeIds].sort().join(",");
  useEffect(() => {
    const controller = new AbortController();
    lifetime.current = controller;
    let pending = false;
    const update = async () => {
      if (
        pending ||
        controller.signal.aborted ||
        document.visibilityState === "hidden"
      )
        return;
      pending = true;
      try {
        const [rows, configs] = await Promise.all([
          client.listeners(cluster.id, controller.signal),
          Promise.all(
            nodeKey
              .split(",")
              .filter(Boolean)
              .slice(0, 12)
              .map((id) => client.discovery(id, controller.signal)),
          ),
        ]);
        if (!controller.signal.aborted) {
          setRecords(rows);
          setViews(configs);
          const document =
            configs[0]?.control.discovery?.configuration?.document;
          if (document) setCurrent(document.listeners ?? []);
          setError(null);
        }
      } catch (cause) {
        if (!controller.signal.aborted) setError(haproxyErrorMessage(cause));
      } finally {
        pending = false;
      }
    };
    refresh.current = update;
    void update();
    const timer = setInterval(() => void update(), 2000);
    document.addEventListener("visibilitychange", update);
    return () => {
      controller.abort();
      clearInterval(timer);
      document.removeEventListener("visibilitychange", update);
    };
  }, [client, cluster.id, nodeKey]);
  const latest = records?.[0];
  const states = views.map((v) => v.control.discovery?.configuration);
  const binding = states[0]?.listener_bindings?.find(
    (b) =>
      b.name === cluster.topology?.frontend &&
      states.every((s) =>
        s?.listener_bindings?.some(
          (other) =>
            other.name === b.name &&
            other.address === b.address &&
            other.port === b.port,
        ),
      ),
  );
  const reason =
    permissionReason ??
    (!cluster.topology?.control
      ? "Bind an enrolled entrypoint to deploy HTTPS listeners."
      : nodeIds.length < 2 || nodeIds.length > 12
        ? "HTTPS rolling deployment requires 2–12 enrolled nodes."
        : latest?.status === "running" || latest?.status === "rolling_back"
          ? "Wait for the current listener deployment to finish."
          : views.some(
                (v) =>
                  v.control.active_rollout_id || v.control.maintenance_blocked,
              )
            ? "Finish the active cluster deployment or maintenance before changing HTTPS."
            : (latest?.number ?? 0) >= 1000000
              ? "This cluster has reached its listener version limit."
              : views.some((v) => !freshTLSView(v))
                ? "Wait for fresh configuration from every node before changing HTTPS."
                : views.length !== nodeIds.length ||
                    states.some((s) => s?.listener_control_version !== 1)
                  ? "Enable a local TLS policy on every node’s agent to manage HTTPS."
                  : !binding
                    ? "Grant the same HTTPS binding for the cluster frontend on every node."
                    : undefined);
  const certificates = sharedCertificates(views);
  const begin = () => {
    setActionError(null);
    setEnabled(true);
    setReference(
      current[0]?.certificate_ref ??
        certificates.find((c) => !c.reason)?.reference ??
        "",
    );
    setEditor(true);
  };
  const deploy = async (
    listeners: HAProxyHTTPSListener[],
    rollback?: HAProxyListenerRevision,
  ) => {
    if (busyRef.current || reason) return;
    busyRef.current = true;
    setBusy(true);
    setActionError(null);
    const signal = lifetime.current!.signal;
    try {
      await runAction(async () => {
        await client.login(true);
        signal.throwIfAborted();
        const configs = await Promise.all(
          nodeIds.map((id) => client.configuration(id, signal)),
        );
        for (const config of configs) {
          const blocked = configurationControlReason(config);
          if (blocked) throw new Error(blocked);
        }
        for (const l of listeners) {
          const cert = sharedCertificates(configs).find(
            (c) =>
              c.reference === l.certificate_ref && certificateMatches(c, l),
          );
          if (!cert || cert.reason)
            throw new Error(
              cert?.reason ??
                "Certificate inventory changed. Refresh and try again.",
            );
        }
        const view = await client.maintenance(nodeIds[0]!, signal);
        const d = view.controller?.discovery,
          c = d?.configuration;
        if (
          view.reason_code ||
          !d ||
          !c ||
          view.controller_node_id !== cluster.topology?.control?.node_id
        )
          throw new Error(
            "Wait for a healthy managed entrypoint and fresh configuration.",
          );
        await client.deployListeners(
          cluster.id,
          {
            request_id: crypto.randomUUID(),
            expected_revision: latest?.number ?? 0,
            listeners,
            nodes: configs.map((config) => ({
              node_id: config.control.node_id,
              expected_generation: config.control.discovery!.generation,
              expected_artifact_id:
                config.control.discovery!.configuration!.artifact_id,
              expected_version:
                config.control.discovery!.configuration!.version,
            })),
            entrypoint: {
              node_id: view.controller_node_id!,
              binding_id: view.binding_id!,
              expected_generation: d.generation,
              expected_artifact_id: c.artifact_id,
              expected_version: c.version,
            },
          },
          rollback?.id,
        );
      });
      if (!signal.aborted) {
        setEditor(false);
        setHistory(false);
        setNotice(
          "HTTPS deployment started. Follow each node under Rolling configuration.",
        );
        await refresh.current();
      }
    } catch (cause) {
      if (!signal.aborted && !isActionCancelled(cause))
        setActionError(
          cause instanceof Error && !("status" in cause)
            ? cause.message
            : haproxyErrorMessage(cause),
        );
    } finally {
      busyRef.current = false;
      if (!lifetime.current?.signal.aborted) setBusy(false);
    }
  };
  const selected = certificates.find((c) => c.reference === reference);
  const save = () => {
    const listeners = httpsListenerDraft(enabled, binding, selected);
    if (listeners) void deploy(listeners);
  };
  return (
    <Section
      layout="stack"
      size="sm"
      title="HTTPS listeners"
      description="Terminate HTTPS on your HAProxy nodes. Certificates and private keys stay in your infrastructure; each change drains, updates and rejoins one node at a time."
      actions={
        <>
          <Button
            disabledReason={
              !records?.length ? "No listener versions yet." : undefined
            }
            onPress={() => {
              setActionError(null);
              setHistory(true);
            }}
          >
            Listener history
          </Button>
          <Button disabledReason={reason} onPress={begin}>
            {current.length ? "Edit HTTPS listener" : "Add HTTPS listener"}
          </Button>
        </>
      }
    >
      <div className="cv-haproxy-configuration">
        {error && (
          <ConnectionIssue message="Listener details: refresh interrupted; retrying automatically." />
        )}
        {notice && <InlineTip role="status">{notice}</InlineTip>}
        {views.some((v) => !freshTLSView(v)) && (
          <InlineTip variant="warning">
            Waiting for fresh node configuration. Last observed HTTPS listeners
            remain visible.
          </InlineTip>
        )}
        {actionError && !editor && !history && (
          <InlineTip role="alert" variant="error">
            {actionError}
          </InlineTip>
        )}
        {!records && !error ? (
          <LoadingStatus delayMs={1000}>Loading HTTPS listeners…</LoadingStatus>
        ) : !current.length ? (
          <EmptyState
            size="sm"
            title="HTTPS is not enabled"
            description="Install certificate files and a TLS policy on each node, then select a shared certificate here. DNS and public networking are configured in your infrastructure."
          />
        ) : (
          <PaginatedDataTable
            aria-label="HAProxy HTTPS listeners"
            itemLabel="listeners"
            rows={current.map((l) => ({ ...l, id: l.name }))}
            rowLabel={(l) => l.name}
            columns={[
              {
                id: "name",
                header: "Listener",
                isRowHeader: true,
                cell: (l) => `${l.name} – ${l.address}:${l.port}`,
              },
              {
                id: "certificate",
                header: "Certificate",
                cell: (l) => l.certificate_ref,
              },
              {
                id: "expiry",
                header: "Expires",
                cell: (l) =>
                  expiry(
                    states[0]?.tls_certificates?.find(
                      (c) => c.reference === l.certificate_ref,
                    ),
                  ),
              },
              {
                id: "loaded",
                header: "Verified nodes",
                cell: (l) =>
                  `${states.filter((s, i) => freshTLSView(views[i]) && s?.tls_listeners?.some((o) => o.name === l.name && o.loaded && o.sha256 === (l.node_certificates?.[views[i]!.control.node_id] ?? l.certificate_sha256))).length} / ${nodeIds.length}`,
              },
            ]}
          />
        )}
        {latest && (
          <InlineTip>
            Latest deployment: version {latest.number} –{" "}
            {rolloutStatus(latest.status)}. Node verification checks the loaded
            certificate identity; public reachability and trust are checked
            separately.
          </InlineTip>
        )}
        <Drawer
          size="lg"
          isOpen={editor}
          onOpenChange={(open) => {
            if (!open && !busy) setEditor(false);
          }}
          isDismissable={!busy}
        >
          <DrawerDialog
            title={
              current.length ? "Edit HTTPS listener" : "Add HTTPS listener"
            }
            hideCloseButton={busy}
          >
            <DialogForm onSubmit={save}>
              <DrawerBody>
                {actionError && (
                  <InlineTip role="alert" variant="error">
                    {actionError}
                  </InlineTip>
                )}
                <Switch
                  label="HTTPS enabled"
                  isSelected={enabled}
                  onChange={setEnabled}
                  disabledReason={
                    permissionReason ??
                    (busy
                      ? "Wait for the listener deployment request to finish."
                      : undefined)
                  }
                  description={
                    enabled
                      ? "Serve HTTPS for applications on this listener. Changes take effect when you deploy."
                      : "Deploying will stop HTTPS for all applications on this listener. HTTP and certificate files are kept."
                  }
                />
                <TextField
                  label="Listener"
                  value={
                    binding
                      ? `${binding.name} – ${binding.address}:${binding.port}`
                      : "No shared binding"
                  }
                  isReadOnly
                  description="This address and port are granted by the TLS policy on each node."
                />
                {enabled && (
                  <>
                    <Select
                      label="Certificate reference"
                      value={reference}
                      onChange={(key) => setReference(String(key ?? ""))}
                      isDisabled={busy}
                      isRequired
                      options={certificates.map((c) => ({
                        id: c.reference,
                        label: c.reference,
                        description:
                          c.reason ??
                          `Expires ${expiry(c)} – ${c.dns_names.join(", ")}`,
                        isDisabled: !!c.reason,
                      }))}
                    />
                    {selected && (
                      <>
                        <TextField
                          label="Hostnames"
                          value={selected.dns_names.join(", ")}
                          isReadOnly
                        />
                        <TextField
                          label={
                            selected.node_certificates
                              ? "Node certificate SHA-256"
                              : "Certificate SHA-256"
                          }
                          value={selected.sha256}
                          isReadOnly
                          description={
                            selected.node_certificates
                              ? "Shown for one node. Every node’s separate certificate fingerprint is verified during deployment; private keys stay on their nodes."
                              : "The same immutable certificate identity is required on every node."
                          }
                        />
                      </>
                    )}
                    <InlineTip>
                      HTTPS requires SNI and TLS 1.2 or newer. Only matching
                      application hostname and path routes are served; unknown
                      routes return 404. Keep old certificate files for
                      rollback.
                    </InlineTip>
                  </>
                )}
                {reason && <InlineTip variant="warning">{reason}</InlineTip>}
              </DrawerBody>
              <DrawerFooter>
                <Button
                  variant={enabled ? "primary" : "danger"}
                  type="submit"
                  isLoading={busy}
                  disabledReason={
                    reason ??
                    (enabled
                      ? (selected?.reason ??
                        (!selected
                          ? "Choose an available certificate."
                          : undefined))
                      : undefined)
                  }
                >
                  Deploy changes
                </Button>
                <Button isDisabled={busy} onPress={() => setEditor(false)}>
                  Cancel
                </Button>
              </DrawerFooter>
            </DialogForm>
          </DrawerDialog>
        </Drawer>
        <Drawer
          size="lg"
          isOpen={history}
          onOpenChange={(open) => {
            if (!open && !busy) setHistory(false);
          }}
          isDismissable={!busy}
        >
          <DrawerDialog title="HTTPS listener history" hideCloseButton={busy}>
            <DrawerBody>
              {actionError && (
                <InlineTip role="alert" variant="error">
                  {actionError}
                </InlineTip>
              )}
              <InlineTip>
                Restoring creates a new rolling deployment and preserves current
                applications and backends. The old certificate must still be
                present and valid on every node.
              </InlineTip>
              <PaginatedDataTable
                aria-label="HTTPS listener versions"
                itemLabel="versions"
                rows={records ?? []}
                rowLabel={(r) => `Version ${r.number}`}
                columns={[
                  {
                    id: "number",
                    header: "Version",
                    isRowHeader: true,
                    cell: (r) => `Version ${r.number}`,
                  },
                  {
                    id: "certificate",
                    header: "Certificates",
                    cell: (r) =>
                      r.listeners.map((l) => l.certificate_ref).join(", ") ||
                      "HTTPS disabled",
                  },
                  {
                    id: "status",
                    header: "Deployment",
                    cell: (r) => (
                      <Badge
                        tone={
                          r.status === "succeeded"
                            ? "success"
                            : r.status === "rollback_failed"
                              ? "error"
                              : "neutral"
                        }
                      >
                        {rolloutStatus(r.status)}
                      </Badge>
                    ),
                  },
                  {
                    id: "created",
                    header: "Created",
                    cell: (r) => new Date(r.created_at).toLocaleString(),
                  },
                ]}
                rowActions={(r) => (
                  <MenuItem
                    disabledReason={
                      reason ??
                      (busy
                        ? "Wait for the current HTTPS change to finish."
                        : r.id === latest?.id
                          ? "This is already the latest listener version."
                          : r.status !== "succeeded"
                            ? "Only successfully deployed listener versions can be restored."
                            : r.listeners.some(
                                  (l) =>
                                    !certificates.some(
                                      (c) =>
                                        c.reference === l.certificate_ref &&
                                        certificateMatches(c, l) &&
                                        !c.reason,
                                    ),
                                )
                              ? "This version’s matching certificates must be valid and available on every node."
                              : undefined)
                    }
                    onAction={() => void deploy(r.listeners, r)}
                  >
                    Restore version {r.number}
                  </MenuItem>
                )}
              />
            </DrawerBody>
            <DrawerFooter>
              <Button isDisabled={busy} onPress={() => setHistory(false)}>
                Close
              </Button>
            </DrawerFooter>
          </DrawerDialog>
        </Drawer>
      </div>
    </Section>
  );
}
