import { useEffect, useRef, useState } from "react";
import {
  Badge,
  Button,
  PaginatedDataTable,
  DescriptionList,
  DescriptionItem,
  DialogBody,
  DialogForm,
  Drawer,
  DrawerBody,
  DrawerDialog,
  DrawerFooter,
  InlineTip,
  LoadingStatus,
  Section,
  Select,
  TextField,
  Switch,
} from "@ecraela/ui";
import {
  HAProxyClient,
  haproxyErrorMessage,
  type HAProxyACMEView,
  type HAProxyACMEPlan,
  type HAProxyACMEInput,
  type HAProxyCluster,
  type HAProxyApplicationRevision,
  type HAProxyApplicationHealth,
} from "./haproxy-api";
import { HAProxyConnectDNS } from "./haproxy-connect-dns";
import { isActionCancelled, usePermissionAction } from "./permissions";
import { rolloutStatus } from "./haproxy-rollouts";

export function acmeStatus(status: string): string {
  return (
    (
      {
        issuing: "Issuing certificates",
        ready: "Waiting to deploy",
        deploying: "Rolling HTTPS update",
        active: "Renewal enabled",
        error: "Needs attention",
        disabled: "Renewal disabled",
        pending: "Waiting",
        failed: "Failed",
      } as Record<string, string>
    )[status] ?? status
  );
}
export function acmeError(code: string): string {
  return (
    (
      {
        delegation_missing:
          "Publish the validation CNAME through DNS review, then allow time for DNS propagation.",
        dns_update_failed:
          "The node could not update or clean up its validation TXT value. Check its scoped DNS credentials.",
        dns_propagation_failed:
          "The validation TXT value did not propagate in time.",
        acme_unavailable:
          "The node could not complete a request to the certificate authority.",
        acme_validation_failed:
          "The certificate authority could not validate DNS ownership.",
        acme_rate_limited:
          "The certificate authority rate limit was reached. The requested retry delay is preserved.",
        certificate_invalid:
          "The issued certificate did not pass local identity and trust checks.",
        certificate_storage_full:
          "The node’s retained certificate storage is full. Archive unneeded generations locally before retrying.",
        policy_unavailable:
          "Restore the node’s local ACME policy before retrying.",
        authority_expired:
          "Issuance authority expired before the node finished.",
        issuance_timeout:
          "Issuance timed out. The same generation will resume automatically.",
        local_storage_failed:
          "The node could not persist its private issuance journal.",
        activation_failed:
          "The HTTPS rollout failed. Inspect Rolling configuration and recovery before re-enabling renewal.",
        authority_changed:
          "The cluster nodes, applications, topology or local policy changed. Disable and set up automatic TLS again.",
      } as Record<string, string>
    )[code] ??
    "The node could not complete issuance. Check its local ACME and DNS policy."
  );
}
export function acmePrimaryAction(
  plan:
    | Pick<HAProxyACMEPlan, "enabled" | "status" | "error_code" | "jobs">
    | null
    | undefined,
  now = Date.now(),
): {
  action: Exclude<HAProxyACMEInput["action"], "disable"> | null;
  label: string;
  description: string;
  disabledReason?: string;
} {
  if (plan?.status === "deploying")
    return {
      action: null,
      label: "HTTPS update in progress",
      description:
        "The manager is updating one node at a time. Follow drain, health checks and rejoin under Rollouts.",
    };
  if (!plan?.enabled)
    return {
      action: "enable",
      label: "Enable automatic TLS",
      description:
        "Authorizes certificate issuance and future renewals for these hostnames. Each node keeps its private key, and new certificates are deployed through a rolling update.",
    };
  if (plan.status === "error") {
    const description =
      "Retries failed certificate requests. Nodes that already finished keep their new certificates; existing HTTPS keeps using the serving certificates until a verified rolling update.";
    if (["activation_failed", "authority_changed"].includes(plan.error_code))
      return {
        action: "retry",
        label: "Retry certificate issuance",
        description,
        disabledReason:
          "Resolve recovery, then turn automatic renewal off and set it up again.",
      };
    const retryAt = Math.max(
      0,
      ...plan.jobs
        .filter((job) => job.status === "failed")
        .map((job) => job.next_try_ms),
    );
    return {
      action: "retry",
      label: "Retry certificate issuance",
      description,
      ...(retryAt > now
        ? {
            disabledReason: `The certificate authority's retry delay ends at ${new Date(retryAt).toLocaleString()}.`,
          }
        : {}),
    };
  }
  if (plan.status === "active")
    return {
      action: "renew",
      label: "Renew certificates now",
      description:
        "Requests new certificates on every node, then deploys them through a rolling update. Existing HTTPS keeps using the serving certificates until verification succeeds.",
    };
  return {
    action: null,
    label:
      plan.status === "ready"
        ? "Certificates ready for deployment"
        : "Certificate issuance in progress",
    description:
      plan.status === "ready"
        ? "All node certificates are ready. Activation waits for fresh healthy nodes and for any maintenance or rollout to finish, then updates one node at a time. Follow progress under Rollouts."
        : "The agents are completing the current certificate requests and retry temporary failures automatically. Existing serving certificates stay installed.",
  };
}

