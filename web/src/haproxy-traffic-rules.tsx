import { ConnectionIssue } from "./connection-health";
import { useEffect, useState } from "react";
import {
  Badge,
  Button,
  CodeBlock,
  DataTable,
  PaginatedDataTable,
  DescriptionItem,
  DescriptionList,
  DialogForm,
  Drawer,
  DrawerBody,
  DrawerDialog,
  DrawerFooter,
  EmptyState,
  InlineTip,
  Link,
  LoadingStatus,
  Menu,
  MenuItem,
  RemoveButton,
  Section,
  Select,
  TextField,
  Toolbar,
} from "@ecraela/ui";
import {
  haproxyErrorMessage,
  type HAProxyApplicationRevision,
  type HAProxyClient,
  type HAProxyConfigurationView,
} from "./haproxy-api";
import {
  trafficDraft,
  trafficPolicyInput,
  trafficLogFormat,
  type TrafficPolicy,
  type TrafficRuleCounts,
  type TrafficRuleDraft,
} from "./haproxy-traffic-model";

const number = (n: number) => new Intl.NumberFormat().format(n);
export function HAProxyTrafficRules({
  client,
  clusterId,
  revision,
  effective,
  nodeIds,
  nodeNames,
  busy,
  error,
  permissionReason,
  onSave,
  requestHref,
}: {
  client: HAProxyClient;
  clusterId: string;
  revision: HAProxyApplicationRevision;
  effective: HAProxyApplicationRevision;
  nodeIds: string[];
  nodeNames: Record<string, string>;
  busy: boolean;
  error: string | null;
  permissionReason: string | undefined;
  onSave: (
    policy: TrafficPolicy | undefined,
    base: HAProxyApplicationRevision,
  ) => Promise<boolean>;
  requestHref: string;
}) {
  const policy = effective.spec.traffic_policy;
  const [counts, setCounts] = useState<TrafficRuleCounts | null>(null);
  const [nodes, setNodes] = useState<HAProxyConfigurationView[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [updated, setUpdated] = useState(0);
  const [base, setBase] = useState<HAProxyApplicationRevision | null>(null);
  const [rows, setRows] = useState<TrafficRuleDraft[]>([]);
  const [mode, setMode] = useState<TrafficPolicy["mode"]>("observe");
  const [unknown, setUnknown] =
    useState<TrafficPolicy["unknown_action"]>("allow");
  const [validation, setValidation] = useState<string | null>(null);
  const [setup, setSetup] = useState(false);
  const nodeKey = nodeIds.join(",");
  useEffect(() => {
    const abort = new AbortController();
    let pending = false;
    setCounts(null);
    setUpdated(0);
    const update = async () => {
      if (pending || document.hidden) return;
      pending = true;
      try {
        const states = await Promise.all(
          nodeKey
            .split(",")
            .filter(Boolean)
            .map((id) => client.configuration(id, abort.signal)),
        );
        if (!abort.signal.aborted) setNodes(states);
        const result = policy
          ? await client.applicationTrafficRules(
              clusterId,
              effective.application_id,
              effective.id,
              abort.signal,
            )
          : null;
        if (!abort.signal.aborted) {
          setCounts(result);
          setUpdated(Date.now());
          setLoadError(null);
        }
      } catch (e) {
        if (!abort.signal.aborted) setLoadError(haproxyErrorMessage(e));
      } finally {
        pending = false;
      }
    };
    void update();
    const timer = setInterval(() => void update(), 5000);
    document.addEventListener("visibilitychange", update);
    return () => {
      abort.abort();
      clearInterval(timer);
      document.removeEventListener("visibilitychange", update);
    };
  }, [
    client,
    clusterId,
    effective.application_id,
    effective.id,
    nodeKey,
    !!policy,
  ]);
  const changing = !["succeeded", "rolled_back", "rollback_failed"].includes(
    revision.status,
  );
  const reason =
    permissionReason ??
    (busy
      ? "Wait for the current action."
      : changing
        ? "Wait for the application deployment to finish."
        : effective.deleted || effective.spec.disabled
          ? "Re-enable this application before changing its traffic policy."
          : undefined);
  const begin = () => {
    setBase({ ...revision, spec: effective.spec });
    setRows(policy?.rules.map(trafficDraft) ?? []);
    setMode(policy?.mode ?? "observe");
    setUnknown(policy?.unknown_action ?? "allow");
    setValidation(null);
  };
  const save = async () => {
    if (!base || busy) return;
    try {
      const next = trafficPolicyInput(
        mode,
        unknown,
        rows,
        base.spec.path_prefix,
      );
      if (
        !nodes ||
        nodes.some(
          (n) => !n.fresh || !n.control.discovery?.configuration?.traffic_rules,
        )
      )
        throw new Error(
          "Enable the local traffic-rules grant and cv5 capture profile on every node, then wait for fresh discovery.",
        );
      for (const n of nodes) {
        const c = n.control.discovery!.configuration!.traffic_rules!;
        if (
          next.rules.some(
            (r) =>
              (r.countries?.length && !c.country_ready) ||
              (r.asns?.length && !c.asn_ready),
          )
        )
          throw new Error(
            "Every node needs the selected Country/ASN dataset before this policy can deploy.",
          );
      }
      setValidation(null);
      if (await onSave(next, base)) setBase(null);
    } catch (e) {
      setValidation(
        e instanceof Error ? e.message : "Check the traffic rules.",
      );
    }
  };
  const patch = (index: number, value: Partial<TrafficRuleDraft>) =>
    setRows((all) => all.map((r, i) => (i === index ? { ...r, ...value } : r)));
  const move = (index: number, delta: number) =>
    setRows((all) => {
      const next = [...all];
      [next[index], next[index + delta]] = [next[index + delta]!, next[index]!];
      return next;
    });
  const count = (id: string, decision: string) =>
    counts?.matches.find((r) => r.rule_id === id && r.decision === decision)
      ?.requests;
  const href = (id: string) => {
    const [path, search] = requestHref.split("?");
    const q = new URLSearchParams(search);
    q.set("range", "15m");
    q.set("traffic_rule", id);
    if (counts) q.set("spec_digest", counts.spec_digest);
    return path + "?" + q;
  };
  return (
    <>
      <Section
        layout="stack"
        title="Traffic rules"
        description="Ordered access rules run inside this application’s HAProxy backend. The first match wins; other applications on the listener keep their own policy."
      >
        <div className="cv-haproxy-configuration">
          <Toolbar aria-label="Traffic policy actions">
            <Button variant="primary" disabledReason={reason} onPress={begin}>
              {policy ? "Edit traffic policy" : "Create traffic policy"}
            </Button>
            <Button onPress={() => setSetup(true)}>Node setup</Button>
            {policy && (
              <Menu
                aria-label="Traffic policy options"
                trigger={
                  <Button disabledReason={reason}>Policy actions</Button>
                }
              >
                <MenuItem
                  tone="danger"
                  onAction={() =>
                    void onSave(undefined, {
                      ...revision,
                      spec: effective.spec,
                    })
                  }
                >
                  Remove traffic policy
                </MenuItem>
              </Menu>
            )}
          </Toolbar>
          {error && !base && (
            <InlineTip variant="error" role="alert">
              {error}
            </InlineTip>
          )}
          {changing && (
            <InlineTip>
              The application deployment is{" "}
              {revision.status.replaceAll("_", " ")}. Follow its node progress
              under Deployments.
            </InlineTip>
          )}
          {loadError && (
            <ConnectionIssue message="Traffic rules: refresh interrupted; retrying automatically." />
          )}
          {counts && !counts.collection_complete && (
            <InlineTip variant="warning">
              Request collection is incomplete or has reported loss.{" "}
              {counts.reporting_nodes} of {counts.expected_nodes} nodes have
              fresh collectors. Counts can omit matches; zero recorded matches
              does not establish zero matching traffic.
            </InlineTip>
          )}
          {policy ? (
            <>
              <DescriptionList columns={3}>
                <DescriptionItem label="Mode">
                  <Badge tone={policy.mode === "observe" ? "info" : "warning"}>
                    {policy.mode === "observe" ? "Observe only" : "Enforcing"}
                  </Badge>
                </DescriptionItem>
                <DescriptionItem label="Unresolved client or required lookup">
                  {policy.unknown_action === "block" ? "Block" : "Allow"}
                  {policy.mode === "observe" ? " (observed only)" : ""}
                </DescriptionItem>
                <DescriptionItem label="Policy application version">
                  Version {effective.number}
                </DescriptionItem>
              </DescriptionList>
              <DataTable
                aria-label="Application traffic rules"
                rows={policy.rules.map((r, i) => ({ ...r, order: i + 1 }))}
                rowLabel={(r) => r.name}
                columns={[
                  { id: "order", header: "Order", cell: (r) => r.order },
                  {
                    id: "name",
                    header: "Rule",
                    isRowHeader: true,
                    cell: (r) => r.name,
                  },
                  {
                    id: "action",
                    header: "Action",
                    cell: (r) => (
                      <Badge
                        tone={r.action === "block" ? "warning" : "success"}
                      >
                        {policy.mode === "observe"
                          ? "Would " + r.action
                          : r.action === "block"
                            ? "Block (403)"
                            : "Allow"}
                      </Badge>
                    ),
                  },
                  {
                    id: "conditions",
                    header: "Conditions (all)",
                    cell: (r) =>
                      [
                        r.cidrs?.join(", "),
                        r.countries?.join(", "),
                        r.asns?.map((n) => "AS" + n).join(", "),
                      ]
                        .filter(Boolean)
                        .join("; "),
                  },
                  {
                    id: "path",
                    header: "Path prefix",
                    cell: (r) => r.path_prefix ?? "Whole application",
                  },
                  {
                    id: "matches",
                    header: "Recorded matches (15m)",
                    align: "end",
                    cell: (r) => {
                      const n = count(
                        r.id,
                        policy.mode === "observe"
                          ? "would_" + r.action
                          : r.action,
                      );
                      return n === undefined
                        ? counts
                          ? "0"
                          : "Unavailable"
                        : number(n);
                    },
                  },
                  {
                    id: "requests",
                    header: "",
                    cell: (r) =>
                      counts ? (
                        <Link href={href(r.id)}>View requests</Link>
                      ) : (
                        "Waiting for version evidence"
                      ),
                  },
                ]}
              />
              <p>
                Unknown fallback matches:{" "}
                {counts
                  ? number(
                      counts.matches
                        .filter((r) => r.rule_id === "unknown")
                        .reduce((n, r) => n + r.requests, 0),
                    )
                  : "Unavailable"}
                .{" "}
                {counts && (
                  <Link href={href("unknown")}>View unknown decisions</Link>
                )}{" "}
                {updated > 0 && (
                  <>Last refreshed {new Date(updated).toLocaleTimeString()}.</>
                )}
              </p>
              <InlineTip>
                Counts cover recorded requests for this exact application
                specification, including health checks. Collection loss,
                sampling and outages can hide matches. Observe mode leaves
                traffic unchanged; blocking returns HTTP 403. Country/ASN
                estimates do not establish a visitor’s identity or location.
              </InlineTip>
            </>
          ) : (
            <EmptyState
              title="No traffic policy"
              description="Create ordered IP/CIDR, country and ASN rules. Start in observe mode to inspect matches before enforcing."
            />
          )}
          {!nodes && !loadError && (
            <LoadingStatus delayMs={1000}>
              Checking node capabilities…
            </LoadingStatus>
          )}
          {nodes && (
            <PaginatedDataTable
              aria-label="Traffic rule node capabilities"
              itemLabel="nodes"
              rows={nodes.map((n) => ({
                id: n.control.node_id,
                view: n,
                caps: n.control.discovery?.configuration?.traffic_rules,
              }))}
              rowLabel={(r) => nodeNames[r.id] ?? r.id}
              columns={[
                {
                  id: "node",
                  header: "Node",
                  isRowHeader: true,
                  cell: (r) => nodeNames[r.id] ?? r.id,
                },
                {
                  id: "status",
                  header: "Local authority",
                  cell: (r) => (
                    <Badge
                      tone={r.caps && r.view.fresh ? "success" : "warning"}
                    >
                      {r.caps
                        ? r.view.fresh
                          ? "Ready"
                          : "Stale discovery"
                        : "Local setup required"}
                    </Badge>
                  ),
                },
                {
                  id: "source",
                  header: "Client source",
                  cell: (r) =>
                    r.caps?.client_source.replaceAll("_", " ") ?? "Not granted",
                },
                {
                  id: "country",
                  header: "Country data",
                  cell: (r) =>
                    r.caps?.country_ready ? "Ready" : "Unavailable",
                },
                {
                  id: "asn",
                  header: "ASN data",
                  cell: (r) => (r.caps?.asn_ready ? "Ready" : "Unavailable"),
                },
              ]}
            />
          )}
        </div>
      </Section>
      <Drawer
        size="lg"
        isOpen={!!base}
        isDismissable={!busy}
        onOpenChange={(open) => {
          if (!open && !busy) setBase(null);
        }}
      >
        <DrawerDialog
          title={policy ? "Edit traffic policy" : "Create traffic policy"}
          description={
            base
              ? `${base.spec.name} – editing application version ${base.number}`
              : undefined
          }
          hideCloseButton={busy}
        >
          <DialogForm onSubmit={() => void save()}>
            <DrawerBody>
              {(validation || error) && (
                <InlineTip variant="error" role="alert">
                  {validation || error}
                </InlineTip>
              )}
              <Select
                label="Policy mode"
                value={mode}
                onChange={(v) => setMode(String(v) as TrafficPolicy["mode"])}
                isDisabled={busy}
                options={[
                  {
                    id: "observe",
                    label: "Observe – record decisions, allow traffic",
                  },
                  {
                    id: "enforce",
                    label: "Enforce – apply allow/block decisions",
                  },
                ]}
              />
              <Select
                label="Unknown client or required lookup"
                value={unknown}
                onChange={(v) =>
                  setUnknown(String(v) as TrafficPolicy["unknown_action"])
                }
                isDisabled={busy}
                options={[
                  { id: "allow", label: "Allow" },
                  { id: "block", label: "Block" },
                ]}
              />
              <InlineTip>
                Unknown handling applies when no rule matched and the client
                address or a required Country/ASN lookup could not be resolved.
                Allow also permits malformed forwarding evidence; use a trusted
                ingress that replaces incoming forwarding headers. An earlier
                allow rule can exempt trusted networks. A block can make rollout
                verification fail and trigger automatic rollback.
              </InlineTip>
              {rows.map((r, i) => (
                <Section
                  layout="stack"
                  key={r.id}
                  size="sm"
                  title={`Rule ${i + 1}`}
                >
                  <div className="cv-haproxy-configuration">
                    <TextField
                      label={`Rule ${i + 1} name`}
                      value={r.name}
                      onChange={(name) => patch(i, { name })}
                      maxLength={64}
                      isRequired
                      isDisabled={busy}
                    />
                    <Select
                      label={`Rule ${i + 1} action`}
                      value={r.action}
                      onChange={(v) =>
                        patch(i, { action: String(v) as "allow" | "block" })
                      }
                      options={[
                        { id: "block", label: "Block" },
                        { id: "allow", label: "Allow exception" },
                      ]}
                      isDisabled={busy}
                    />
                    <TextField
                      label={`Rule ${i + 1} IP/CIDR networks`}
                      value={r.cidrs}
                      onChange={(cidrs) => patch(i, { cidrs })}
                      description="Comma-separated IP addresses or canonical CIDR networks. Single addresses use /32 or /128."
                      isDisabled={busy}
                    />
                    <TextField
                      label={`Rule ${i + 1} country codes`}
                      value={r.countries}
                      onChange={(countries) => patch(i, { countries })}
                      description="Two-letter codes, for example RU, GB or JP. Uses the host’s local Country map."
                      isDisabled={busy}
                    />
                    <TextField
                      label={`Rule ${i + 1} ASNs`}
                      value={r.asns}
                      onChange={(asns) => patch(i, { asns })}
                      description="ASN numbers, for example AS15169. Uses the host’s local ASN map."
                      isDisabled={busy}
                    />
                    <TextField
                      label={`Rule ${i + 1} path prefix`}
                      value={r.path}
                      onChange={(path) => patch(i, { path })}
                      description={`Optional; keep inside ${base?.spec.path_prefix ?? "/"}. Different condition types all have to match.`}
                      maxLength={128}
                      isDisabled={busy}
                    />
                    <Toolbar aria-label={`Rule ${i + 1} order`}>
                      <Button
                        disabledReason={
                          busy
                            ? "Deployment is starting."
                            : i === 0
                              ? "Already the first rule."
                              : undefined
                        }
                        onPress={() => move(i, -1)}
                      >
                        Move up
                      </Button>
                      <Button
                        disabledReason={
                          busy
                            ? "Deployment is starting."
                            : i === rows.length - 1
                              ? "Already the last rule."
                              : undefined
                        }
                        onPress={() => move(i, 1)}
                      >
                        Move down
                      </Button>
                      <RemoveButton
                        label={`Remove rule ${i + 1}`}
                        disabledReason={
                          busy ? "Deployment is starting." : undefined
                        }
                        onPress={() =>
                          setRows((all) => all.filter((_, n) => n !== i))
                        }
                      />
                    </Toolbar>
                  </div>
                </Section>
              ))}
              <Button
                disabledReason={
                  busy
                    ? "Deployment is starting."
                    : rows.length >= 8
                      ? "This policy has the maximum eight rules."
                      : undefined
                }
                onPress={() =>
                  setRows((all) => [
                    ...all,
                    {
                      id: crypto.randomUUID(),
                      name: "",
                      action: "block",
                      path: "",
                      cidrs: "",
                      countries: "",
                      asns: "",
                    },
                  ])
                }
              >
                Add rule
              </Button>
              <p>
                Rules run in order and the first match wins. Saving starts a
                rolling application deployment: withdraw, drain, update, verify
                and rejoin each node. Version history retains the policy for
                rollback.
              </p>
            </DrawerBody>
            <DrawerFooter>
              <Button
                type="submit"
                variant="primary"
                isPending={busy}
                disabledReason={permissionReason}
              >
                Deploy policy
              </Button>
              <Button isDisabled={busy} onPress={() => setBase(null)}>
                Cancel
              </Button>
            </DrawerFooter>
          </DialogForm>
        </DrawerDialog>
      </Drawer>
      <Drawer size="lg" isOpen={setup} onOpenChange={setSetup}>
        <DrawerDialog
          title="Traffic rule node setup"
          description="A host administrator grants local authority. This guide does not modify nodes."
        >
          <DrawerBody>
            <p>
              Upgrade the manager, ingest, relay and each agent to a build
              supporting application control version 6. Preserve every identity
              and existing agent argument. Enable the application’s private
              request collection. Older manager processes must not share this
              database.
            </p>
            <CodeBlock
              label="Local grant"
              title="/etc/ecraela-haproxy-agent/traffic.json"
              code={JSON.stringify(
                {
                  version: 1,
                  client_ip_policy: "/etc/ecraela-haproxy-agent/geo.json",
                  maps_directory: "/var/lib/ecraela-haproxy-agent/traffic-maps",
                  haproxy_log_socket: "/run/haproxy/access.sock",
                },
                null,
                2,
              )}
            />
            <p>
              Use the same client-IP policy as the Geography setup. For IP-only
              rules it may omit databases. Country/ASN rules require the
              corresponding licensed local MMDB files. Create an owner-writable
              map directory and mount it read-only in HAProxy at this exact
              path; mount it writable in the agent. No Docker socket is needed.
            </p>
            <CodeBlock
              label="Additional agent argument"
              language="text"
              code="--traffic-rules-policy /etc/ecraela-haproxy-agent/traffic.json"
            />
            <p>
              Use the Geography setup’s transport/accepted-address and XFF
              captures. Replace the private log format with cv5, including its
              two compiler-owned decision fields:
            </p>
            <CodeBlock
              label="Private log format"
              language="text"
              code={trafficLogFormat}
            />
            <InlineTip variant="warning">
              Keep the single private Unix logger and source captures, validate
              HAProxy configuration, and drain/reload one node at a time. A
              PROXY source requires conditional reception from the exact granted
              peer CIDRs. XFF enforcement requires the captured header to remain
              unchanged.
            </InlineTip>
            <p>
              Restart the existing agent without enrolling it again. The local
              map snapshots are immutable and retained for rollback. Include the
              map directory in node backups. Remove application traffic policies
              before replacing host trust or datasets in this first release;
              retain prior files and do not edit hash-named maps.
            </p>
          </DrawerBody>
          <DrawerFooter>
            <Button onPress={() => setSetup(false)}>Close setup</Button>
          </DrawerFooter>
        </DrawerDialog>
      </Drawer>
    </>
  );
}