const date = (ms: number) =>
  ms ? new Date(ms).toLocaleString() : "Not scheduled";
export function HAProxyAutomaticTLS({
  client,
  cluster,
  nodeNames,
  permissionReason,
  dnsPermissionReason,
}: {
  client: HAProxyClient;
  cluster: HAProxyCluster;
  nodeNames: Record<string, string>;
  permissionReason?: string | undefined;
  dnsPermissionReason?: string | undefined;
}) {
  const { runAction } = usePermissionAction();
  const [view, setView] = useState<HAProxyACMEView | null>(null);
  const [apps, setApps] = useState<HAProxyApplicationRevision[]>([]);
  const [health, setHealth] = useState<HAProxyApplicationHealth[]>([]);
  const [open, setOpen] = useState(false);
  const [issuer, setIssuer] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const submitting = useRef(false);
  const lifetime = useRef<AbortController | null>(null);
  const refresh = useRef<() => Promise<void>>(async () => {});
  const [delegation, setDelegation] = useState<{
    revision: HAProxyApplicationRevision;
    issuer: string;
    target: string;
  } | null>(null);
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
        const [v, a, h] = await Promise.all([
          client.certificates(cluster.id, controller.signal),
          client.applications(cluster.id, controller.signal),
          client.applicationHealth(cluster.id, controller.signal),
        ]);
        if (!controller.signal.aborted) {
          setView(v);
          setApps(a);
          setHealth(h);
          setError(null);
          setIssuer(
            (old) => old || v.plan?.issuer || v.options[0]?.issuer || "",
          );
        }
      } catch (e) {
        if (!controller.signal.aborted) setError(haproxyErrorMessage(e));
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
  }, [client, cluster.id]);
  const plan = view?.plan;
  const serving = plan?.history.filter((h) => h.status === "succeeded").at(-1);
  const expiring =
    serving &&
    Object.values(serving.certificates).some(
      (c) =>
        Date.now() >=
        c.not_before_ms + (c.not_after_ms - c.not_before_ms) * 0.8,
    );

  const option = view?.options.find(
    (o) => o.issuer === issuer && o.binding.name === cluster.topology?.frontend,
  );
  const latest = apps.filter(
    (a) =>
      !apps.some(
        (other) =>
          other.application_id === a.application_id && other.number > a.number,
      ),
  );
  const hostnames = [
    ...new Set(
      latest
        .filter((a) => a.spec.listener === cluster.topology?.frontend)
        .map((a) => a.spec.hostname),
    ),
  ].sort();
  const reason =
    permissionReason ??
    (error
      ? "Refresh certificate automation before changing it."
      : !view
        ? "Loading certificate automation."
        : undefined);
  const enableReason =
    reason ??
    (plan?.status === "deploying"
      ? "Wait for the current HTTPS update to finish."
      : !option
        ? "Configure matching local ACME and TLS policies on every node first."
        : !cluster.topology?.control
          ? "Bind the cluster’s managed entrypoint first."
          : hostnames.length === 0
            ? "Deploy an application on this listener first."
            : undefined);
  const renewalReason =
    reason ??
    (busy
      ? "Wait for the current certificate request to finish."
      : !plan
        ? "Set up automatic TLS before enabling renewal."
        : !plan.enabled
          ? enableReason
          : undefined);
  const primary = acmePrimaryAction(plan);
  const primaryReason =
    reason ??
    (busy
      ? "Wait for the current certificate request to finish."
      : primary.action === "enable"
        ? enableReason
        : primary.disabledReason);
  async function act(action: HAProxyACMEInput["action"]) {
    if (
      submitting.current ||
      reason ||
      (action === "enable" && enableReason) ||
      ((action === "retry" || action === "renew") &&
        (primary.action !== action || primaryReason))
    )
      return;
    const binding = option?.binding ?? plan?.binding;
    if (!binding) return;
    submitting.current = true;
    setBusy(true);
    setActionError(null);
    const signal = lifetime.current!.signal;
    try {
      await runAction(async () => {
        await client.login(true);
        await client.setCertificates(cluster.id, {
          request_id: crypto.randomUUID(),
          expected_version: plan?.version ?? 0,
          action,
          issuer: option?.issuer ?? plan!.issuer,
          binding,
        });
      });
      if (!signal.aborted) {
        setNotice(
          action === "disable"
            ? "Automatic renewal disabled. Existing serving certificates remain installed."
            : action === "enable"
              ? "Automatic TLS enabled. Each node will validate DNS and issue its own key and certificate."
              : action === "retry"
                ? "Failed certificate requests queued for retry. Existing serving certificates stay installed."
                : "New certificate requests started. Serving certificates stay installed until a verified rolling update.",
        );
        await refresh.current();
      }
    } catch (e) {
      if (!signal.aborted && !isActionCancelled(e))
        setActionError(haproxyErrorMessage(e));
    } finally {
      submitting.current = false;
      if (!signal.aborted) setBusy(false);
    }
  }
  return (
    <>
      <Section
        layout="stack"
        size="sm"
        title="Automatic TLS"
        description="Issue and renew certificates with DNS-01. Each node keeps its own private keys and uses your locally configured validation authority."
        actions={
          <Button
            onPress={() => {
              setActionError(null);
              setOpen(true);
            }}
          >
            Manage automatic TLS
          </Button>
        }
      >
        {!open && actionError && (
          <InlineTip variant="error" role="alert">
            {actionError}
          </InlineTip>
        )}
        {!open && notice && (
          <InlineTip variant="success" role="status">
            {notice}
          </InlineTip>
        )}
        {view && (
          <Switch
            label="Automatic renewal"
            description="Renew certificates before expiry. Turning this off keeps the serving certificates installed."
            isSelected={!!plan?.enabled}
            disabledReason={renewalReason}
            aria-busy={busy}
            onChange={(enabled) => {
              if (!renewalReason) void act(enabled ? "enable" : "disable");
            }}
          />
        )}
        {expiring && (
          <InlineTip variant="warning">
            Serving certificates are approaching expiry. Restore issuance or
            install a valid replacement before the earliest node certificate
            expires.
          </InlineTip>
        )}
        {error && (
          <InlineTip variant="warning">
            {error} Last received certificate details remain visible.
          </InlineTip>
        )}
        {!view && !error ? (
          <LoadingStatus delayMs={1000}>
            Loading certificate automation…
          </LoadingStatus>
        ) : plan ? (
          <DescriptionList layout="inline">
            <DescriptionItem label="State">
              <Badge
                tone={
                  plan.status === "active"
                    ? "success"
                    : plan.status === "error"
                      ? "error"
                      : "neutral"
                }
              >
                {acmeStatus(plan.status)}
              </Badge>
            </DescriptionItem>
            <DescriptionItem label="Issuer">
              {plan.issuer} ({plan.environment})
            </DescriptionItem>
            <DescriptionItem label="Next renewal">
              {date(plan.enabled ? plan.renew_at_ms : 0)}
            </DescriptionItem>
          </DescriptionList>
        ) : (
          <InlineTip>
            Automatic TLS is not configured. Set up DNS delegation and select an
            issuer granted on your nodes.
          </InlineTip>
        )}
      </Section>
      <Drawer
        size="lg"
        isOpen={open && !delegation}
        isDismissable={!busy}
        onOpenChange={(v) => {
          if (!busy) setOpen(v);
        }}
      >
        <DrawerDialog
          title="Automatic TLS"
          description={cluster.name}
          hideCloseButton={busy}
        >
          <DialogForm
            onSubmit={() => {
              if (primary.action && !primaryReason) void act(primary.action);
            }}
          >
            <DrawerBody>
              {actionError && (
                <InlineTip variant="error" role="alert">
                  {actionError}
                </InlineTip>
              )}
              {notice && (
                <InlineTip variant="success" role="status">
                  {notice}
                </InlineTip>
              )}
              {error && (
                <InlineTip variant="warning">
                  {error}
                  <Button onPress={() => void refresh.current()}>
                    Refresh
                  </Button>
                </InlineTip>
              )}
              <Section
                layout="stack"
                size="sm"
                title="Issuer and listener"
                description="Node policy controls the certificate authority, DNS credentials and certificate storage. Enabling authorizes ongoing renewal and rolling activation for these nodes and hostnames."
              >
                <DialogBody>
                  <Select
                    label="Local issuer"
                    selectedKey={issuer}
                    onSelectionChange={(key) => setIssuer(String(key))}
                    isDisabled={busy || !!plan?.enabled}
                    options={(view?.options ?? [])
                      .filter(
                        (o) => o.binding.name === cluster.topology?.frontend,
                      )
                      .map((o) => ({
                        id: o.issuer,
                        label: `${o.issuer} – ${o.environment}`,
                      }))}
                  />
                  <TextField
                    label="HTTPS listener"
                    isReadOnly
                    value={
                      option
                        ? `${option.binding.name} – ${option.binding.address}:${option.binding.port}`
                        : plan
                          ? `${plan.binding.name} – ${plan.binding.address}:${plan.binding.port}`
                          : "No shared local binding"
                    }
                  />
                  <TextField
                    label="Application hostnames"
                    isReadOnly
                    value={(plan?.enabled ? plan.hostnames : hostnames).join(
                      ", ",
                    )}
                  />
                  {!option && (
                    <InlineTip>
                      Enable a local ACME DNS-01 policy and TLS binding on every
                      node. The first solver supports scoped RFC 2136 updates to
                      a dedicated validation zone.
                    </InlineTip>
                  )}
                  {(option?.environment ?? plan?.environment) === "staging" && (
                    <InlineTip variant="warning">
                      This issuer is for testing. Its certificates are not
                      trusted by public browsers.
                    </InlineTip>
                  )}
                </DialogBody>
              </Section>
              <Section
                layout="stack"
                size="sm"
                title="DNS delegation"
                description="Create each validation CNAME with your DNS provider through your normal DNS change process. Agents check the published delegation before every issuance. Application traffic records stay independent."
              >
                <PaginatedDataTable
                  aria-label="TLS validation delegation"
                  itemLabel="challenges"
                  rows={(option?.challenges ?? plan?.challenges ?? [])
                    .filter((c) => hostnames.includes(c.hostname))
                    .map((c) => ({ ...c, id: c.hostname }))}
                  rowLabel={(c) => c.hostname}
                  columns={[
                    {
                      id: "hostname",
                      header: "Validation name",
                      isRowHeader: true,
                      cell: (c) => `_acme-challenge.${c.hostname}`,
                    },
                    {
                      id: "target",
                      header: "CNAME target",
                      cell: (c) => c.target,
                    },
                    {
                      id: "prepare",
                      header: "DNS",
                      cell: (c) => {
                        const app = latest.find(
                          (a) => a.spec.hostname === c.hostname,
                        );
                        const h = health.find(
                          (h) => h.application_id === app?.application_id,
                        );
                        const why =
                          reason ??
                          dnsPermissionReason ??
                          (!app ||
                          app.status !== "succeeded" ||
                          h?.configuration !== "loaded" ||
                          h.reachability !== "reachable"
                            ? "Wait for fresh passing application checks on every node."
                            : undefined);
                        return (
                          <Button
                            size="sm"
                            disabledReason={why}
                            onPress={() => {
                              if (app)
                                setDelegation({
                                  revision: app,
                                  issuer: issuer || plan!.issuer,
                                  target: c.target,
                                });
                            }}
                          >
                            DNS delegation instructions
                          </Button>
                        );
                      },
                    },
                  ]}
                />
              </Section>
              {plan && (
                <>
                  <Section
                    layout="stack"
                    size="sm"
                    title={`Generation ${plan.generation}`}
                    description={acmeStatus(plan.status)}
                  >
                    {plan.error_code && (
                      <InlineTip variant="error" role="alert">
                        {acmeError(plan.error_code)}
                      </InlineTip>
                    )}
                    <DescriptionList columns={2}>
                      <DescriptionItem label="Next renewal">
                        {date(plan.enabled ? plan.renew_at_ms : 0)}
                      </DescriptionItem>
                      <DescriptionItem label="Renewal authority">
                        {plan.enabled ? "Enabled for this cluster" : "Disabled"}
                      </DescriptionItem>
                    </DescriptionList>
                    <PaginatedDataTable
                      aria-label="Node certificate issuance"
                      itemLabel="nodes"
                      rows={plan.jobs}
                      rowLabel={(j) => nodeNames[j.node_id] ?? j.node_id}
                      columns={[
                        {
                          id: "node",
                          header: "Node",
                          isRowHeader: true,
                          cell: (j) => nodeNames[j.node_id] ?? j.node_id,
                        },
                        {
                          id: "status",
                          header: "Issuance",
                          cell: (j) => (
                            <Badge
                              tone={
                                j.status === "ready"
                                  ? "success"
                                  : j.status === "failed"
                                    ? "error"
                                    : "neutral"
                              }
                            >
                              {j.status === "ready"
                                ? "Issued"
                                : acmeStatus(j.status)}
                            </Badge>
                          ),
                        },
                        {
                          id: "expiry",
                          header: "Certificate expires",
                          cell: (j) =>
                            j.certificate
                              ? date(j.certificate.not_after_ms)
                              : "Pending",
                        },
                        {
                          id: "retry",
                          header: "Retry",
                          cell: (j) =>
                            j.error_code ? (
                              <span title={acmeError(j.error_code)}>
                                {j.status === "failed"
                                  ? "Needs attention"
                                  : date(j.next_try_ms)}
                              </span>
                            ) : (
                              "—"
                            ),
                        },
                      ]}
                    />
                    {plan.jobs
                      .filter((j) => j.error_code)
                      .map((j) => (
                        <InlineTip key={j.id} variant="warning">
                          {nodeNames[j.node_id] ?? j.node_id}:{" "}
                          {acmeError(j.error_code)}{" "}
                          {plan.enabled &&
                          plan.status === "issuing" &&
                          j.status !== "failed"
                            ? `Automatic retry ${j.next_try_ms > Date.now() ? `at ${date(j.next_try_ms)}` : "is queued"}.`
                            : "Resolve the reported issue before retrying."}
                        </InlineTip>
                      ))}
                    <InlineTip>
                      All node certificates must be ready before activation.
                      Follow drain, update, health checks and rejoin under
                      Rolling configuration. A failed issuance leaves the
                      serving certificate unchanged.
                    </InlineTip>
                  </Section>
                  <Section
                    layout="stack"
                    size="sm"
                    title="Certificate history"
                    description="Public metadata only. Listener history can restore retained, valid certificates; automatic renewal remains separately controlled."
                  >
                    <PaginatedDataTable
                      aria-label="Automatic TLS history"
                      itemLabel="versions"
                      rows={[...plan.history]
                        .reverse()
                        .map((h) => ({ ...h, id: String(h.generation) }))}
                      rowLabel={(h) => `Generation ${h.generation}`}
                      columns={[
                        {
                          id: "version",
                          header: "Generation",
                          isRowHeader: true,
                          cell: (h) => String(h.generation),
                        },
                        {
                          id: "status",
                          header: "Deployment",
                          cell: (h) => rolloutStatus(h.status),
                        },
                        {
                          id: "date",
                          header: "Completed",
                          cell: (h) => date(h.completed_at_ms),
                        },
                        {
                          id: "nodes",
                          header: "Certificates",
                          cell: (h) =>
                            `${Object.keys(h.certificates).length} nodes`,
                        },
                      ]}
                    />
                  </Section>
                </>
              )}
            </DrawerBody>
            <DrawerFooter description={primary.description}>
              {primary.action && (
                <Button
                  type="submit"
                  variant="primary"
                  disabledReason={primaryReason}
                  isLoading={busy}
                >
                  {primary.label}
                </Button>
              )}
              <Button isDisabled={busy} onPress={() => setOpen(false)}>
                {primary.action === "enable" ? "Cancel" : "Close"}
              </Button>
            </DrawerFooter>
          </DialogForm>
        </DrawerDialog>
      </Drawer>
      {delegation && (
        <HAProxyConnectDNS
          client={client}
          revision={delegation.revision}
          health={health.find(
            (h) => h.application_id === delegation.revision.application_id,
          )}
          permissionReason={reason ?? dnsPermissionReason}
          delegation={{ issuer: delegation.issuer, target: delegation.target }}
          onClose={() => setDelegation(null)}
        />
      )}
    </>
  );
}
